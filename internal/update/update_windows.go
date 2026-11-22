//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func backupPath(currentExe string) string { return currentExe + ".bak" }

func findBackupPath(currentExe string) string {
	canonical := backupPath(currentExe)
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	dir := filepath.Dir(currentExe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return canonical
	}
	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".bak") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = filepath.Join(dir, e.Name())
			newestTime = info.ModTime()
		}
	}
	if newest != "" {
		return newest
	}
	return canonical
}

func (u *Updater) HasBackup() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	info, err := os.Stat(findBackupPath(exe))
	return err == nil && !info.IsDir()
}

// buildUpdateScript 渲染 .bat：等进程退出（tasklist + findstr 行内匹配 PID，
// 不能用 /B 行首锚定）→ 清旧备份 → 现 exe 备份成 .bak → 新包 move 上位 →
// certutil 校验落地哈希（不一致回滚）→ 拉起 → 自删。哈希在 Go 侧拷贝前算好，
// bat 只负责拷贝后比对。
func buildUpdateScript(pid int, currentExe, bak, newExe, expectedHash string) string {
	return fmt.Sprintf(`@echo off
echo Waiting for application to close...
set /a timeout=60
:waitloop
tasklist /FI "PID eq %d" /NH 2>NUL | findstr /C:" %d " >NUL
if not errorlevel 1 (
    timeout /t 1 /nobreak >NUL
    set /a timeout-=1
    if %%timeout%% leq 0 (
        echo Update timed out waiting for app to exit, aborting
        exit /b 1
    )
    goto waitloop
)
echo Cleaning stale backups...
for %%%%I in ("%s") do set "BAKDIR=%%%%~dpI"
for %%%%f in ("%%BAKDIR%%*.bak") do if /i not "%%%%f"=="%s" del /F /Q "%%%%f"
echo Backing up current binary...
copy /Y "%s" "%s" >NUL
if errorlevel 1 (
    echo Backup failed, aborting update
    exit /b 1
)
echo Replacing application...
move /Y "%s" "%s" >NUL 2>&1
if errorlevel 1 (
    copy /Y "%s" "%s" >NUL
    if errorlevel 1 (
        echo Update failed!
        exit /b 1
    )
    del /F /Q "%s" >NUL 2>&1
)
echo Verifying integrity...
set "expected=%s"
set "actual="
for /f "skip=1 delims=" %%%%i in ('certutil -hashfile "%s" SHA256') do (
    if not defined actual set "actual=%%%%i"
)
if /i not "%%actual%%"=="%%expected%%" (
    echo Integrity check failed: expected %%expected%%, got %%actual%%
    echo Rolling back to previous version...
    copy /Y "%s" "%s" >NUL
    exit /b 1
)
echo Starting new version...
start "" "%s"
del "%%~f0"
`, pid, pid, bak, bak, currentExe, bak, newExe, currentExe, newExe, currentExe, newExe, expectedHash, currentExe, bak, currentExe, currentExe)
}

// buildRollbackScript 渲染回滚 .bat：等退出 → .bak 拷回 → 拉起 → 自删。
func buildRollbackScript(pid int, bak, currentExe string) string {
	return fmt.Sprintf(`@echo off
echo Waiting for application to close...
set /a timeout=60
:waitloop
tasklist /FI "PID eq %d" /NH 2>NUL | findstr /C:" %d " >NUL
if not errorlevel 1 (
    timeout /t 1 /nobreak >NUL
    set /a timeout-=1
    if %%timeout%% leq 0 (
        echo Rollback timed out waiting for app to exit, aborting
        exit /b 1
    )
    goto waitloop
)
echo Restoring previous version...
copy /Y "%s" "%s" >NUL
if errorlevel 1 (
    echo Rollback failed!
    exit /b 1
)
echo Starting restored version...
start "" "%s"
del "%%~f0"
`, pid, pid, bak, currentExe, currentExe)
}

func (u *Updater) Apply() error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current program path: %w", err)
	}
	payload, err := u.payloadPath()
	if err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return err
	}
	expectedHash, err := sha256OfFile(payload)
	if err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to hash update file: %w", err)
	}

	u.send(Event{Stage: StageApplying})
	scriptPath := filepath.Join(os.TempDir(), "gtasks_updater.bat")
	if err := os.WriteFile(scriptPath, []byte(buildUpdateScript(os.Getpid(), currentExe, backupPath(currentExe), payload, expectedHash)), 0o644); err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to create update script: %w", err)
	}
	cmd := exec.Command("cmd", "/C", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to launch update script: %w", err)
	}
	if u.quit != nil {
		u.quit()
	}
	return nil
}

func (u *Updater) Rollback() error {
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current program path: %w", err)
	}
	bak := findBackupPath(currentExe)
	if _, err := os.Stat(bak); err != nil {
		msg := fmt.Sprintf("no rollback backup found at %s", bak)
		u.send(Event{Stage: StageError, Message: msg})
		return fmt.Errorf("%s", msg)
	}
	scriptPath := filepath.Join(os.TempDir(), "gtasks_rollback.bat")
	if err := os.WriteFile(scriptPath, []byte(buildRollbackScript(os.Getpid(), bak, currentExe)), 0o644); err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to create rollback script: %w", err)
	}
	cmd := exec.Command("cmd", "/C", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to launch rollback script: %w", err)
	}
	if u.quit != nil {
		u.quit()
	}
	return nil
}

//go:build !windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DetectInstalled 在非 Windows 上按安装位置粗判：macOS 看是否跑在 .app 包里，
// Linux 看是否落在 /usr 或 /opt 的包管理路径下；其余当绿色版。
func DetectInstalled() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	dir := filepath.Dir(exe)
	if runtime.GOOS == "darwin" {
		return strings.Contains(dir, ".app/")
	}
	return strings.HasPrefix(dir, "/usr/") || strings.HasPrefix(dir, "/opt/")
}

func backupPath(currentExe string) string { return currentExe + ".bak" }

// findBackupPath 优先取规范的 <exe>.bak；不存在时退化成同目录下最新的
// *.bak（早期版本留下的旧名字备份也能用于回滚）。都没有时返回规范路径，
// 让调用方的报错信息保持准确。
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

// HasBackup 报告当前二进制是否存在可回滚的备份。
func (u *Updater) HasBackup() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	info, err := os.Stat(findBackupPath(exe))
	return err == nil && !info.IsDir()
}

// shellQuote 用单引号包裹，内部单引号按 shell 惯用法转义：含空格 / $ / 反引号的
// 路径（/Applications 下的中文目录之类）都能安全进 shell。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeTempScript 用 CreateTemp 写脚本：O_CREATE|O_EXCL 不跟随预置符号链接、
// 不覆盖已有文件，随机后缀消掉固定 /tmp 文件名的 TOCTOU 面。
func writeTempScript(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp script: %w", err)
	}
	scriptPath := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(scriptPath)
		return "", fmt.Errorf("failed to write temp script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(scriptPath)
		return "", fmt.Errorf("failed to write temp script: %w", err)
	}
	if err := os.Chmod(scriptPath, 0700); err != nil {
		os.Remove(scriptPath)
		return "", fmt.Errorf("failed to make temp script executable: %w", err)
	}
	return scriptPath, nil
}

// bundlePath 从 bundle 内二进制路径反推 .app 路径；不在 bundle 里返回空串
// （Linux 的各种布局都走这条路）。
func bundlePath(exe string) string {
	const marker = ".app/Contents/MacOS/"
	idx := strings.Index(exe, marker)
	if idx == -1 {
		return ""
	}
	return exe[:idx+len(".app")]
}

// buildRelaunchSnippet 优先 `open` 整个 .app（走 LaunchServices， Dock / 焦点
// 都正常）；bundle 外退化成 nohup 直接拉起二进制。
func buildRelaunchSnippet(exeQ, bundleQ string) string {
	if bundleQ == "''" {
		return fmt.Sprintf("nohup %s > /dev/null 2>&1 &", exeQ)
	}
	return fmt.Sprintf(`if command -v open > /dev/null 2>&1; then
    open %s
else
    nohup %s > /dev/null 2>&1 &
fi`, bundleQ, exeQ)
}

// buildBundleVersionSync 自升级只换 Contents/MacOS/gtasks，不换 Info.plist；
// 不同步的话 Finder「显示简介」/ LaunchServices 读到的还是 dmg 打包时的版本。
// 版本直接从落地的新二进制读（它自己 -version 会打印），并过一遍严格字符集
// 再进 PlistBuddy，避免脏输出注入 shell。bundle 外整段是 no-op。
func buildBundleVersionSync(exeQ string) string {
	return fmt.Sprintf(`plist=$(dirname %s)/../Info.plist
if [ -f "$plist" ] && [ -x /usr/libexec/PlistBuddy ]; then
    new_ver=$(%s -version 2>/dev/null | awk '{print $1; exit}')
    case "$new_ver" in
    '' | *[!A-Za-z0-9._+-]*) new_ver="" ;;
    esac
    if [ -n "$new_ver" ]; then
        echo "Syncing bundle version to $new_ver..."
        for key in CFBundleShortVersionString CFBundleVersion; do
            /usr/libexec/PlistBuddy -c "Set :$key $new_ver" "$plist" 2>/dev/null ||
                /usr/libexec/PlistBuddy -c "Add :$key string $new_ver" "$plist" 2>/dev/null || true
        done
    fi
fi`, exeQ, exeQ)
}

// buildUnixUpdateScript 渲染替换脚本：等进程退出 → 清旧备份 → 现二进制改名
// .bak → 新包原子 mv 上位 → chmod +x → 校验落地哈希（不一致回滚）→ 同步
// bundle 版本 → 重新拉起 → 自删。全程 mv 优先（同 FS 原子），跨设备才 cp+rm。
func buildUnixUpdateScript(pid int, currentExe, bak, newExe, expectedHash string) string {
	exeQ := shellQuote(currentExe)
	bakQ := shellQuote(bak)
	newQ := shellQuote(newExe)
	hashQ := shellQuote(expectedHash)
	bundleQ := shellQuote(bundlePath(currentExe))
	plistSync := buildBundleVersionSync(exeQ)
	relaunch := buildRelaunchSnippet(exeQ, bundleQ)
	return fmt.Sprintf(`#!/bin/sh
expected_hash=%s
echo "Waiting for application to close..."
timeout=60
while kill -0 %d 2>/dev/null; do
    sleep 1
    timeout=$((timeout - 1))
    if [ "$timeout" -le 0 ]; then
        echo "Update timed out waiting for app to exit, aborting"
        exit 1
    fi
done
echo "Cleaning stale backups..."
bak_dir=$(dirname %s)
for oldbak in "$bak_dir"/*.bak; do
    [ -e "$oldbak" ] || continue
    [ "$oldbak" = %s ] && continue
    rm -f "$oldbak"
done
echo "Backing up current binary..."
if ! mv -f %s %s 2>/dev/null; then
    cp -f %s %s && rm -f %s
    if [ $? -ne 0 ]; then
        echo "Backup failed, aborting update"
        exit 1
    fi
fi
echo "Replacing application..."
if ! mv -f %s %s 2>/dev/null; then
    cp -f %s %s && rm -f %s
    if [ $? -ne 0 ]; then
        echo "Update failed! Restoring backup..."
        mv -f %s %s 2>/dev/null || cp -f %s %s
        exit 1
    fi
fi
chmod +x %s
echo "Verifying integrity..."
hash_output=$(sha256sum %s 2>/dev/null || shasum -a 256 %s)
actual_hash=$(echo "$hash_output" | cut -d' ' -f1)
if [ "$actual_hash" != "$expected_hash" ]; then
    echo "Checksum mismatch! Restoring backup..."
    mv -f %s %s 2>/dev/null || cp -f %s %s
    exit 1
fi
%s
echo "Starting new version..."
%s
rm -f "$0"
`, hashQ, pid, bakQ, bakQ, exeQ, bakQ, exeQ, bakQ, exeQ, newQ, exeQ, newQ, exeQ, newQ, bakQ, exeQ, bakQ, exeQ, exeQ, exeQ, exeQ, bakQ, exeQ, bakQ, exeQ, plistSync, relaunch)
}

// buildUnixRollbackScript 渲染回滚脚本：等退出 → .bak 归位 → 同步 bundle
// 版本 → 拉起 → 自删。
func buildUnixRollbackScript(pid int, bak, currentExe string) string {
	exeQ := shellQuote(currentExe)
	bakQ := shellQuote(bak)
	plistSync := buildBundleVersionSync(exeQ)
	relaunch := buildRelaunchSnippet(exeQ, shellQuote(bundlePath(currentExe)))
	return fmt.Sprintf(`#!/bin/sh
echo "Waiting for application to close..."
timeout=60
while kill -0 %d 2>/dev/null; do
    sleep 1
    timeout=$((timeout - 1))
    if [ "$timeout" -le 0 ]; then
        echo "Rollback timed out waiting for app to exit, aborting"
        exit 1
    fi
done
echo "Restoring previous version..."
if ! mv -f %s %s 2>/dev/null; then
    cp -f %s %s && rm -f %s
    if [ $? -ne 0 ]; then
        echo "Rollback failed!"
        exit 1
    fi
fi
chmod +x %s
%s
echo "Starting restored version..."
%s
rm -f "$0"
`, pid, bakQ, exeQ, bakQ, exeQ, bakQ, exeQ, plistSync, relaunch)
}

// Apply 起后台脚本完成替换，然后退出当前进程把舞台让给脚本。
// 脚本里比对的是「下载后、拷贝前」在 Go 侧算好的哈希，能抓住拷贝残缺 /
// 临时文件被换掉这类问题。
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
	scriptContent := buildUnixUpdateScript(os.Getpid(), currentExe, backupPath(currentExe), payload, expectedHash)
	scriptPath, err := writeTempScript("gtasks_updater_*.sh", scriptContent)
	if err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		os.Remove(scriptPath)
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to launch update script: %w", err)
	}
	if u.quit != nil {
		u.quit()
	}
	return nil
}

// Rollback 把上一次 Apply 留下的 .bak 换回来；没有备份时明确报错。
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
	scriptContent := buildUnixRollbackScript(os.Getpid(), bak, currentExe)
	scriptPath, err := writeTempScript("gtasks_rollback_*.sh", scriptContent)
	if err != nil {
		u.send(Event{Stage: StageError, Message: err.Error()})
		return err
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		os.Remove(scriptPath)
		u.send(Event{Stage: StageError, Message: err.Error()})
		return fmt.Errorf("failed to launch rollback script: %w", err)
	}
	if u.quit != nil {
		u.quit()
	}
	return nil
}

//go:build windows

package update

import (
	"strings"
	"testing"
)

func TestBuildUpdateScript(t *testing.T) {
	script := buildUpdateScript(4242, `C:\Program Files\GTasks\gtasks.exe`,
		`C:\Program Files\GTasks\gtasks.exe.bak`, `C:\Temp\gtasks_update_new.exe`, "abc123")
	for _, want := range []string{
		`PID eq 4242`,
		`findstr /C:" 4242 "`,
		"certutil -hashfile",
		"abc123",
		`start "" "C:\Program Files\GTasks\gtasks.exe"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("update script missing %q", want)
		}
	}
}

func TestBuildRollbackScript(t *testing.T) {
	script := buildRollbackScript(99, `C:\gtasks.exe.bak`, `C:\gtasks.exe`)
	for _, want := range []string{`PID eq 99`, `copy /Y "C:\gtasks.exe.bak" "C:\gtasks.exe"`} {
		if !strings.Contains(script, want) {
			t.Errorf("rollback script missing %q", want)
		}
	}
}

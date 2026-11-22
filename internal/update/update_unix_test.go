//go:build !windows

package update

import (
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	if got := shellQuote(`/Appl's Dir/gtasks`); got != `'/Appl'\''s Dir/gtasks'` {
		t.Errorf("shellQuote = %q", got)
	}
}

func TestBundlePath(t *testing.T) {
	cases := map[string]string{
		"/Applications/GTasks.app/Contents/MacOS/gtasks": "/Applications/GTasks.app",
		"/usr/local/bin/gtasks":                          "",
	}
	in, want := "/Applications/GTasks.app/Contents/MacOS/gtasks", "/Applications/GTasks.app"
	if got := bundlePath(in); got != want {
		t.Errorf("bundlePath(%q) = %q, want %q", in, got, want)
	}
	if got := bundlePath("/usr/local/bin/gtasks"); got != "" {
		t.Errorf("bundlePath outside bundle = %q, want empty", got)
	}
	_ = cases
}

func TestBuildUnixUpdateScript(t *testing.T) {
	script := buildUnixUpdateScript(4242, "/Applications/GTasks.app/Contents/MacOS/gtasks",
		"/Applications/GTasks.app/Contents/MacOS/gtasks.bak", "/tmp/gtasks_update_payload_abc", "abc123")
	for _, want := range []string{
		"kill -0 4242",
		"expected_hash='abc123'",
		"'/Applications/GTasks.app/Contents/MacOS/gtasks'",
		"'/tmp/gtasks_update_payload_abc'",
		"PlistBuddy",
		"open '/Applications/GTasks.app'",
		"sha256sum",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("update script missing %q", want)
		}
	}
	// bundle 内重启优先 open；nohup 只允许作为 command -v open 失败时的 else 兜底。
	openIdx := strings.Index(script, "open '/Applications/GTasks.app'")
	nohupIdx := strings.Index(script, "nohup '/Applications")
	if openIdx == -1 || nohupIdx == -1 || openIdx > nohupIdx {
		t.Error("bundle relaunch must try open before the nohup fallback")
	}
}

func TestBuildUnixUpdateScriptOutsideBundle(t *testing.T) {
	script := buildUnixUpdateScript(1, "/usr/local/bin/gtasks", "/usr/local/bin/gtasks.bak", "/tmp/payload", "ff")
	if !strings.Contains(script, "nohup '/usr/local/bin/gtasks' > /dev/null 2>&1 &") {
		t.Error("non-bundle relaunch must nohup the binary")
	}
}

func TestBuildUnixRollbackScript(t *testing.T) {
	script := buildUnixRollbackScript(7, "/opt/gtasks.bak", "/opt/gtasks")
	for _, want := range []string{"kill -0 7", "'/opt/gtasks.bak'", "'/opt/gtasks'", "open ''"} {
		if strings.Contains(want, "open ''") {
			continue
		}
		if !strings.Contains(script, want) {
			t.Errorf("rollback script missing %q", want)
		}
	}
	if !strings.Contains(script, "nohup '/opt/gtasks' > /dev/null 2>&1 &") {
		t.Error("rollback outside bundle must nohup the binary")
	}
}

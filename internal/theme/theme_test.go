package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsToSystem(t *testing.T) {
	manager := Load(filepath.Join(t.TempDir(), "theme.txt"))
	if manager.Mode() != System {
		t.Fatalf("空目录加载应默认跟随系统，实际 %q", manager.Mode())
	}
}

func TestSetPersistsAndNotifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.txt")
	manager := Load(path)

	var seen []Mode
	manager.OnChange(func(mode Mode) { seen = append(seen, mode) })
	if len(seen) != 1 || seen[0] != System {
		t.Fatalf("注册回调应立即收到当前模式，实际 %v", seen)
	}

	if !manager.Set(Dark) {
		t.Fatal("切换到暗黑应报告发生变化")
	}
	if manager.Set(Dark) {
		t.Fatal("重复设置同一模式不应报告变化")
	}
	if len(seen) != 2 || seen[1] != Dark {
		t.Fatalf("变更回调应收到暗黑，实际 %v", seen)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取持久化文件失败: %v", err)
	}
	if string(raw) != "dark\n" {
		t.Fatalf("持久化内容应为 dark，实际 %q", raw)
	}

	reloaded := Load(path)
	if reloaded.Mode() != Dark {
		t.Fatalf("重新加载应读回暗黑，实际 %q", reloaded.Mode())
	}
}

func TestParseAndNext(t *testing.T) {
	if Parse("  light ") != Light {
		t.Fatal("Parse 应容忍空白")
	}
	if Parse("nope") != System {
		t.Fatal("无法识别的模式应回落到跟随系统")
	}
	if Light.Next() != Dark || Dark.Next() != System || System.Next() != Light {
		t.Fatal("循环顺序应为 明亮 → 暗黑 → 跟随系统")
	}
}

func TestBackgroundsMatchFrontend(t *testing.T) {
	if got := PageBackground(true); got != (RGB{R: 27, G: 27, B: 27}) {
		t.Fatalf("会话页深色底色应为 #1b1b1b，实际 %+v", got)
	}
	if got := PageBackground(false); got != (RGB{R: 248, G: 250, B: 253}) {
		t.Fatalf("会话页浅色底色应为 #f8fafd，实际 %+v", got)
	}
	if got := AppBackground(true); got != (RGB{R: 32, G: 33, B: 36}) {
		t.Fatalf("主窗口深色底色应与 styles.css 的 --bg 一致，实际 %+v", got)
	}
	if got := AppBackground(false); got != (RGB{R: 255, G: 255, B: 255}) {
		t.Fatalf("主窗口浅色底色应为纯白，实际 %+v", got)
	}
	if got := WorkbenchBackground(); got != (RGB{R: 20, G: 22, B: 26}) {
		t.Fatalf("工作台底色应与 workbench.css 的 --bg 一致，实际 %+v", got)
	}
}

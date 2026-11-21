// Package theme 保存全局外观模式（明亮 / 暗黑 / 跟随系统）并负责下发。
//
// Go 侧是唯一的事实来源：模式落在数据目录里，切换时先改原生外观
// （macOS 上即 NSApp.appearance，标题栏与所有 webview 的
// prefers-color-scheme 会一起变），再通知各个窗口刷新自己的 CSS。
package theme

import (
	"os"
	"strings"
	"sync"
)

// Mode 是用户选择的外观模式。
type Mode string

// 三种可选模式。
const (
	Light  Mode = "light"
	Dark   Mode = "dark"
	System Mode = "system"
)

// Parse 把文本解析成模式，无法识别时回落到跟随系统。
func Parse(text string) Mode {
	switch Mode(strings.TrimSpace(text)) {
	case Light, Dark, System:
		return Mode(strings.TrimSpace(text))
	default:
		return System
	}
}

// Next 返回循环切换的下一档：明亮 → 暗黑 → 跟随系统。
func (m Mode) Next() Mode {
	switch m {
	case Light:
		return Dark
	case Dark:
		return System
	default:
		return Light
	}
}

// EffectiveDark 报告模式实际解析出的深浅：跟随系统时问系统。
func EffectiveDark(mode Mode) bool {
	switch mode {
	case Light:
		return false
	case Dark:
		return true
	default:
		return SystemDark()
	}
}

// RGB 是窗口首屏底色。单独定义一个小结构，免得这个包去依赖具体 GUI 框架的
// 颜色类型（Wails 的 application.RGBA 由调用方自己转）。
type RGB struct {
	R, G, B uint8
}

// PageBackground 是会话页（tasks.google.com）的首屏底色，取自 Google 网页版
// 自己的浅色 #f8fafd 与深色 #1b1b1b：页面第一帧画出来之前 WebKit 涂的就是
// 这个颜色，配错了会先闪一帧白屏。
func PageBackground(dark bool) RGB {
	if dark {
		return RGB{R: 27, G: 27, B: 27}
	}
	return RGB{R: 248, G: 250, B: 253}
}

// WorkbenchBackground 是工作台窗口首屏底色，与 frontend/workbench.css 的
// --bg 一致；工作台固定深色，不跟全局主题走。
func WorkbenchBackground() RGB {
	return RGB{R: 20, G: 22, B: 26}
}

// Manager 持有当前模式并广播变更。
type Manager struct {
	mu        sync.RWMutex
	mode      Mode
	path      string
	listeners []func(Mode)
}

// Load 读取持久化的模式；path 为空或读不到时默认跟随系统。
func Load(path string) *Manager {
	manager := &Manager{mode: System, path: path}
	if path == "" {
		return manager
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return manager
	}
	manager.mode = Parse(string(raw))
	return manager
}

// Mode 返回当前模式。
func (m *Manager) Mode() Mode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mode
}

// OnChange 注册变更回调，注册时会立刻用当前模式回调一次。
func (m *Manager) OnChange(fn func(Mode)) {
	m.mu.Lock()
	m.listeners = append(m.listeners, fn)
	mode := m.mode
	m.mu.Unlock()
	fn(mode)
}

// Set 切换模式；模式没变时不做任何事。返回是否真的发生了变化。
func (m *Manager) Set(mode Mode) bool {
	m.mu.Lock()
	if m.mode == mode {
		m.mu.Unlock()
		return false
	}
	m.mode = mode
	path := m.path
	listeners := append([]func(Mode){}, m.listeners...)
	m.mu.Unlock()

	if path != "" {
		// 落盘失败不影响本次切换，下次启动会回落到跟随系统。
		_ = os.WriteFile(path, []byte(string(mode)+"\n"), 0o600)
	}
	setAppearance(mode)
	for _, fn := range listeners {
		fn(mode)
	}
	return true
}

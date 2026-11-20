//go:build !darwin

package theme

import "unsafe"

// setAppearance 在非 macOS 平台没有全局外观开关：各窗口按自己的 CSS 变量
// 渲染即可，标题栏由系统决定。
func setAppearance(Mode) {}

// ApplyWindow 在非 macOS 平台无操作。
func ApplyWindow(window unsafe.Pointer, mode Mode) {}

// SystemDark 在非 macOS 平台无法可靠探测，统一按浅色处理；跟随系统时各前端
// 用自己的 prefers-color-scheme 媒体查询兜底。
func SystemDark() bool { return false }

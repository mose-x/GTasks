//go:build !darwin || purego

// Package appkit 给主窗口补一个真正的 document-start 用户脚本（仅 macOS）。
// 其他平台没有对应的原生入口：Windows 走 bootstrap 的 stub 重定向，Linux 的
// WebKitGTK 由 WindowLoadStarted 事件兜底，体验差异见各后端注释。
package appkit

// AddDocumentStartScript 在非 macOS 后端是空操作。
func AddDocumentStartScript(title, source string) {}

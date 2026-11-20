//go:build darwin

package theme

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdlib.h>

// 把整个 App 的外观切到指定 NSAppearance 名字；name 为空表示跟随系统。
// NSApp.appearance 会向下传播到没有单独设置 appearance 的窗口，因此标题栏
// 和 WKWebView 的 prefers-color-scheme 会一起变化。
// 单窗口版本：NSApp.appearance 理论上会向下传播，但逐个窗口显式设置才能保证
// 标题栏在切换的那一帧就跟着变（Wails 建窗时可能已经固定过窗口外观）。
static void gtasksSetWindowAppearance(void *window, const char *name) {
	NSString *appearanceName = name == NULL ? nil : [NSString stringWithUTF8String:name];
	NSWindow *nsWindow = (NSWindow *)window;
	if (nsWindow == nil) return;
	dispatch_async(dispatch_get_main_queue(), ^{
		NSAppearance *appearance = nil;
		if (appearanceName.length > 0) {
			appearance = [NSAppearance appearanceNamed:appearanceName];
		}
		[nsWindow setAppearance:appearance];
	});
}

// 系统当前是否深色。调用方只在「跟随系统」模式下问这个值，此时 NSApp 没有
// 显式外观，effectiveAppearance 就是系统外观。
static int gtasksSystemIsDark(void) {
	__block int dark = 0;
	void (^work)(void) = ^{
		NSAppearance *appearance = [NSApp effectiveAppearance];
		NSString *match = [appearance bestMatchFromAppearancesWithNames:
			@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
		dark = [match isEqualToString:NSAppearanceNameDarkAqua] ? 1 : 0;
	};
	if ([NSThread isMainThread]) {
		work();
	} else {
		dispatch_sync(dispatch_get_main_queue(), work);
	}
	return dark;
}

static void gtasksSetAppAppearance(const char *name) {
	NSString *appearanceName = name == NULL ? nil : [NSString stringWithUTF8String:name];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSAppearance *appearance = nil;
		if (appearanceName.length > 0) {
			appearance = [NSAppearance appearanceNamed:appearanceName];
		}
		[NSApp setAppearance:appearance];
	});
}
*/
import "C"

import "unsafe"

// ApplyWindow 把模式落到单个原生窗口上；window 为 nil 时忽略。
func ApplyWindow(window unsafe.Pointer, mode Mode) {
	if window == nil {
		return
	}
	C.gtasksSetWindowAppearance(window, appearanceName(mode))
}

// SystemDark 报告系统当前是否深色外观。
func SystemDark() bool {
	return C.gtasksSystemIsDark() == 1
}

// appearanceName 把模式映射成 NSAppearance 名字，跟随系统返回 nil。
func appearanceName(mode Mode) *C.char {
	switch mode {
	case Light:
		return C.CString("NSAppearanceNameAqua")
	case Dark:
		return C.CString("NSAppearanceNameDarkAqua")
	default:
		return nil
	}
}

// setAppearance 把模式映射到 AppKit 外观。字符串字面量而非 NSAppearanceNameDarkAqua
// 常量，避免 -mmacosx-version-min=10.13 下的可用性告警；老系统上查不到名字时
// appearanceNamed: 返回 nil，等价于跟随系统。
func setAppearance(mode Mode) {
	name := appearanceName(mode)
	if name != nil {
		defer C.free(unsafe.Pointer(name))
	}
	C.gtasksSetAppAppearance(name)
}

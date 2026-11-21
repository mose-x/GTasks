//go:build darwin && !purego

// Package appkit 给主窗口补一个真正的 document-start 用户脚本（仅 macOS）。
//
// Wails v3 在 macOS 上把 WebviewWindowOptions.JS 留到 finish-navigation 才执行，
// 外部_origin 的首帧到加载完成之间没有任何自家代码能跑。wails 的 darwin 后端
// 自己有一个 document-start 注入入口（windowAddDocumentStartScript，本来只给
// fetch body shim 用），这里是同一个二进制里的 C 符号，直接 extern 复用：按窗口
// 标题找到主窗口，把早遮罩脚本挂成 WKUserScript（AtDocumentStart）。
package appkit

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit

#import <AppKit/AppKit.h>
#import <dispatch/dispatch.h>
#import <stdlib.h>

// windowAddDocumentStartScript 由 wails v3 darwin 后端编译进同一个二进制
// （pkg/application/webview_window_darwin.go），签名：
//   void windowAddDocumentStartScript(void* nsWindow, const char* source)
// 它把 source 挂成 WKUserScriptInjectionTimeAtDocumentStart 的用户脚本，
// 对加入之后的每一次导航生效。
extern void windowAddDocumentStartScript(void* nsWindow, const char* source);

struct gtasksScriptTarget {
	const char* title;
	const char* source;
};

static void gtasksAddScriptOnMain(void* context) {
	struct gtasksScriptTarget* target = (struct gtasksScriptTarget*)context;
	NSString* wanted = [NSString stringWithUTF8String:target->title];
	for (NSWindow* window in NSApp.windows) {
		if ([[window title] isEqualToString:wanted]) {
			windowAddDocumentStartScript((void*)window, target->source);
			break;
		}
	}
	free((void*)target->title);
	free((void*)target->source);
	free(target);
}

void gtasksAddDocumentStartScript(const char* title, const char* source) {
	struct gtasksScriptTarget* target =
	    (struct gtasksScriptTarget*)malloc(sizeof(struct gtasksScriptTarget));
	target->title = strdup(title);
	target->source = strdup(source);
	if ([NSThread isMainThread]) {
		gtasksAddScriptOnMain(target);
		return;
	}
	dispatch_async_f(dispatch_get_main_queue(), target, gtasksAddScriptOnMain);
}
*/
import "C"

import "unsafe"

// AddDocumentStartScript 把 source 挂到标题为 title 的原生窗口的 webview 上，
// 作为 document-start 用户脚本（对之后每次导航生效）。找不到窗口时静默跳过：
// 早遮罩只是体验优化，缺了它 bootstrap 仍会在加载完成时接管。
func AddDocumentStartScript(title, source string) {
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cTitle))
	cSource := C.CString(source)
	defer C.free(unsafe.Pointer(cSource))
	C.gtasksAddDocumentStartScript(cTitle, cSource)
}

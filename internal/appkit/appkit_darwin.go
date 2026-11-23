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
#cgo LDFLAGS: -framework AppKit -framework WebKit

#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <dispatch/dispatch.h>
#import <stdlib.h>

// windowAddDocumentStartScript 由 wails v3 darwin 后端编译进同一个二进制
// （pkg/application/webview_window_darwin.go），签名：
//   void windowAddDocumentStartScript(void* nsWindow, const char* source)
// 它把 source 挂成 WKUserScriptInjectionTimeAtDocumentStart 的用户脚本，
// 对加入之后的每一次导航生效。
extern void windowAddDocumentStartScript(void* nsWindow, const char* source);

// 早遮罩脚本的首行注释当作去重标记：重试挂脚本之前先看 WKUserContentController
// 里是否已有同前缀的脚本，避免同一次运行里重复注入。
#define GTASKS_SCRIPT_MARKER "// GTasks document-start"

// gtasksWindowScripts 经 performSelector 取窗口的 webView 用户脚本列表：wails 的
// WailsWebviewWindow 协议头文件不在本编译单元里，但 webView / configuration /
// userContentController / userScripts 都是 WKWebView 的公开 API。
static NSArray* gtasksWindowScripts(NSWindow* window) {
  if (![window respondsToSelector:@selector(webView)]) return nil;
  id webView = [window performSelector:@selector(webView)];
  if (![webView respondsToSelector:@selector(configuration)]) return nil;
  id configuration = [webView performSelector:@selector(configuration)];
  if (![configuration respondsToSelector:@selector(userContentController)]) return nil;
  id controller = [configuration performSelector:@selector(userContentController)];
  if (![controller respondsToSelector:@selector(userScripts)]) return nil;
  return [controller performSelector:@selector(userScripts)];
}

static int gtasksTryAddOnMain(const char* title, const char* source) {
  NSString* wanted = [NSString stringWithUTF8String:title];
  NSString* marker = [NSString stringWithUTF8String:GTASKS_SCRIPT_MARKER];
  for (NSWindow* window in NSApp.windows) {
    if (![[window title] isEqualToString:wanted]) continue;
    for (WKUserScript* script in gtasksWindowScripts(window)) {
      if ([script.source hasPrefix:marker]) return 1;
    }
    windowAddDocumentStartScript((void*)window, source);
    return 1;
  }
  return 0;
}

// 由 Go 的后台 goroutine 调用：同步回主线程找窗口。wails 要进主循环之后才真正
// 建 NSWindow，调用太早时窗口列表还是空的，返回 0 让 Go 侧重试。
int gtasksTryAddDocumentStartScript(const char* title, const char* source) {
  __block int installed = 0;
  dispatch_sync(dispatch_get_main_queue(), ^{
    installed = gtasksTryAddOnMain(title, source);
  });
  return installed;
}
*/
import "C"

import (
	"time"
	"unsafe"
)

// AddDocumentStartScript 把 source 挂到标题为 title 的原生窗口的 webview 上，
// 作为 document-start 用户脚本（对之后每次导航生效）。wails 进主循环之后才真正
// 创建 NSWindow，setup 阶段调用时窗口列表往往还是空的：这里改由后台 goroutine
// 重试到挂上为止（含「已经挂过」的去重判定），保证启动后的每一次导航——包括
// 登录重定向——都有早遮罩 / 九宫格隐藏规则兜底。
func AddDocumentStartScript(title, source string) {
	cTitle := C.CString(title)
	cSource := C.CString(source)
	go func() {
		defer C.free(unsafe.Pointer(cTitle))
		defer C.free(unsafe.Pointer(cSource))
		for attempt := 0; attempt < 240; attempt++ {
			if C.gtasksTryAddDocumentStartScript(cTitle, cSource) == 1 {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

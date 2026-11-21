// Package bridge carries JSON messages between Go and the Google session
// webview, which is loaded from an external origin (tasks.google.com).
package bridge

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// BootstrapJS is injected into the session window via WebviewWindowOptions.JS.
// Wails re-executes it after every finished navigation.
//
//go:embed bootstrap.js
var BootstrapJS string

// EarlyPaintJS 是 document-start 的早遮罩脚本：macOS 上由 internal/appkit 以
// WKUserScript 注入主窗口，抢在 Google 首帧之前盖遮罩 / 藏九宫格（bootstrap 要
// 等 finish-navigation 才跑，盖不住首帧到加载完成那一段）。
//
//go:embed earlypaint.js
var EarlyPaintJS string

// reportsMainFrame records whether the Wails backend fills in
// OriginInfo.IsMainFrame. Only the darwin backend does: the WebView2 and
// WebKitGTK message paths leave the field false for every frame, so enforcing
// it there would silently drop the entire bridge.
var reportsMainFrame = runtime.GOOS == "darwin"

// lineSeparators escapes U+2028/U+2029, which are legal inside a JSON string but
// terminate a JavaScript string literal.
var lineSeparators = strings.NewReplacer("\u2028", `\u2028`, "\u2029", `\u2029`)

const (
	// Protocol tags every message the bootstrap sends so RawMessageHandler can
	// ignore everything else.
	Protocol = "gtask1"
	// messagePrefix is the guaranteed start of an encoded envelope: the
	// bootstrap always inserts the protocol key first.
	messagePrefix = `{"p":"` + Protocol + `"`

	// DefaultTimeout bounds a single page round trip.
	DefaultTimeout = 30 * time.Second
)

var (
	ErrNotBound  = errors.New("bridge: Google 会话窗口尚未绑定")
	ErrNotReady  = errors.New("bridge: 引导脚本尚未在页面中安装，正在加载或登录中")
	ErrNoReply   = errors.New("bridge: 页面未响应，可能尚未登录、正在导航或引导脚本未注入")
	ErrPageError = errors.New("bridge: 页面调用失败")
)

// envelope mirrors the JSON shape produced by bootstrap.js.
type envelope struct {
	P     string          `json:"p"`
	Kind  string          `json:"kind"`
	ID    uint64          `json:"id,omitempty"`
	OK    bool            `json:"ok,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Entry json.RawMessage `json:"entry,omitempty"`
	Info  json.RawMessage `json:"info,omitempty"`
	Name  string          `json:"name,omitempty"`
	Args  map[string]any  `json:"args,omitempty"`
}

// SniffHandler receives one decoded batchexecute capture from the page.
type SniffHandler func(entry json.RawMessage)

// CommandHandler receives a fire-and-forget UI command posted by the page
// (open the workbench, switch theme, show the native context menu, ...).
// It runs on a worker goroutine, never on the webview message thread.
type CommandHandler func(name string, args map[string]any)

// ReadyHandler is invoked once per page load, right after the bootstrap
// announces itself and unlocks ExecJS.
type ReadyHandler func(info json.RawMessage)

// Bridge multiplexes concurrent Go calls onto a single session window.
type Bridge struct {
	mu      sync.Mutex
	seq     uint64
	pending map[uint64]chan *envelope
	window  *application.WebviewWindow
	name    string
	sniff   SniffHandler
	command CommandHandler
	onReady ReadyHandler

	stateMu sync.RWMutex
	ready   bool
	info    json.RawMessage
}

// New returns an unbound bridge. Call Bind once the session window exists.
func New() *Bridge {
	return &Bridge{pending: make(map[uint64]chan *envelope)}
}

// SetSniffHandler registers the callback used for captured RPC traffic.
func (b *Bridge) SetSniffHandler(handler SniffHandler) {
	b.mu.Lock()
	b.sniff = handler
	b.mu.Unlock()
}

// SetCommandHandler registers the callback for UI commands posted by the page.
func (b *Bridge) SetCommandHandler(handler CommandHandler) {
	b.mu.Lock()
	b.command = handler
	b.mu.Unlock()
}

// SetReadyHandler registers the callback fired when the page announces itself.
func (b *Bridge) SetReadyHandler(handler ReadyHandler) {
	b.mu.Lock()
	b.onReady = handler
	b.mu.Unlock()
}

// Bind attaches the session window and records its name for origin filtering.
func (b *Bridge) Bind(window *application.WebviewWindow) {
	b.mu.Lock()
	b.window = window
	b.name = window.Name()
	b.mu.Unlock()
}

// Ready reports whether the bootstrap has announced itself at least once since
// the current page loaded.
func (b *Bridge) Ready() bool {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	return b.ready
}

// MarkNavigating 在会话页开始导航时调用（切语言的 location.replace、主题兜底
// Reload、页面自己跳转都算）：把 ready 打回去，并当场掐掉所有在飞的调用。
//
// 不这么做的话，调用会投进一个正在被拆掉的文档里：老文档的 fetch 被中断，回一句
// "TypeError: Load failed"；新文档还没装好引导脚本，压根不回，Go 侧只能干等到
// 30 秒超时。主界面读数据撞上这段时间就会挂住或报错。
func (b *Bridge) MarkNavigating() {
	b.stateMu.Lock()
	b.ready = false
	b.stateMu.Unlock()

	b.mu.Lock()
	inflight := b.pending
	b.pending = make(map[uint64]chan *envelope, len(inflight))
	b.mu.Unlock()

	// Call 把 nil 回执当作 ErrNoReply，属于「自己会好」的临时故障。
	for _, waiter := range inflight {
		select {
		case waiter <- nil:
		default:
		}
	}
}

// Info returns the most recent probe payload reported by the page.
func (b *Bridge) Info() json.RawMessage {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	return b.info
}

// HandleRaw implements application.Options.RawMessageHandler. It is called for
// every non-reserved message posted by any webview in the application.
func (b *Bridge) HandleRaw(window application.Window, message string, origin *application.OriginInfo) {
	if !strings.HasPrefix(message, messagePrefix) {
		return
	}
	if !fromMainFrame(origin) {
		return
	}

	b.mu.Lock()
	expected := b.name
	b.mu.Unlock()
	if window == nil {
		return
	}
	if expected != "" && window.Name() != expected {
		return
	}

	var decoded envelope
	if err := json.Unmarshal([]byte(message), &decoded); err != nil {
		return
	}

	switch decoded.Kind {
	case "ready":
		b.stateMu.Lock()
		b.ready = true
		b.info = decoded.Info
		b.stateMu.Unlock()
		b.mu.Lock()
		handler := b.onReady
		b.mu.Unlock()
		if handler != nil {
			go handler(decoded.Info)
		}

	case "command":
		b.mu.Lock()
		handler := b.command
		b.mu.Unlock()
		if handler != nil {
			go handler(decoded.Name, decoded.Args)
		}

	case "result":
		b.mu.Lock()
		waiter := b.pending[decoded.ID]
		b.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- &decoded:
			default:
			}
		}

	case "sniff":
		b.mu.Lock()
		handler := b.sniff
		b.mu.Unlock()
		if handler != nil && len(decoded.Entry) > 0 {
			handler(decoded.Entry)
		}

	case "error":
		// Surfaced through the application logger by the caller if needed.
	}
}

// fromMainFrame reports whether origin identifies the window's main frame. Only
// the darwin backend fills the flag in; on Windows and WebKitGTK it is false for
// every frame, so the protocol tag and the window-name filter in HandleRaw are
// the only guards available there.
func fromMainFrame(origin *application.OriginInfo) bool {
	return origin == nil || !reportsMainFrame || origin.IsMainFrame
}

// Call sends a method to the page bootstrap and waits for its reply.
func (b *Bridge) Call(ctx context.Context, method string, args any) (json.RawMessage, error) {
	b.mu.Lock()
	window := b.window
	b.mu.Unlock()
	if window == nil {
		return nil, ErrNotBound
	}
	// ExecJS is queued internally until the page reports "wails:runtime:ready",
	// so calling before the first announce would grow that queue without bound.
	if !b.Ready() {
		return nil, ErrNotReady
	}

	b.mu.Lock()
	b.seq++
	id := b.seq
	waiter := make(chan *envelope, 1)
	b.pending[id] = waiter
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	script, err := dispatchScript(id, method, args)
	if err != nil {
		return nil, err
	}
	window.ExecJS(script)

	timeout := DefaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case reply := <-waiter:
		if reply == nil {
			return nil, ErrNoReply
		}
		if !reply.OK {
			return nil, fmt.Errorf("%w: %s", ErrPageError, reply.Error)
		}
		return reply.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrNoReply
	}
}

// jsStringLiteral renders text as a double-quoted JavaScript string literal.
// encoding/json also escapes <, > and &, so the result is safe inside an HTML
// <script> element as well.
func jsStringLiteral(text string) (string, error) {
	literal, err := json.Marshal(text)
	if err != nil {
		return "", fmt.Errorf("bridge: 编码脚本失败: %w", err)
	}
	return lineSeparators.Replace(string(literal)), nil
}

// dispatchScript renders a Call payload as an inline script. The payload is
// double encoded so that arbitrary JSON becomes a single JS string literal.
func dispatchScript(id uint64, method string, args any) (string, error) {
	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "args": args})
	if err != nil {
		return "", fmt.Errorf("bridge: 编码调用失败: %w", err)
	}
	literal, err := jsStringLiteral(string(payload))
	if err != nil {
		return "", err
	}
	return "window.__gtask&&window.__gtask.dispatch(" + literal + ");", nil
}

// RedirectHTML builds the document used to start the session window on Windows.
//
// The Windows backend applies WebviewWindowOptions.JS only in the HTML branch of
// navigateInitialPage, where it registers the script with
// AddScriptToExecuteOnDocumentCreated; the external-URL branch never sees it.
// Starting from this stub keeps that registration path alive, and since the
// registration lives for the whole webview lifetime, the bootstrap is re-injected
// into every document the login chain produces — at document start, ahead of the
// page's own scripts.
func RedirectHTML(target string) string {
	// json.Marshal on a string cannot fail.
	literal, _ := jsStringLiteral(target)
	return `<!doctype html><html><head><meta charset="utf-8"></head><body>` +
		`<script>location.replace(` + literal + `);</script></body></html>`
}

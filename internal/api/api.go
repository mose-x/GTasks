// Package api exposes the Go backend to the frontend as a small JSON HTTP API
// served by the Wails asset server.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gtasks/internal/batchexec"
	"gtasks/internal/bridge"
	"gtasks/internal/i18n"
	"gtasks/internal/session"
	"gtasks/internal/tasks"
	"gtasks/internal/theme"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// probeTimeout bounds the live session probe triggered by the UI.
const probeTimeout = 8 * time.Second

// Entry is one captured batchexecute request/response pair.
type Entry struct {
	Seq uint64          `json:"seq"`
	At  int64           `json:"at"`
	Raw json.RawMessage `json:"raw"`
}

// Recorder keeps a bounded ring of captures in Go so that page navigations do
// not discard what the sniffer already reported. When persistence is enabled
// every capture is additionally appended to a JSON Lines file, so discovery
// work survives restarts.
type Recorder struct {
	mu      sync.Mutex
	seq     uint64
	dropped uint64
	entries []Entry
	limit   int
	path    string
	file    *os.File
}

// NewRecorder returns a recorder holding at most limit entries.
func NewRecorder(limit int) *Recorder {
	if limit <= 0 {
		limit = 1000
	}
	return &Recorder{limit: limit}
}

// Add stores a raw capture. It satisfies bridge.SniffHandler.
func (r *Recorder) Add(raw json.RawMessage) {
	entry := Entry{At: time.Now().UnixMilli(), Raw: raw}

	r.mu.Lock()
	r.seq++
	entry.Seq = r.seq
	r.entries = append(r.entries, entry)
	if overflow := len(r.entries) - r.limit; overflow > 0 {
		r.entries = r.entries[overflow:]
		r.dropped += uint64(overflow)
	}
	file := r.file
	r.mu.Unlock()

	if file == nil {
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	// Best effort: losing a line must never break the live capture path.
	_, _ = file.Write(append(line, '\n'))
}

// Since returns entries with a sequence number greater than cursor.
func (r *Recorder) Since(cursor uint64) ([]Entry, uint64, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		if entry.Seq > cursor {
			out = append(out, entry)
		}
	}
	return out, r.seq, r.dropped
}

// Clear drops every capture and restarts numbering.
func (r *Recorder) Clear() {
	r.mu.Lock()
	r.entries = nil
	r.seq = 0
	r.dropped = 0
	r.mu.Unlock()
}

// SetPersistence appends future captures to path. The log is never truncated by
// Clear so that earlier discoveries are kept.
func (r *Recorder) SetPersistence(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	if path == "" {
		r.path = ""
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("api: 无法打开抓包日志 %s: %w", path, err)
	}
	r.file = file
	r.path = path
	return nil
}

// LoadHistory reads a JSON Lines capture log back into the ring so that earlier
// discoveries survive a restart. Only the most recent limit entries are kept and
// the rest are counted as dropped; malformed or payload-less lines are skipped.
// Every loaded entry is renumbered into one contiguous run, because sequence
// numbers restart from zero in each session and the workbench polls by cursor.
// A missing file is not an error: the first run has no history yet.
func (r *Recorder) LoadHistory(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("api: 无法读取抓包日志 %s: %w", path, err)
	}

	loaded := make([]Entry, 0, 128)
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil || len(entry.Raw) == 0 {
			continue
		}
		if entry.At == 0 {
			entry.At = time.Now().UnixMilli()
		}
		loaded = append(loaded, entry)
	}
	if len(loaded) == 0 {
		return 0, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if overflow := len(loaded) - r.limit; overflow > 0 {
		r.dropped += uint64(overflow)
		loaded = loaded[overflow:]
	}
	for index := range loaded {
		r.seq++
		loaded[index].Seq = r.seq
	}
	r.entries = append(r.entries, loaded...)
	return len(loaded), nil
}

// PersistPath reports the capture log location, or "" when disabled.
func (r *Recorder) PersistPath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path
}

// Close releases the capture log handle.
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
}

// Seq reports the sequence number of the most recent capture.
func (r *Recorder) Seq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// All returns every capture currently held in the ring.
func (r *Recorder) All() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Handler routes /api requests to the bridge, the batchexecute client, the
// operation registry and the session watcher.
type Handler struct {
	bridge   *bridge.Bridge
	client   *batchexec.Client
	rec      *Recorder
	registry *tasks.Registry
	service  *tasks.Service
	watcher  *session.Watcher
	theme    *theme.Manager
	i18n     *i18n.Manager
	dataDir  string

	mu         sync.RWMutex
	mainWin    *application.WebviewWindow
	sessionWin *application.WebviewWindow
	themeMenu  ThemeMenuOpener
	langMenu   LangMenuOpener
	winLookup  WindowLookup
}

// WindowLookup 按名字取窗口（工作台是懒创建的，API 侧拿不到直接引用）。
type WindowLookup func(name string) *application.WebviewWindow

// ThemeMenuOpener 在调用方指定的 webview 坐标处弹出主题单选菜单。由 main 注入，
// 这样前端按钮点下去弹的是原生菜单，和会话页顶栏那颗按钮行为一致。
type ThemeMenuOpener func(source string, x, y int)

// LangMenuOpener 在调用方指定的 webview 坐标处弹出语言单选菜单，菜单项由打包
// 进来的 locales/*.toml 决定。source 是发起请求的窗口（"main" / "workbench"）：
// 坐标是那个窗口自己 webview 里的，菜单必须弹回同一个窗口才对得上。
type LangMenuOpener func(source string, x, y int)

// menuSource 从 Referer 兜底判断请求来自哪个窗口（前端会在请求体里带 source，
// 这里只兜住老前端 / 手工调用）。
func menuSource(r *http.Request) string {
	if strings.Contains(r.Referer(), "workbench.html") {
		return "workbench"
	}
	return "main"
}

func resolveMenuSource(bodySource string, r *http.Request) string {
	if bodySource == "main" || bodySource == "workbench" {
		return bodySource
	}
	return menuSource(r)
}

// Deps bundles the Handler's collaborators.
type Deps struct {
	Bridge   *bridge.Bridge
	Client   *batchexec.Client
	Recorder *Recorder
	Registry *tasks.Registry
	Service  *tasks.Service
	Watcher  *session.Watcher
	Theme    *theme.Manager
	I18n     *i18n.Manager
	DataDir  string
}

// NewHandler wires the HTTP API to its collaborators.
func NewHandler(deps Deps) *Handler {
	return &Handler{
		bridge:   deps.Bridge,
		client:   deps.Client,
		rec:      deps.Recorder,
		registry: deps.Registry,
		service:  deps.Service,
		watcher:  deps.Watcher,
		theme:    deps.Theme,
		i18n:     deps.I18n,
		dataDir:  deps.DataDir,
	}
}

// BindWindows records the windows the API is allowed to control.
func (h *Handler) BindWindows(mainWin, sessionWin *application.WebviewWindow) {
	h.mu.Lock()
	h.mainWin = mainWin
	h.sessionWin = sessionWin
	h.mu.Unlock()
}

// SetThemeMenuOpener 注入主题菜单的弹出实现。
func (h *Handler) SetThemeMenuOpener(opener ThemeMenuOpener) {
	h.mu.Lock()
	h.themeMenu = opener
	h.mu.Unlock()
}

// SetWindowLookup 注入按名字取窗口的实现，供 /api/eval 定向到主窗口 / 工作台。
func (h *Handler) SetWindowLookup(lookup WindowLookup) {
	h.mu.Lock()
	h.winLookup = lookup
	h.mu.Unlock()
}

// SetLangMenuOpener 注入语言菜单的弹出实现。
func (h *Handler) SetLangMenuOpener(opener LangMenuOpener) {
	h.mu.Lock()
	h.langMenu = opener
	h.mu.Unlock()
}

// ServeHTTP implements http.Handler for the /api subtree.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/api/state" && r.Method == http.MethodGet:
		h.handleState(w, r)
	case path == "/api/probe" && r.Method == http.MethodPost:
		h.handleProbe(w, r)
	case path == "/api/session" && r.Method == http.MethodPost:
		h.handleSession(w, r)
	case path == "/api/sniff" && r.Method == http.MethodGet:
		h.handleSniffGet(w, r)
	case path == "/api/sniff" && r.Method == http.MethodPost:
		h.handleSniffPost(w, r)
	case path == "/api/sniff/clear" && r.Method == http.MethodPost:
		h.rec.Clear()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case path == "/api/sniff/export" && r.Method == http.MethodGet:
		h.handleSniffExport(w, r)
	case path == "/api/codegen" && r.Method == http.MethodPost:
		h.handleCodegen(w, r)
	case path == "/api/ops" && r.Method == http.MethodGet:
		h.handleOpsList(w, r)
	case path == "/api/ops" && r.Method == http.MethodPost:
		h.handleOpsAssign(w, r)
	case path == "/api/ops/execute" && r.Method == http.MethodPost:
		h.handleOpsExecute(w, r)
	case path == "/api/config" && r.Method == http.MethodGet:
		h.handleConfig(w, r)
	case path == "/api/session/auto" && r.Method == http.MethodPost:
		h.handleSessionAuto(w, r)
	case path == "/api/theme":
		h.handleTheme(w, r)
	case path == "/api/theme/menu" && r.Method == http.MethodPost:
		h.handleThemeMenu(w, r)
	case path == "/api/i18n" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		h.handleI18n(w, r)
	case path == "/api/i18n/menu" && r.Method == http.MethodPost:
		h.handleI18nMenu(w, r)
	case path == "/api/eval" && r.Method == http.MethodPost:
		h.handleEval(w, r)
	case path == "/api/initdata" && r.Method == http.MethodGet:
		h.handleInitData(w, r)
	case path == "/api/call" && r.Method == http.MethodPost:
		h.handleCall(w, r)
	case path == "/api/decode" && r.Method == http.MethodPost:
		h.handleDecode(w, r)
	case path == "/api/tasks" && r.Method == http.MethodGet:
		h.handleTasks(w, r)
	default:
		writeError(w, http.StatusNotFound, "未知接口: "+r.Method+" "+path)
	}
}

type stateResponse struct {
	Ready         bool               `json:"ready"`
	Session       *batchexec.Session `json:"session,omitempty"`
	Sniffed       uint64             `json:"sniffed"`
	Dropped       uint64             `json:"dropped"`
	SessionWindow bool               `json:"sessionWindowVisible"`
	Watcher       *session.Status    `json:"watcher,omitempty"`
	AutoManage    bool               `json:"autoManage"`
	Sniffing      bool               `json:"sniffing"`
	OpsReady      int                `json:"opsReady"`
	OpsTotal      int                `json:"opsTotal"`
}

func (h *Handler) handleState(w http.ResponseWriter, _ *http.Request) {
	_, seq, dropped := h.rec.Since(^uint64(0))
	response := stateResponse{
		Ready:   h.bridge.Ready(),
		Sniffed: seq,
		Dropped: dropped,
	}
	if cached := h.bridge.Info(); len(cached) > 0 {
		var session batchexec.Session
		if err := json.Unmarshal(cached, &session); err == nil {
			response.Session = &session
		}
	}
	if sessionWin := h.sessionWindow(); sessionWin != nil {
		response.SessionWindow = sessionWin.IsVisible()
	}
	if h.watcher != nil {
		status := h.watcher.Status()
		response.Watcher = &status
		response.AutoManage = h.watcher.AutoManage()
		response.Sniffing = h.watcher.Sniffing()
	}
	if h.registry != nil {
		definitions := h.registry.All()
		response.OpsTotal = len(definitions)
		for _, definition := range definitions {
			if executable(definition) {
				response.OpsReady++
			}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) handleProbe(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	session, err := h.client.Probe(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action string `json:"action"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	sessionWin := h.sessionWindow()
	if sessionWin == nil {
		writeError(w, http.StatusServiceUnavailable, "会话窗口不存在")
		return
	}
	switch request.Action {
	case "show":
		sessionWin.Show()
	case "hide":
		sessionWin.Hide()
	case "focus":
		// 被其它窗口完全遮住时，WebKit 会把页面标成 hidden，Google 的前端会
		// 停止渲染并忽略交互；逆向时需要能把会话窗口真正拉到前台。
		sessionWin.Show()
		sessionWin.Center()
		sessionWin.Focus()
	case "float":
		sessionWin.Show()
		sessionWin.Center()
		sessionWin.SetAlwaysOnTop(true)
		sessionWin.Focus()
	case "sink":
		sessionWin.SetAlwaysOnTop(false)
	case "windows":
		list := make([]map[string]any, 0, 4)
		for _, win := range application.Get().Window.GetAll() {
			wv, ok := win.(*application.WebviewWindow)
			if !ok {
				continue
			}
			x, y := wv.Position()
			width, height := wv.Size()
			list = append(list, map[string]any{
				"name": wv.Name(), "visible": wv.IsVisible(), "minimised": wv.IsMinimised(),
				"x": x, "y": y, "width": width, "height": height,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "windows": list})
		return
	case "info":
		x, y := sessionWin.Position()
		width, height := sessionWin.Size()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "action": request.Action, "visible": sessionWin.IsVisible(),
			"minimised": sessionWin.IsMinimised(), "x": x, "y": y,
			"width": width, "height": height,
		})
		return
	case "reload":
		sessionWin.Reload()
	case "navigate":
		if request.URL == "" {
			writeError(w, http.StatusBadRequest, "navigate 需要 url")
			return
		}
		sessionWin.SetURL(request.URL)
	default:
		writeError(w, http.StatusBadRequest, "未知 action: "+request.Action)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": request.Action})
}

func (h *Handler) handleSniffGet(w http.ResponseWriter, r *http.Request) {
	cursor, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	entries, seq, dropped := h.rec.Since(cursor)
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"seq":     seq,
		"dropped": dropped,
	})
}

func (h *Handler) handleSniffPost(w http.ResponseWriter, r *http.Request) {
	var request struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	var control *batchexec.SniffControl
	var err error
	if request.On {
		control, err = h.client.SniffStart(ctx)
	} else {
		control, err = h.client.SniffStop(ctx)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	writeJSON(w, http.StatusOK, control)
}

// handleEval 在会话页面里执行一段脚本，供逆向时查看全局对象或驱动只读交互。
func (h *Handler) handleEval(w http.ResponseWriter, r *http.Request) {
	var request struct {
		JS     string `json:"js"`
		Window string `json:"window"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(request.JS) == "" {
		writeError(w, http.StatusBadRequest, "js 不能为空")
		return
	}
	// 主窗口 / 工作台没有桥接通道，只能 fire-and-forget 地 ExecJS；调试脚本自己
	// 把结果写回（比如改 document.title 或 fetch 回来）。
	h.mu.RLock()
	lookup := h.winLookup
	h.mu.RUnlock()
	var target *application.WebviewWindow
	switch request.Window {
	case "main":
		h.mu.RLock()
		target = h.mainWin
		h.mu.RUnlock()
	case "workbench":
		if lookup != nil {
			target = lookup("workbench")
		}
	case "", "session":
	default:
		writeError(w, http.StatusBadRequest, "window 只能是 session / main / workbench")
		return
	}
	if target != nil {
		target.ExecJS(request.JS)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "window": request.Window})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	result, err := h.client.Eval(ctx, request.JS)
	if err != nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleInitData 暴露页面首屏内联数据：不带 key 时列清单，带 key 时返回该份数据。
func (h *Handler) handleInitData(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	key := r.URL.Query().Get("key")
	if key == "" {
		keys, err := h.client.InitDataKeys(ctx)
		if err != nil {
			writeError(w, http.StatusBadGateway, describe(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
		return
	}

	payload, err := h.client.InitData(ctx, key)
	if err != nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	if !payload.Found {
		writeError(w, http.StatusNotFound, "页面里没有这份初始化数据: "+key)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) handleCall(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RpcIDs  []string          `json:"rpcids"`
		Args    []any             `json:"args"`
		Options batchexec.Options `json:"opts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(request.RpcIDs) == 0 {
		writeError(w, http.StatusBadRequest, "rpcids 不能为空")
		return
	}

	calls := make([]batchexec.Call, 0, len(request.RpcIDs))
	for index, rpcID := range request.RpcIDs {
		var args any
		if index < len(request.Args) {
			args = request.Args[index]
		}
		calls = append(calls, batchexec.Call{RpcID: rpcID, Args: args})
	}

	ctx, cancel := context.WithTimeout(context.Background(), bridge.DefaultTimeout)
	defer cancel()
	result, err := h.client.Batch(ctx, calls, request.Options)
	if err != nil && result == nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	payload := map[string]any{"result": result}
	if err != nil {
		payload["decodeError"] = describe(err)
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleTasks returns the whole dataset parsed into the shape the main window
// renders: lists in sidebar order, tasks in display order. The web app draws
// every one of its views from this single payload, so the main window never
// needs to call anything else to stay in sync with it.
func (h *Handler) handleTasks(w http.ResponseWriter, _ *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "任务服务未启用")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), bridge.DefaultTimeout)
	defer cancel()

	snapshot, err := h.service.Snapshot(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": snapshot,
		"email":    h.accountEmail(),
	})
}

// accountEmail reads the signed-in address out of the page's own globals for the
// header label. The key Google publishes it under is not stable across builds,
// so the values are scanned instead of looked up; a miss is not an error.
func (h *Handler) accountEmail() string {
	cached := h.bridge.Info()
	if len(cached) == 0 {
		return ""
	}
	var page struct {
		WizScalars map[string]any `json:"wizScalars"`
	}
	if err := json.Unmarshal(cached, &page); err != nil {
		return ""
	}
	keys := make([]string, 0, len(page.WizScalars))
	for key := range page.WizScalars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if text, ok := page.WizScalars[key].(string); ok && looksLikeEmail(text) {
			return text
		}
	}
	return ""
}

// looksLikeEmail is deliberately conservative. Several of the opaque blobs the
// page publishes contain an @ as part of their own serialisation, so a value
// only counts as an address when every character is one an address can have.
func looksLikeEmail(text string) bool {
	at := strings.IndexByte(text, '@')
	if at <= 0 || at == len(text)-1 || strings.Count(text, "@") != 1 {
		return false
	}
	domain := text[at+1:]
	if !strings.Contains(domain, ".") {
		return false
	}
	for _, character := range text {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '@' || character == '.' || character == '_' ||
				character == '-' || character == '+':
		default:
			return false
		}
	}
	return true
}

func (h *Handler) handleDecode(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Raw string `json:"raw"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	responses, err := batchexec.Decode(request.Raw)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, describe(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"responses": responses})
}

func (h *Handler) sessionWindow() *application.WebviewWindow {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessionWin
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func describe(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "调用超时：" + err.Error()
	}
	return fmt.Sprintf("%v", err)
}

func (h *Handler) handleSniffExport(w http.ResponseWriter, _ *http.Request) {
	entries := h.rec.All()
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gtasks-captures.jsonl"`)
	encoder := json.NewEncoder(w)
	for _, entry := range entries {
		_ = encoder.Encode(entry)
	}
}

func (h *Handler) handleCodegen(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Entry json.RawMessage `json:"entry"`
		Seq   uint64          `json:"seq"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	entry := request.Entry
	if len(entry) == 0 && request.Seq > 0 {
		for _, candidate := range h.rec.All() {
			if candidate.Seq == request.Seq {
				entry = candidate.Raw
				break
			}
		}
	}
	if len(entry) == 0 {
		writeError(w, http.StatusBadRequest, "需要提供 entry 或有效的 seq")
		return
	}
	result, err := GenerateSnippet(entry)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, describe(err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleOpsList(w http.ResponseWriter, _ *http.Request) {
	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "操作表未启用")
		return
	}
	definitions := h.registry.All()
	ready := 0
	for _, definition := range definitions {
		if executable(definition) {
			ready++
		}
	}

	// 同步通道实现的操作没有 steps，但一样能跑；待发现列表里要把它们摘掉，
	// 否则工作台会一直提示去抓包。
	syncOps := tasks.SyncOps()
	isSync := make(map[tasks.Operation]bool, len(syncOps))
	syncNotes := make(map[string]string, len(syncOps))
	for _, op := range syncOps {
		isSync[op] = true
		syncNotes[string(op)] = tasks.SyncOpNotes(op)
	}
	pending := make([]tasks.Operation, 0, len(definitions))
	for _, op := range h.registry.Pending() {
		if !isSync[op] {
			pending = append(pending, op)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":      h.registry.Path(),
		"ready":     ready,
		"total":     len(definitions),
		"pending":   pending,
		"operation": definitions,
		"sync":      syncOps,
		"syncNotes": syncNotes,
	})
}

// executable reports whether an operation can actually run: either the registry
// carries captured rpcid steps for it, or the Go side synthesises its sync
// payload directly.
func executable(definition tasks.Definition) bool {
	return definition.Ready() || tasks.IsSyncOp(definition.Op)
}

func (h *Handler) handleOpsAssign(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		writeError(w, http.StatusServiceUnavailable, "操作表未启用")
		return
	}
	var request struct {
		Op    string       `json:"op"`
		Steps []tasks.Step `json:"steps"`
		Notes string       `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := h.registry.Assign(tasks.Operation(request.Op), request.Steps, request.Notes); err != nil {
		writeError(w, http.StatusBadRequest, describe(err))
		return
	}
	definition, _ := h.registry.Lookup(tasks.Operation(request.Op))
	writeJSON(w, http.StatusOK, definition)
}

func (h *Handler) handleOpsExecute(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "任务服务未启用")
		return
	}
	var request struct {
		Op     string         `json:"op"`
		Params map[string]any `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), bridge.DefaultTimeout)
	defer cancel()

	result, err := h.service.Execute(ctx, tasks.Operation(request.Op), request.Params)
	if err != nil && result == nil {
		writeError(w, http.StatusBadGateway, describe(err))
		return
	}
	payload := map[string]any{"result": result}
	if err != nil {
		payload["error"] = describe(err)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"dataDir":     h.dataDir,
		"captures":    h.rec.PersistPath(),
		"captureSeq":  h.rec.Seq(),
		"operations":  h.registry.Path(),
		"batchPath":   batchexec.DefaultPath,
		"autoManage":  h.watcher != nil && h.watcher.AutoManage(),
		"autoSniff":   h.watcher != nil && h.watcher.AutoSniff(),
		"pollSeconds": int(session.DefaultInterval.Seconds()),
	})
}

// handleTheme 读写全局外观模式。POST 只接受 mode 字段，切换的实际动作
// （原生外观 + 各窗口 CSS）由 main 里注册的 theme.OnChange 回调完成。
func (h *Handler) handleTheme(w http.ResponseWriter, r *http.Request) {
	if h.theme == nil {
		writeError(w, http.StatusServiceUnavailable, "主题管理未启用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"mode": string(h.theme.Mode())})
	case http.MethodPost:
		var request struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		mode := theme.Parse(request.Mode)
		if string(mode) != strings.TrimSpace(request.Mode) {
			writeError(w, http.StatusBadRequest, "mode 只能是 light / dark / system")
			return
		}
		h.theme.Set(mode)
		writeJSON(w, http.StatusOK, map[string]any{"mode": string(h.theme.Mode())})
	default:
		writeError(w, http.StatusMethodNotAllowed, "该接口只支持 GET / POST")
	}
}

// handleThemeMenu 在主窗口弹出主题单选菜单。坐标由前端按钮的位置算出，
// 传的是 webview 内的 CSS 像素。
func (h *Handler) handleThemeMenu(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	opener := h.themeMenu
	h.mu.RUnlock()
	if opener == nil {
		writeError(w, http.StatusServiceUnavailable, "主题菜单未启用")
		return
	}
	var request struct {
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	opener(resolveMenuSource(request.Source, r), request.X, request.Y)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleI18n(w http.ResponseWriter, r *http.Request) {
	if h.i18n == nil {
		writeError(w, http.StatusServiceUnavailable, "i18n 未启用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(h.i18n.Payload())
	case http.MethodPost:
		var request struct {
			Lang string `json:"lang"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		code := strings.TrimSpace(request.Lang)
		if !h.i18n.Bundle().Has(code) {
			writeError(w, http.StatusBadRequest, "不支持的语言: "+code)
			return
		}
		h.i18n.Set(code)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lang": h.i18n.Lang()})
	}
}

func (h *Handler) handleI18nMenu(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	opener := h.langMenu
	h.mu.RUnlock()
	if opener == nil {
		writeError(w, http.StatusServiceUnavailable, "语言菜单未启用")
		return
	}
	var request struct {
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	opener(resolveMenuSource(request.Source, r), request.X, request.Y)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleSessionAuto(w http.ResponseWriter, r *http.Request) {
	if h.watcher == nil {
		writeError(w, http.StatusServiceUnavailable, "会话监视器未启用")
		return
	}
	var request struct {
		AutoManage *bool `json:"autoManage"`
		AutoSniff  *bool `json:"autoSniff"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if request.AutoManage != nil {
		h.watcher.SetAutoManage(*request.AutoManage)
	}
	if request.AutoSniff != nil {
		h.watcher.SetAutoSniff(*request.AutoSniff)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"autoManage": h.watcher.AutoManage(),
		"autoSniff":  h.watcher.AutoSniff(),
	})
}

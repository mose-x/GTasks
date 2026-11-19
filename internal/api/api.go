// Package api exposes the Go backend to the frontend as a small JSON HTTP API
// served by the Wails asset server.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"gtasks/internal/batchexec"
	"gtasks/internal/bridge"
	"gtasks/internal/session"
	"gtasks/internal/tasks"

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
	dataDir  string

	mu         sync.RWMutex
	mainWin    *application.WebviewWindow
	sessionWin *application.WebviewWindow
}

// Deps bundles the Handler's collaborators.
type Deps struct {
	Bridge   *bridge.Bridge
	Client   *batchexec.Client
	Recorder *Recorder
	Registry *tasks.Registry
	Service  *tasks.Service
	Watcher  *session.Watcher
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
	case path == "/api/call" && r.Method == http.MethodPost:
		h.handleCall(w, r)
	case path == "/api/decode" && r.Method == http.MethodPost:
		h.handleDecode(w, r)
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
			if definition.Ready() {
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
		if definition.Ready() {
			ready++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      h.registry.Path(),
		"ready":     ready,
		"total":     len(definitions),
		"pending":   h.registry.Pending(),
		"operation": definitions,
	})
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
		"operations":  h.registry.Path(),
		"batchPath":   batchexec.DefaultPath,
		"autoManage":  h.watcher != nil && h.watcher.AutoManage(),
		"autoSniff":   h.watcher != nil && h.watcher.AutoSniff(),
		"pollSeconds": int(session.DefaultInterval.Seconds()),
	})
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

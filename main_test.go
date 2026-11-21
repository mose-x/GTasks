package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gtasks/internal/api"
	"gtasks/internal/batchexec"
	"gtasks/internal/bridge"
	"gtasks/internal/tasks"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	sessionBridge := bridge.New()
	recorder := api.NewRecorder(10)
	sessionBridge.SetSniffHandler(recorder.Add)
	client := batchexec.NewClient(sessionBridge)

	registry, err := tasks.Default()
	if err != nil {
		t.Fatalf("加载内置操作表: %v", err)
	}
	return buildAssetHandler(api.NewHandler(api.Deps{
		Bridge:   sessionBridge,
		Client:   client,
		Recorder: recorder,
		Registry: registry,
		Service:  tasks.NewService(registry, client),
		DataDir:  t.TempDir(),
	}))
}

func TestAssetHandlerServesFrontend(t *testing.T) {
	handler := newTestHandler(t)

	// 克隆界面退役后，内嵌资源只剩逆向工作台与控制台 API 用的共享脚本。
	for _, path := range []string{"/workbench.html", "/workbench.js", "/workbench.css", "/shared.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, response.Code)
		}
		if response.Body.Len() == 0 {
			t.Fatalf("%s: empty body", path)
		}
	}

	// 根路径不再有克隆界面：FileServer 会把 /index.html 规整到 /，而 / 现在
	// 只是工作台的目录列表，不再返回克隆界面的 HTML。
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("/: expected 200, got %d", response.Code)
	}
	if body := response.Body.String(); strings.Contains(body, "id=\"app\"") || strings.Contains(body, "class=\"app\"") {
		t.Fatalf("/ 不该再返回克隆界面: %q", body[:min(len(body), 120)])
	}
}

func TestAssetHandlerRoutesAPI(t *testing.T) {
	handler := newTestHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}

	var state struct {
		Ready    bool   `json:"ready"`
		Sniffed  uint64 `json:"sniffed"`
		OpsTotal int    `json:"opsTotal"`
		OpsReady int    `json:"opsReady"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if state.Ready {
		t.Fatal("an unbound bridge must not report ready")
	}
	if state.OpsTotal == 0 {
		t.Fatal("state should report the bundled operation catalogue")
	}
	// The read path ships discovered, and so does every operation the sync
	// channel implements in Go; the rest still has to be captured per account.
	if want := 1 + len(tasks.SyncOps()); state.OpsReady != want {
		t.Fatalf("expected %d executable operations (snapshot + sync channel), got %d", want, state.OpsReady)
	}
}

func TestAssetHandlerListsOperations(t *testing.T) {
	handler := newTestHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/ops", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}

	var payload struct {
		Total   int      `json:"total"`
		Ready   int      `json:"ready"`
		Pending []string `json:"pending"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("ops payload is not JSON: %v", err)
	}
	ready := 1 + len(tasks.SyncOps())
	if payload.Ready != ready || payload.Total != len(payload.Pending)+ready {
		t.Fatalf("expected %d executable operations and the rest pending, got %+v", ready, payload)
	}
	for _, op := range payload.Pending {
		if op == string(tasks.OpSnapshot) {
			t.Fatalf("%s should not be pending", tasks.OpSnapshot)
		}
		// 同步通道已经实现的操作不该再出现在待发现列表里，否则工作台会一直
		// 提示去抓包。
		if tasks.IsSyncOp(tasks.Operation(op)) {
			t.Fatalf("%s is implemented over the sync channel and must not be pending", op)
		}
	}
}

// Without a bound session window the read path cannot reach Google, and must
// say so instead of returning an empty task list the UI would render as
// "no tasks".
func TestAssetHandlerTasksNeedsASession(t *testing.T) {
	handler := newTestHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if response.Code == http.StatusOK {
		t.Fatalf("expected a failure without a bound session window, got 200: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "error") {
		t.Fatalf("expected a JSON error body, got %s", response.Body.String())
	}
}

func TestAssetHandlerRejectsUnknownAPIPath(t *testing.T) {
	handler := newTestHandler(t)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestSniffRecordingRoundTrip(t *testing.T) {
	recorder := api.NewRecorder(2)

	recorder.Add(json.RawMessage(`{"rpcids":["AbCdE"],"status":200}`))
	recorder.Add(json.RawMessage(`{"rpcids":["FgHiJ"],"status":200}`))
	recorder.Add(json.RawMessage(`{"rpcids":["KlMnO"],"status":200}`))

	entries, seq, dropped := recorder.Since(0)
	if seq != 3 {
		t.Fatalf("expected sequence 3, got %d", seq)
	}
	if dropped != 1 {
		t.Fatalf("expected 1 dropped entry, got %d", dropped)
	}
	if len(entries) != 2 {
		t.Fatalf("expected the ring to keep 2 entries, got %d", len(entries))
	}

	// Cursor based paging must not replay what the client already saw.
	tail, _, _ := recorder.Since(entries[len(entries)-1].Seq)
	if len(tail) != 0 {
		t.Fatalf("expected no new entries after the cursor, got %d", len(tail))
	}
}

func TestRecorderPersistenceAppendsJSONLines(t *testing.T) {
	recorder := api.NewRecorder(10)
	path := t.TempDir() + "/captures.jsonl"
	if err := recorder.SetPersistence(path); err != nil {
		t.Fatalf("SetPersistence: %v", err)
	}
	recorder.Add(json.RawMessage(`{"rpcids":["AbCdE"]}`))
	recorder.Add(json.RawMessage(`{"rpcids":["FgHiJ"]}`))
	recorder.Close()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 行，实到 %d: %q", len(lines), string(body))
	}
	var entry api.Entry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("日志行不是合法 JSON: %v", err)
	}
	if entry.Seq != 1 {
		t.Fatalf("期望 seq=1，实到 %d", entry.Seq)
	}
}

// 主窗口就是内嵌的 Google 页：语言没变时不该触发任何重载。
func TestLangNavigatorSkipsRedundantReload(t *testing.T) {
	nav := newLangNavigator("zh")
	if url := nav.request("zh"); url != "" {
		t.Fatalf("同一种语言不该重载，得到 %q", url)
	}
	if nav.isQuiet() {
		t.Fatal("没重载就不该让 watcher 安静")
	}
}

// 主窗口永远在屏幕上：选了新语言当场就载，并给 watcher 留一段安静期；
// 页面 announce 就绪后安静期收短。
func TestLangNavigatorReloadsImmediatelyAndQuietsWatcher(t *testing.T) {
	nav := newLangNavigator("zh")

	if url := nav.request("en"); url != tasksURLFor("en") {
		t.Fatalf("切语言应当场拿到重载地址，得到 %q", url)
	}
	if !nav.isQuiet() {
		t.Fatal("重载期间要让 watcher 安静，别把掉线误判成会话失效")
	}
	if url := nav.request("zh"); url != tasksURLFor("zh") {
		t.Fatalf("切回来也该重载，得到 %q", url)
	}

	nav.settled()
	if left := time.Until(nav.quietTil); left <= 0 || left > sessionReadyGrace {
		t.Fatalf("页面就绪后安静期该收短到 %v 以内，实际还剩 %v", sessionReadyGrace, left)
	}
}

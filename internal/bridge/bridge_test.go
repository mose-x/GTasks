package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestDispatchScriptRoundTripsPayload(t *testing.T) {
	script, err := dispatchScript(7, "batch", map[string]any{
		"rpcids": []string{"AbCdE"},
		"freqs":  []any{[]any{"id", nil, 2}},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	const prefix = "window.__gtask&&window.__gtask.dispatch("
	if !strings.HasPrefix(script, prefix) || !strings.HasSuffix(script, ");") {
		t.Fatalf("unexpected script shape: %s", script)
	}

	literal := strings.TrimSuffix(strings.TrimPrefix(script, prefix), ");")
	var encoded string
	if err := json.Unmarshal([]byte(literal), &encoded); err != nil {
		t.Fatalf("script argument is not a valid JSON string literal: %v", err)
	}

	var payload struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Args   struct {
			RpcIDs []string `json:"rpcids"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload.ID != 7 || payload.Method != "batch" || len(payload.Args.RpcIDs) != 1 {
		t.Fatalf("payload did not round trip: %+v", payload)
	}
}

func TestDispatchScriptEscapesLineSeparators(t *testing.T) {
	script, err := dispatchScript(1, "probe", "a\u2028b\u2029c")
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	// A raw U+2028/U+2029 terminates a JS string literal; they must be escaped.
	if strings.ContainsAny(script, "\u2028\u2029") {
		t.Fatalf("line separators were not escaped: %q", script)
	}
	if !strings.Contains(script, `\u2028`) || !strings.Contains(script, `\u2029`) {
		t.Fatalf("expected escaped line separators in %q", script)
	}
}

func TestHandleRawIgnoresForeignMessages(t *testing.T) {
	bridge := New()
	captured := make(chan json.RawMessage, 1)
	bridge.SetSniffHandler(func(entry json.RawMessage) { captured <- entry })

	// Wrong protocol prefix.
	bridge.HandleRaw(nil, `{"p":"other","kind":"sniff","entry":{}}`, nil)
	// Not JSON at all.
	bridge.HandleRaw(nil, "wails:drag", nil)

	select {
	case entry := <-captured:
		t.Fatalf("foreign message should not reach the sniff handler, got %s", entry)
	default:
	}
}

func TestFromMainFrameOnlyFiltersWhereReported(t *testing.T) {
	childFrame := &application.OriginInfo{Origin: "https://tasks.google.com", IsMainFrame: false}
	mainFrame := &application.OriginInfo{Origin: "https://tasks.google.com", IsMainFrame: true}

	previous := reportsMainFrame
	defer func() { reportsMainFrame = previous }()

	reportsMainFrame = true
	if fromMainFrame(childFrame) {
		t.Fatal("darwin 上应丢弃子框架消息")
	}
	if !fromMainFrame(mainFrame) {
		t.Fatal("darwin 上应接受主框架消息")
	}

	// Windows/WebKitGTK always report false, which must not drop anything.
	reportsMainFrame = false
	if !fromMainFrame(childFrame) {
		t.Fatal("未上报框架信息的后端不应据此过滤")
	}
	if !fromMainFrame(nil) {
		t.Fatal("缺少 origin 信息时应放行")
	}
}

// ExecJS is queued internally until the page reports readiness; calling before
// the first announce would grow that queue without bound.
func TestCallFailsFastBeforeThePageAnnounces(t *testing.T) {
	bridge := New()
	bridge.window = &application.WebviewWindow{}

	if _, err := bridge.Call(context.Background(), "probe", nil); !errors.Is(err, ErrNotReady) {
		t.Fatalf("未就绪时应立即返回 ErrNotReady，实到: %v", err)
	}
}

func TestCallReportsUnboundBridge(t *testing.T) {
	if _, err := New().Call(context.Background(), "probe", nil); !errors.Is(err, ErrNotBound) {
		t.Fatalf("未绑定窗口应返回 ErrNotBound，实到: %v", err)
	}
}

func TestRedirectHTMLQuotesTarget(t *testing.T) {
	html := RedirectHTML("https://tasks.google.com/")
	if !strings.HasPrefix(html, "<!doctype html>") {
		t.Fatalf("应是完整的 HTML 文档: %s", html)
	}
	if !strings.Contains(html, `location.replace("https://tasks.google.com/");`) {
		t.Fatalf("缺少重定向调用: %s", html)
	}
	if strings.Count(html, "</script>") != 1 {
		t.Fatalf("文档结构异常: %s", html)
	}
}

// The target ends up inside an inline script, so markup must be escaped.
func TestRedirectHTMLEscapesMarkup(t *testing.T) {
	html := RedirectHTML(`https://tasks.google.com/</script><script>alert(1)`)
	if strings.Contains(html, "</script><script>") {
		t.Fatalf("目标 URL 未转义，可提前结束 script: %s", html)
	}
}

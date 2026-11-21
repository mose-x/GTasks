package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"gtasks/internal/bridge"
)

func TestLooksLikeEmail(t *testing.T) {
	valid := []string{"someone@gmail.com", "first.last+tag@example.co.uk", "a_b-c@d.e"}
	for _, text := range valid {
		if !looksLikeEmail(text) {
			t.Errorf("looksLikeEmail(%q) = false, 期望 true", text)
		}
	}

	// The page publishes opaque blobs that contain an @ as part of their own
	// serialisation; none of them may be mistaken for the signed-in address.
	invalid := []string{
		"",
		"plainaddress",
		"@no-local-part.com",
		"no-domain@",
		"two@@example.com",
		"no-dot-in-domain@example",
		`%.@.[null,null,null,[3,1,[1794911143,712500000]],null,22]]`,
		"someone@example.com extra",
		"https://someone@example.com/",
	}
	for _, text := range invalid {
		if looksLikeEmail(text) {
			t.Errorf("looksLikeEmail(%q) = true, 期望 false", text)
		}
	}
}

// 会话页重载（切语言、主题兜底、掉线恢复）期间桥接够不着，主窗口不该因此闪
// 空列表：把上一次成功的快照垫回去，附带 stale 标记让前端静默补一次。
func TestStaleTasksFallback(t *testing.T) {
	handler := &Handler{}

	if handler.writeStaleTasks(httptest.NewRecorder()) {
		t.Fatal("没有存货时不该报告成功")
	}

	handler.tasksCache = map[string]any{"email": "someone@gmail.com"}
	recorder := httptest.NewRecorder()
	if !handler.writeStaleTasks(recorder) {
		t.Fatal("有存货时应该垫回去")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("垫回去的响应应该是 200，实际 %d", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["stale"] != true {
		t.Fatalf("响应应该带 stale 标记，实际 %v", body["stale"])
	}
	if body["email"] != "someone@gmail.com" {
		t.Fatalf("存货里的字段丢了: %v", body)
	}
	if _, polluted := handler.tasksCache["stale"]; polluted {
		t.Fatal("垫出去的副本不该污染存货")
	}
}

// 只有「会话页暂时够不着」这一类会自己好的故障才垫存货；真掉线要照常报错，
// 好让界面引导用户重新登录。
func TestTransientSessionError(t *testing.T) {
	transient := []error{
		bridge.ErrNotReady,
		bridge.ErrNoReply,
		bridge.ErrNotBound,
		fmt.Errorf("tasks: 执行 snapshot 失败: %w", bridge.ErrNotReady),
	}
	for _, err := range transient {
		if !transientSessionError(err) {
			t.Errorf("transientSessionError(%v) = false, 期望 true", err)
		}
	}

	permanent := []error{
		errors.New("tasks: 快照响应里没有可解析的数据（会话可能已失效）"),
		fmt.Errorf("%w: rpc 报错", bridge.ErrPageError),
	}
	for _, err := range permanent {
		if transientSessionError(err) {
			t.Errorf("transientSessionError(%v) = true, 期望 false", err)
		}
	}
}

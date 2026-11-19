package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "captures.jsonl")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写入抓包日志: %v", err)
	}
	return path
}

func captureLine(seq uint64, rpcid string) string {
	raw, err := json.Marshal(map[string]any{"url": "/_/TasksUi/data/batchexecute", "rpcids": []string{rpcid}})
	if err != nil {
		panic(err)
	}
	line, err := json.Marshal(Entry{Seq: seq, At: int64(seq) * 1000, Raw: raw})
	if err != nil {
		panic(err)
	}
	return string(line)
}

func TestRecorderLoadHistoryRestoresCaptures(t *testing.T) {
	recorder := NewRecorder(100)
	path := writeLog(t, captureLine(1, "aaa"), captureLine(2, "bbb"), captureLine(3, "ccc"))

	loaded, err := recorder.LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if loaded != 3 {
		t.Fatalf("应载入 3 条，实到 %d", loaded)
	}
	if got := recorder.Seq(); got != 3 {
		t.Fatalf("序号应接续到 3，实到 %d", got)
	}

	// A new capture must continue the sequence instead of restarting it.
	recorder.Add(json.RawMessage(`{"rpcids":["ddd"]}`))
	entries, seq, dropped := recorder.Since(3)
	if seq != 4 || dropped != 0 || len(entries) != 1 {
		t.Fatalf("增量拉取不正确: seq=%d dropped=%d entries=%d", seq, dropped, len(entries))
	}
	if all := recorder.All(); len(all) != 4 {
		t.Fatalf("环内应有 4 条，实到 %d", len(all))
	}
}

// Sequence numbers restart from zero every session, so the loader has to
// renumber; otherwise the workbench's cursor polling would skip or repeat rows.
func TestRecorderLoadHistoryRenumbersAcrossSessions(t *testing.T) {
	recorder := NewRecorder(100)
	path := writeLog(t, captureLine(7, "aaa"), captureLine(1, "bbb"), captureLine(2, "bbb"))

	if _, err := recorder.LoadHistory(path); err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	for index, entry := range recorder.All() {
		if entry.Seq != uint64(index+1) {
			t.Fatalf("第 %d 条序号应为 %d，实到 %d", index, index+1, entry.Seq)
		}
	}
}

func TestRecorderLoadHistoryKeepsMostRecentWithinLimit(t *testing.T) {
	recorder := NewRecorder(2)
	path := writeLog(t, captureLine(1, "a"), captureLine(2, "b"), captureLine(3, "c"), captureLine(4, "d"))

	loaded, err := recorder.LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if loaded != 2 {
		t.Fatalf("应只保留 2 条，实到 %d", loaded)
	}
	_, _, dropped := recorder.Since(0)
	if dropped != 2 {
		t.Fatalf("应记录 2 条被丢弃，实到 %d", dropped)
	}
	entries := recorder.All()
	if len(entries) != 2 || entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Fatalf("保留的应是最近两条且重新编号: %+v", entries)
	}
}

func TestRecorderLoadHistorySkipsMalformedLines(t *testing.T) {
	recorder := NewRecorder(10)
	path := writeLog(t,
		"这不是 JSON",
		"",
		`{"seq":1,"at":1}`, // 没有 raw，无法还原
		captureLine(2, "bbb"),
	)

	loaded, err := recorder.LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if loaded != 1 {
		t.Fatalf("应只载入 1 条有效记录，实到 %d", loaded)
	}
}

func TestRecorderLoadHistoryMissingFile(t *testing.T) {
	recorder := NewRecorder(10)
	loaded, err := recorder.LoadHistory(filepath.Join(t.TempDir(), "不存在.jsonl"))
	if err != nil || loaded != 0 {
		t.Fatalf("首次运行不应报错: loaded=%d err=%v", loaded, err)
	}
	if _, err := recorder.LoadHistory(""); err != nil || recorder.Seq() != 0 {
		t.Fatalf("空路径应是 no-op: %v", err)
	}
}

func TestRecorderPersistenceRoundTrip(t *testing.T) {
	recorder := NewRecorder(10)
	path := filepath.Join(t.TempDir(), "captures.jsonl")
	if err := recorder.SetPersistence(path); err != nil {
		t.Fatalf("SetPersistence: %v", err)
	}
	recorder.Add(json.RawMessage(`{"rpcids":["aaa"]}`))
	recorder.Add(json.RawMessage(`{"rpcids":["bbb"]}`))
	recorder.Close()

	reloaded := NewRecorder(10)
	loaded, err := reloaded.LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if loaded != 2 {
		t.Fatalf("应读回 2 条，实到 %d", loaded)
	}

	var rpcids []string
	for _, entry := range reloaded.All() {
		var payload struct {
			RpcIDs []string `json:"rpcids"`
		}
		if err := json.Unmarshal(entry.Raw, &payload); err != nil {
			t.Fatalf("解析抓包内容: %v", err)
		}
		rpcids = append(rpcids, payload.RpcIDs...)
	}
	if len(rpcids) != 2 || rpcids[0] != "aaa" || rpcids[1] != "bbb" {
		t.Fatalf("抓包内容顺序不正确: %v", rpcids)
	}
}

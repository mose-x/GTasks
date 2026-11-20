package tasks

import (
	"encoding/json"
	"strings"
	"testing"
)

// fixture mirrors the shape of a real snapshot payload with invented ids and
// titles, so the mapping can be pinned without shipping anyone's task list.
//
// It reproduces the awkward parts deliberately: entries arriving in an order
// that disagrees with the sidebar order, a list whose completed count exceeds
// the completed tasks actually returned, starred and unstarred tasks, a
// parent/child pair, and slots nobody has mapped yet.
const fixture = `[
  [[1795134816,64691000],null,null,[true,true],null,null,[null,["1000"]]],
  [
    [[null,null,[]]],
    [null,["list-b",null,"list-b,list-a",null,null,null,null,null,null,null,[null,12],null,null,null,null,null,null,null,null,null,null,null,null,true]],
    [null,null,null,["list-a",["甲",null,"user",null,2],[[["a2"],["a1"]]],null,null,[1586336399,290460000],[null,34],[1794812651,269935000],null,7]],
    [null,null,["a1",[null,"未完成任务"],["1794812600,0"],"list-a",null,null,null,[null,4],[],null,[1586336399,0],null,["1000"]]],
    [null,null,["a2",[true,"已完成任务",null,null,[1794812000,500000000],null,null,null,null,null,null,null,null,true,[1790000000,100000000]],[1794812651,0],"list-a",null,null,null,[null,8],[["a3"]],null,[1586336400,0],[],["1000"]]],
    [null,null,["a3",[null,"子任务"],["1794812700,0"],"list-a",null,null,null,[null,9],[],null,[1586336500,0],null,["1000"]]],
    [null,null,["a4",[true,"更早完成的",null,null,[1700000000,0]],[1794812800,0],"list-a",null,null,null,[null,3],[],null,[1586336600,0],null,["1000"]]],
    [null,null,null,["list-b",["乙",null,"user",null,1],[[["b1"]]],null,null,[1782697474,0],[null,200],[1794916603,0]]],
    [null,null,["b1",[null,"另一个清单的任务"],["1794916000,0"],"list-b",null,null,null,[null,5],[],null,[1782697500,0],null,["1000"]]]
  ]
]`

// fixtureUpdated replaces the "[sec,nanos]" pairs that the fixture writes as
// strings for readability with the arrays Google actually sends.
func loadFixture(t *testing.T) json.RawMessage {
	t.Helper()
	body := fixture
	for _, pair := range [][2]string{
		{`"1794812600,0"`, `[1794812600,0]`},
		{`"1794812700,0"`, `[1794812700,0]`},
		{`"1794916000,0"`, `[1794916000,0]`},
	} {
		body = strings.Replace(body, pair[0], pair[1], 1)
	}
	if !json.Valid([]byte(body)) {
		t.Fatalf("fixture 不是合法 JSON")
	}
	return json.RawMessage(body)
}

func parseFixture(t *testing.T) *Snapshot {
	t.Helper()
	snapshot, err := ParseSnapshot(loadFixture(t))
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	return snapshot
}

func TestParseSnapshotHeader(t *testing.T) {
	snapshot := parseFixture(t)
	if got := snapshot.ServerTime.Seconds; got != 1795134816 {
		t.Fatalf("ServerTime.Seconds = %d, 期望 1795134816", got)
	}
	if got := snapshot.ServerTime.Nanos; got != 64691000 {
		t.Fatalf("ServerTime.Nanos = %d, 期望 64691000", got)
	}
	if snapshot.Account != "1000" {
		t.Fatalf("Account = %q, 期望 %q", snapshot.Account, "1000")
	}
	if snapshot.ActiveListID != "list-b" {
		t.Fatalf("ActiveListID = %q, 期望 %q", snapshot.ActiveListID, "list-b")
	}
}

// The sidebar order comes from the order entry, not from the order the list
// entries happened to arrive in.
func TestParseSnapshotListOrder(t *testing.T) {
	snapshot := parseFixture(t)
	if got := strings.Join(snapshot.ListOrder, ","); got != "list-b,list-a" {
		t.Fatalf("ListOrder = %q, 期望 %q", got, "list-b,list-a")
	}
	if len(snapshot.Lists) != 2 {
		t.Fatalf("清单数 = %d, 期望 2", len(snapshot.Lists))
	}
	if snapshot.Lists[0].ID != "list-b" || snapshot.Lists[1].ID != "list-a" {
		t.Fatalf("清单未按侧栏顺序排列: %s, %s", snapshot.Lists[0].ID, snapshot.Lists[1].ID)
	}
	if snapshot.Lists[0].Index != 0 || snapshot.Lists[1].Index != 1 {
		t.Fatalf("清单 Index 未回填: %d, %d", snapshot.Lists[0].Index, snapshot.Lists[1].Index)
	}

	listA := snapshot.Lists[1]
	if listA.Title != "甲" || listA.Kind != "user" {
		t.Fatalf("清单标题/类型 = %q/%q, 期望 甲/user", listA.Title, listA.Kind)
	}
	if listA.Incomplete != 2 {
		t.Fatalf("list-a Incomplete = %d, 期望 2（服务端声明的未完成任务数）", listA.Incomplete)
	}
	if listA.CompletedTotal != 7 {
		t.Fatalf("list-a CompletedTotal = %d, 期望 7", listA.CompletedTotal)
	}
	if listA.Created.Seconds != 1586336399 || listA.Updated.Seconds != 1794812651 {
		t.Fatalf("list-a 时间戳解析错误: created=%d updated=%d", listA.Created.Seconds, listA.Updated.Seconds)
	}
	if got := strings.Join(listA.TaskIDs, ","); got != "a2,a1" {
		t.Fatalf("list-a TaskIDs = %q, 期望 %q", got, "a2,a1")
	}
}

// A list with no completed tasks omits slot 9 entirely rather than sending 0.
func TestParseSnapshotCompletedTotalDefaultsToZero(t *testing.T) {
	snapshot := parseFixture(t)
	if got := snapshot.Lists[0].CompletedTotal; got != 0 {
		t.Fatalf("list-b CompletedTotal = %d, 期望 0", got)
	}
}

func TestParseSnapshotTaskFields(t *testing.T) {
	snapshot := parseFixture(t)
	byID := make(map[string]Task, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		byID[task.ID] = task
	}
	if len(byID) != 5 {
		t.Fatalf("任务数 = %d, 期望 5", len(byID))
	}

	starred := byID["a2"]
	if starred.Title != "已完成任务" || starred.ListID != "list-a" {
		t.Fatalf("a2 标题/清单 = %q/%q", starred.Title, starred.ListID)
	}
	if !starred.Completed {
		t.Fatal("a2 应为已完成")
	}
	if starred.CompletedAt.Seconds != 1794812000 || starred.CompletedAt.Nanos != 500000000 {
		t.Fatalf("a2 CompletedAt = %+v, 期望 [1794812000,500000000]", starred.CompletedAt)
	}
	// Starred is the field the public Tasks API does not expose at all.
	if !starred.Starred {
		t.Fatal("a2 应为已加星标")
	}
	if starred.StarredAt.Seconds != 1790000000 {
		t.Fatalf("a2 StarredAt = %+v, 期望 1790000000 秒", starred.StarredAt)
	}
	if got := strings.Join(starred.Children, ","); got != "a3" {
		t.Fatalf("a2 Children = %q, 期望 %q", got, "a3")
	}
	if starred.Created.Seconds != 1586336400 || starred.Updated.Seconds != 1794812651 {
		t.Fatalf("a2 时间戳解析错误: created=%d updated=%d", starred.Created.Seconds, starred.Updated.Seconds)
	}

	plain := byID["a1"]
	if plain.Completed || plain.Starred {
		t.Fatalf("a1 不应是已完成或已加星标: %+v", plain)
	}
	if !plain.CompletedAt.IsZero() || !plain.StarredAt.IsZero() {
		t.Fatalf("a1 的可选时间戳应为零值: %+v", plain)
	}
	if plain.Notes != "" {
		t.Fatalf("a1 Notes = %q, 期望空", plain.Notes)
	}
	if byID["b1"].Notes != "" || byID["b1"].ListID != "list-b" {
		t.Fatalf("b1 解析错误: %+v", byID["b1"])
	}
}

// Parent is derived from the parent's child list, so it survives whatever slot
// Google decides to put the reference in.
func TestParseSnapshotLinksParents(t *testing.T) {
	snapshot := parseFixture(t)
	for _, task := range snapshot.Tasks {
		if task.ID == "a3" {
			if task.Parent != "a2" {
				t.Fatalf("a3.Parent = %q, 期望 %q", task.Parent, "a2")
			}
			return
		}
	}
	t.Fatal("快照里没有 a3")
}

// Display order is the server-declared incomplete order first, then completed
// tasks newest first — the order the web app renders.
func TestParseSnapshotTaskOrder(t *testing.T) {
	snapshot := parseFixture(t)
	var got []string
	for _, task := range snapshot.Tasks {
		if task.ListID == "list-a" {
			got = append(got, task.ID)
		}
	}
	// a2 and a1 are the declared order for list-a; a2 is completed but still
	// comes first because the declaration wins. a3 is a child not present in
	// the declaration, then a4, oldest completion last.
	want := []string{"a2", "a1", "a3", "a4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("list-a 顺序 = %v, 期望 %v", got, want)
	}
	if snapshot.Tasks[0].ListID != "list-b" {
		t.Fatalf("清单应按侧栏顺序分组，首个任务属于 %q, 期望 list-b", snapshot.Tasks[0].ListID)
	}
}

// Unmapped slots must survive verbatim, otherwise a Google-side layout change
// shows up as silently missing data instead of as something inspectable.
func TestParseSnapshotKeepsUnknownSlots(t *testing.T) {
	snapshot := parseFixture(t)
	listA := snapshot.Lists[1]
	if raw, ok := listA.Extra[6]; !ok || string(raw) != "[null,34]" {
		t.Fatalf("list-a Extra[6] = %s (存在=%v), 期望 [null,34]", raw, ok)
	}
	if raw, ok := listA.MetaExtra[4]; !ok || string(raw) != "2" {
		t.Fatalf("list-a MetaExtra[4] = %s (存在=%v), 期望 2", raw, ok)
	}
	for _, task := range snapshot.Tasks {
		if task.ID != "a1" {
			continue
		}
		if raw, ok := task.Extra[7]; !ok || string(raw) != "[null,4]" {
			t.Fatalf("a1 Extra[7] = %s (存在=%v), 期望 [null,4]", raw, ok)
		}
		return
	}
	t.Fatal("快照里没有 a1")
}

// A starred task with no unknown slots must not grow an empty bag, which would
// show up as noise in every API response.
func TestParseSnapshotOmitsEmptySlotBags(t *testing.T) {
	body, err := json.Marshal(parseFixture(t))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(body), `"extra":{}`) || strings.Contains(string(body), `"detailExtra":{}`) {
		t.Fatalf("空的 SlotBag 不应出现在 JSON 里: %s", body)
	}
}

func TestTimestampMarshalsAsRFC3339OrNull(t *testing.T) {
	snapshot := parseFixture(t)
	byID := make(map[string]Task, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		byID[task.ID] = task
	}

	encoded, err := json.Marshal(byID["a2"].CompletedAt)
	if err != nil {
		t.Fatalf("Marshal CompletedAt: %v", err)
	}
	if got := string(encoded); !strings.HasPrefix(got, `"`) || !strings.Contains(got, "T") {
		t.Fatalf("CompletedAt JSON = %s, 期望 RFC 3339 字符串", got)
	}

	encoded, err = json.Marshal(byID["a1"].CompletedAt)
	if err != nil {
		t.Fatalf("Marshal 零值 CompletedAt: %v", err)
	}
	if got := string(encoded); got != "null" {
		t.Fatalf("零值 CompletedAt JSON = %s, 期望 null", got)
	}
}

func TestSnapshotViews(t *testing.T) {
	snapshot := parseFixture(t)
	if got := len(snapshot.IncompleteTasks("list-a")); got != 2 {
		t.Fatalf("list-a 未完成任务 = %d, 期望 2", got)
	}
	if got := len(snapshot.IncompleteTasks("")); got != 3 {
		t.Fatalf("全部未完成任务 = %d, 期望 3", got)
	}
	starred := snapshot.StarredTasks()
	if len(starred) != 0 {
		t.Fatalf("已加星标的未完成任务 = %d, 期望 0（a2 已完成）", len(starred))
	}
	if _, ok := snapshot.List("list-b"); !ok {
		t.Fatal("List(list-b) 未找到")
	}
	if _, ok := snapshot.List("nope"); ok {
		t.Fatal("List(nope) 不应存在")
	}
}

func TestParseSnapshotRejectsMalformed(t *testing.T) {
	for _, body := range []string{`{}`, `[]`, `[[]]`, `[[[]],"x"`, `[[[],[]],[[null,null,["",[]]]]]`} {
		if _, err := ParseSnapshot(json.RawMessage(body)); err == nil {
			t.Fatalf("ParseSnapshot(%s) 应报错", body)
		}
	}
}

package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discovered lists the operations the bundled catalogue already carries an
// rpcid for. Everything else must still be pending, so a build that silently
// loses a mapping fails here rather than in the field.
var discovered = map[Operation]string{
	OpSnapshot: "Yz8iP",
}

func TestDefaultCatalogueCarriesOnlyDiscoveredSteps(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	all := registry.All()
	if len(all) < 20 {
		t.Fatalf("内置操作表应覆盖 20+ 个操作，实到 %d", len(all))
	}
	for _, definition := range all {
		if definition.Label == "" || definition.Notes == "" {
			t.Fatalf("操作 %s 缺少 label 或 notes", definition.Op)
		}
		rpcID, shouldBeReady := discovered[definition.Op]
		if shouldBeReady != definition.Ready() {
			t.Fatalf("操作 %s 的就绪状态不符：期望 ready=%v，实到 %v", definition.Op, shouldBeReady, definition.Ready())
		}
		if shouldBeReady && definition.Steps[0].RpcID != rpcID {
			t.Fatalf("操作 %s 的 rpcid = %q, 期望 %q", definition.Op, definition.Steps[0].RpcID, rpcID)
		}
	}
	if got, want := len(registry.Pending()), len(all)-len(discovered); got != want {
		t.Fatalf("待发现操作数 = %d, 期望 %d", got, want)
	}

	// The operations the official API cannot express must be in the catalogue.
	for _, required := range []Operation{"setStarred", "setDueDateTime", "setRecurrence"} {
		if _, ok := registry.Lookup(required); !ok {
			t.Fatalf("内置操作表缺少 %s", required)
		}
	}
}

// A persisted operations.json written by an older build must still pick up
// rpcids discovered since, otherwise git-distributed users keep a stale table
// forever.
func TestLoadFromAbsorbsNewSeedOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	stale := `{"version":1,"operations":[
		{"op":"createTask","label":"新建任务","steps":[{"rpcid":"OldRpc"}],"notes":"用户自己抓的"},
		{"op":"listTasks","label":"列出清单内的任务","steps":[],"notes":"旧版本"}
	]}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatalf("写入旧操作表: %v", err)
	}

	registry, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	snapshot, ok := registry.Lookup(OpSnapshot)
	if !ok || !snapshot.Ready() {
		t.Fatalf("旧操作表应补上 %s，实到 ok=%v def=%+v", OpSnapshot, ok, snapshot)
	}
	if snapshot.Steps[0].RpcID != "Yz8iP" {
		t.Fatalf("补上的 rpcid = %q, 期望 Yz8iP", snapshot.Steps[0].RpcID)
	}

	// The user's own mapping wins over the seed, and catalogue order is kept.
	userDefined, _ := registry.Lookup("createTask")
	if len(userDefined.Steps) != 1 || userDefined.Steps[0].RpcID != "OldRpc" {
		t.Fatalf("用户已抓的 createTask 被覆盖: %+v", userDefined.Steps)
	}
	order := registry.All()
	if order[0].Op != OpSnapshot {
		t.Fatalf("目录顺序应按内置表排列，首个为 %q, 期望 %q", order[0].Op, OpSnapshot)
	}
	last := order[len(order)-1].Op
	if last != "searchTasks" {
		t.Fatalf("未在内置表里的操作应排在末尾，实到 %q", last)
	}
	if len(order) != 23 {
		t.Fatalf("合并后操作数 = %d, 期望 23", len(order))
	}
}

func TestAssignPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	registry, err := Seeded(path)
	if err != nil {
		t.Fatalf("Seeded: %v", err)
	}

	steps := []Step{{
		RpcID: "AbCdE",
		Args:  `[null,{{json .TaskID}},[{{json .Starred}}]]`,
	}}
	if err := registry.Assign("setStarred", steps, "抓包于 2026-11-19"); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	definition, ok := reloaded.Lookup("setStarred")
	if !ok || !definition.Ready() {
		t.Fatalf("落盘后 setStarred 丢失: ok=%v def=%+v", ok, definition)
	}
	if definition.Steps[0].RpcID != "AbCdE" {
		t.Fatalf("rpcid 未保留: %+v", definition.Steps[0])
	}
	if !strings.Contains(definition.Notes, "2026-11-19") {
		t.Fatalf("notes 未保留: %q", definition.Notes)
	}
	if got, want := len(reloaded.Pending()), len(reloaded.All())-1-len(discovered); got != want {
		t.Fatalf("pending 数量 = %d, 期望 %d", got, want)
	}
}

func TestLoadFromFallsBackToSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	registry, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom 应在文件缺失时回退到内置版本: %v", err)
	}
	if registry.Path() != path {
		t.Fatalf("应记住路径以便后续落盘，实到 %q", registry.Path())
	}
	if len(registry.All()) == 0 {
		t.Fatal("回退后操作表不应为空")
	}
}

func TestAssignRejectsBadInput(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	cases := map[string][]Step{
		"空 steps":   nil,
		"缺少 rpcid":  {{Args: `[]`}},
		"args 模板非法": {{RpcID: "X", Args: `[{{json .X`}},
	}
	for name, steps := range cases {
		if err := registry.Assign("createTask", steps, ""); err == nil {
			t.Fatalf("%s: 应该被拒绝", name)
		}
	}
	if err := registry.Assign("", []Step{{RpcID: "X"}}, ""); err == nil {
		t.Fatal("空 op 应该被拒绝")
	}
}

func TestRenderSubstitutesParams(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	err = registry.Assign("setStarred", []Step{{
		RpcID: "AbCdE",
		Args:  `[null,{{json .TaskID}},[{{json .Starred}},null],{{json .Title}}]`,
	}}, "")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}

	calls, err := registry.Render("setStarred", map[string]any{
		"TaskID":  "task-1",
		"Starred": true,
		"Title": `带"引号"和换行
的标题`,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(calls) != 1 || calls[0].RpcID != "AbCdE" {
		t.Fatalf("调用不正确: %+v", calls)
	}

	encoded, err := json.Marshal(calls[0].Args)
	if err != nil {
		t.Fatalf("参数无法序列化: %v", err)
	}
	var args []any
	if err := json.Unmarshal(encoded, &args); err != nil {
		t.Fatalf("参数不是合法数组: %v", err)
	}
	if len(args) != 4 || args[1] != "task-1" || args[3] == nil {
		t.Fatalf("参数替换不正确: %v", args)
	}
	nested, ok := args[2].([]any)
	if !ok || len(nested) != 2 || nested[0] != true {
		t.Fatalf("嵌套布尔位替换不正确: %v", args[2])
	}
}

func TestRenderReportsUndiscoveredOperation(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	_, err = registry.Render("createTask", nil)
	if err == nil || !strings.Contains(err.Error(), "尚未配置") {
		t.Fatalf("应给出明确的未配置提示，实到: %v", err)
	}

	_, err = registry.Render("notAnOperation", nil)
	if err == nil || !strings.Contains(err.Error(), "未知操作") {
		t.Fatalf("应报告未知操作，实到: %v", err)
	}
}

func TestRenderReportsMissingParam(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if err := registry.Assign("createTask", []Step{{
		RpcID: "X",
		Args:  `[{{json .TaskListID}}]`,
	}}, ""); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	// missingkey=error must surface a clear failure instead of rendering <no value>.
	if _, err := registry.Render("createTask", map[string]any{}); err == nil {
		t.Fatal("缺少参数时应该报错")
	}
}

func TestServiceExecuteRejectsUndiscoveredOperation(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	service := NewService(registry, nil)

	if _, err := service.Execute(context.Background(), "listTasks", nil); err == nil {
		t.Fatal("未配置的操作不应可执行")
	}
}

func TestInMemoryRegistryDoesNotTouchDisk(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if registry.Path() != "" {
		t.Fatalf("内存操作表不应有路径，实到 %q", registry.Path())
	}

	seedBefore, err := os.ReadFile("operations.json")
	if err != nil {
		t.Fatalf("读取内置操作表: %v", err)
	}

	// Assign on an in-memory registry must succeed without persisting.
	if err := registry.Assign("listTasks", []Step{{RpcID: "X"}}, ""); err != nil {
		t.Fatalf("Assign: %v", err)
	}

	seedAfter, err := os.ReadFile("operations.json")
	if err != nil {
		t.Fatalf("重新读取内置操作表: %v", err)
	}
	if string(seedBefore) != string(seedAfter) {
		t.Fatal("内存操作表不应改写内嵌的种子文件")
	}
}

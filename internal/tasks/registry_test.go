package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultCatalogueIsCompleteButUndiscovered(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	all := registry.All()
	if len(all) < 20 {
		t.Fatalf("内置操作表应覆盖 20+ 个操作，实到 %d", len(all))
	}
	for _, definition := range all {
		if definition.Ready() {
			t.Fatalf("内置操作 %s 不应带 steps", definition.Op)
		}
		if definition.Label == "" || definition.Notes == "" {
			t.Fatalf("操作 %s 缺少 label 或 notes", definition.Op)
		}
	}
	if got := len(registry.Pending()); got != len(all) {
		t.Fatalf("所有操作都应待发现，实到 %d/%d", got, len(all))
	}

	// The operations the official API cannot express must be in the catalogue.
	for _, required := range []Operation{"setStarred", "setDueDateTime", "setRecurrence"} {
		if _, ok := registry.Lookup(required); !ok {
			t.Fatalf("内置操作表缺少 %s", required)
		}
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
	if got := len(reloaded.Pending()); got != len(reloaded.All())-1 {
		t.Fatalf("pending 数量不正确: %d", got)
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

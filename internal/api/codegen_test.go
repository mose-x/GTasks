package api

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleCapture = `{
  "url": "/_/TasksUi/data/batchexecute?rpcids=AbCdE&source-path=%2F&bl=boq.tasks.ui.20261118.00&rt=c&_reqid=424242",
  "via": "fetch",
  "rpcids": ["AbCdE"],
  "bl": "boq.tasks.ui.20261118.00",
  "at": "AT-token-123",
  "freq": [[["AbCdE","[\"task-1\",[null,true]]",null,"1"]]],
  "status": 200,
  "response": ")]}'\n\n[[\"wrb.fr\",\"AbCdE\",\"[\\\"ok\\\"]\",null,null,null,1],[\"di\",38]]",
  "t": 1795000000000,
  "href": "https://tasks.google.com/"
}`

func TestGenerateSnippetExtractsSteps(t *testing.T) {
	result, err := GenerateSnippet(json.RawMessage(sampleCapture))
	if err != nil {
		t.Fatalf("GenerateSnippet: %v", err)
	}

	if len(result.RpcIDs) != 1 || result.RpcIDs[0] != "AbCdE" {
		t.Fatalf("rpcids 不正确: %v", result.RpcIDs)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("应生成 1 个 step，实到 %d", len(result.Steps))
	}
	step := result.Steps[0]
	if step.RpcID != "AbCdE" {
		t.Fatalf("rpcid 不正确: %q", step.RpcID)
	}
	if step.Args != `["task-1",[null,true]]` {
		t.Fatalf("args 未还原为可读 JSON: %s", step.Args)
	}
	if len(result.Decoded) != 1 || result.Decoded[0].RpcID != "AbCdE" {
		t.Fatalf("响应未解码: %+v", result.Decoded)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("不应有警告: %v", result.Warnings)
	}
}

func TestGenerateSnippetProducesCompilableGo(t *testing.T) {
	result, err := GenerateSnippet(json.RawMessage(sampleCapture))
	if err != nil {
		t.Fatalf("GenerateSnippet: %v", err)
	}

	for _, want := range []string{
		"package snippet",
		`"gtasks/internal/batchexec"`,
		"client.Batch(ctx, []batchexec.Call{",
		`{RpcID: "AbCdE", Args: []any{"task-1", []any{nil, true}}},`,
	} {
		if !strings.Contains(result.Go, want) {
			t.Fatalf("生成的 Go 代码缺少 %q:\n%s", want, result.Go)
		}
	}
}

func TestGenerateSnippetWarnsWithoutBuildLabel(t *testing.T) {
	entry := strings.Replace(sampleCapture, `"bl": "boq.tasks.ui.20261118.00"`, `"bl": ""`, 1)
	result, err := GenerateSnippet(json.RawMessage(entry))
	if err != nil {
		t.Fatalf("GenerateSnippet: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("缺少 bl 时应给出提示")
	}
}

func TestGenerateSnippetRejectsEmptyCapture(t *testing.T) {
	if _, err := GenerateSnippet(json.RawMessage(`{"url":"https://tasks.google.com/","status":200}`)); err == nil {
		t.Fatal("没有 f.req 的记录应报错")
	}
	if _, err := GenerateSnippet(json.RawMessage(`not json`)); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestGoLiteralRendersJSONValues(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "nil"},
		{"bool", true, "true"},
		{"integer", float64(42), "42"},
		{"float", 1.5, "1.5"},
		{"string", `带"引号"`, `"带\"引号\""`},
		{"empty slice", []any{}, "[]any{}"},
		{"empty map", map[string]any{}, "map[string]any{}"},
		{"nested", []any{nil, []any{float64(1), "x"}}, "[]any{nil, []any{1, \"x\"}}"},
		{"map sorted", map[string]any{"b": float64(2), "a": float64(1)}, "map[string]any{\"a\": 1, \"b\": 2}"},
	}
	for _, testCase := range cases {
		if got := goLiteral(testCase.value); got != testCase.want {
			t.Errorf("%s: goLiteral = %s, want %s", testCase.name, got, testCase.want)
		}
	}
}

func TestExtractCallsHandlesMultiRpcBatch(t *testing.T) {
	freq := []json.RawMessage{json.RawMessage(`[["A","[]",null,"1"],["B","[1,2]",null,"2"]]`)}
	calls, err := extractCalls(freq)
	if err != nil {
		t.Fatalf("extractCalls: %v", err)
	}
	if len(calls) != 2 || calls[0].rpcID != "A" || calls[1].rpcID != "B" {
		t.Fatalf("解析结果不正确: %+v", calls)
	}
	encoded, _ := json.Marshal(calls[1].args)
	if string(encoded) != "[1,2]" {
		t.Fatalf("第二个调用的参数不正确: %s", encoded)
	}
}

package batchexec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeProducesPositionalFrames(t *testing.T) {
	encoded, err := Encode([]Call{
		{RpcID: "AbCdE", Args: []any{"task-id", []any{nil, 1}}},
		{RpcID: "FgHiJ", Args: nil},
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var outer [][]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &outer); err != nil {
		t.Fatalf("outer frame is not an array of arrays: %v", err)
	}
	if len(outer) != 1 {
		t.Fatalf("expected a single wrapping frame, got %d", len(outer))
	}
	if len(outer[0]) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(outer[0]))
	}

	var first []json.RawMessage
	if err := json.Unmarshal(outer[0][0], &first); err != nil {
		t.Fatalf("call frame: %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("expected the 4-slot positional frame, got %d slots", len(first))
	}

	var rpcID string
	if err := json.Unmarshal(first[0], &rpcID); err != nil || rpcID != "AbCdE" {
		t.Fatalf("slot 0 should be the rpcid, got %q (%v)", rpcID, err)
	}
	// Slot 1 holds the arguments as a JSON string, not as nested JSON.
	var argsText string
	if err := json.Unmarshal(first[1], &argsText); err != nil {
		t.Fatalf("slot 1 should be a JSON string: %v", err)
	}
	if argsText != `["task-id",[null,1]]` {
		t.Fatalf("unexpected encoded args: %s", argsText)
	}
	if string(first[2]) != "null" {
		t.Fatalf("slot 2 should be null, got %s", first[2])
	}
	var sequence string
	if err := json.Unmarshal(first[3], &sequence); err != nil || sequence != "1" {
		t.Fatalf("slot 3 should be the 1-based sequence, got %q (%v)", sequence, err)
	}

	var second []json.RawMessage
	if err := json.Unmarshal(outer[0][1], &second); err != nil {
		t.Fatalf("second call frame: %v", err)
	}
	var secondArgs string
	if err := json.Unmarshal(second[1], &secondArgs); err != nil {
		t.Fatalf("second args: %v", err)
	}
	if secondArgs != "[]" {
		t.Fatalf("nil args should encode to an empty array, got %s", secondArgs)
	}
}

func TestDecodePlainResponse(t *testing.T) {
	body := ")]}'\n\n" +
		`[["wrb.fr","AbCdE","[\"ok\",[1,2]]",null,null,null,1],` +
		`["wrb.fr","FgHiJ",null,"[\"UNAUTHENTICATED\"]",null,null,2],` +
		`["di",38],` +
		`["af.httprm",37,"5314567270682293609",6],` +
		`["e",4,null,null,643]]`

	responses, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected only wrb.fr frames, got %d", len(responses))
	}

	first := responses[0]
	if first.RpcID != "AbCdE" || first.Index != 1 {
		t.Fatalf("unexpected first response: %+v", first)
	}
	if len(first.Error) > 0 {
		t.Fatalf("first response should carry no error, got %s", first.Error)
	}
	var data []any
	if err := json.Unmarshal(first.Data, &data); err != nil {
		t.Fatalf("first data should be valid JSON: %v", err)
	}
	if len(data) != 2 || data[0] != "ok" {
		t.Fatalf("unexpected decoded data: %v", data)
	}

	second := responses[1]
	if second.RpcID != "FgHiJ" || second.Index != 2 {
		t.Fatalf("unexpected second response: %+v", second)
	}
	if !strings.Contains(string(second.Error), "UNAUTHENTICATED") {
		t.Fatalf("expected the error frame to be preserved, got %s", second.Error)
	}
}

func TestDecodeChunkedResponse(t *testing.T) {
	body := "12\n)]}'\n\n4a\n" +
		`[["wrb.fr","AbCdE","[\"ok\"]",null,null,null,1],["di",38]]`

	responses, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(responses) != 1 || responses[0].RpcID != "AbCdE" {
		t.Fatalf("unexpected responses: %+v", responses)
	}
	if responses[0].Raw != `["ok"]` {
		t.Fatalf("unexpected raw payload: %s", responses[0].Raw)
	}
}

// TestDecodeRealChunkedBody 用抓包得到的真实响应做回归：rt=c 会把载荷切成多个
// 带长度前缀的数据块，旧实现只读第一块，且服务级错误帧藏在第 5 槽。
func TestDecodeRealChunkedBody(t *testing.T) {
	body := ")]}'\n\n193\n" +
		`[["wrb.fr","SOV0tb",null,null,null,[13,null,[["type.googleapis.com/google.internal.tasks.v1.ErrorInfo",[null,null,null,null,null,1]]]],"1"],` +
		`["di",44],["af.httprm",44,"5778740775962784027",1]]` +
		"\n25\n" +
		`[["e",4,null,null,229]]` +
		"\n"

	responses, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("expected a single wrb.fr frame, got %d: %+v", len(responses), responses)
	}
	only := responses[0]
	if only.RpcID != "SOV0tb" {
		t.Fatalf("unexpected rpcid: %q", only.RpcID)
	}
	if only.Index != 1 {
		t.Fatalf("序列号应支持数字字符串，得到 %d", only.Index)
	}
	if !strings.Contains(string(only.Error), "ErrorInfo") {
		t.Fatalf("结构化错误帧应被保留: %s", only.Error)
	}
	if len(only.Data) != 0 {
		t.Fatalf("出错调用不应带 data: %s", only.Data)
	}
}

// TestDecodeCollectsEveryChunk 覆盖成功载荷与后续数据块同时存在的情况。
func TestDecodeCollectsEveryChunk(t *testing.T) {
	body := ")]}'\n\n60\n" +
		`[["wrb.fr","AbCdE","[\"ok\"]",null,null,null,"generic"]]` +
		"\n58\n" +
		`[["wrb.fr","FgHiJ","[\"second\"]",null,null,null,"generic"]]` +
		"\n25\n" +
		`[["e",4,null,null,143]]` +
		"\n"

	responses, err := Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected both chunks to be decoded, got %+v", responses)
	}
	if responses[0].RpcID != "AbCdE" || responses[1].RpcID != "FgHiJ" {
		t.Fatalf("unexpected order: %+v", responses)
	}
	if responses[1].Raw != `["second"]` {
		t.Fatalf("unexpected payload: %s", responses[1].Raw)
	}
}

func TestDecodeRejectsNonBatchExecuteBody(t *testing.T) {
	for _, body := range []string{"", "   ", "<html>login redirect</html>", ")]}'"} {
		if _, err := Decode(body); err == nil {
			t.Fatalf("expected an error for body %q", body)
		}
	}
}

func TestAuthBadClassifiesSessionLoss(t *testing.T) {
	for _, state := range []string{"unauthorized", "redirected"} {
		if !AuthBad(state) {
			t.Fatalf("%q 应判定为会话失效", state)
		}
	}
	for _, state := range []string{"", "ok", "anonymous", "http429", "http500"} {
		if AuthBad(state) {
			t.Fatalf("%q 不应判定为会话失效", state)
		}
	}
}

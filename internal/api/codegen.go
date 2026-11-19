package api

import (
	"encoding/json"
	"fmt"
	"go/format"
	"sort"
	"strconv"
	"strings"

	"gtasks/internal/batchexec"
	"gtasks/internal/tasks"
)

// capture mirrors the entry shape produced by the in-page sniffer.
type capture struct {
	URL      string            `json:"url"`
	Via      string            `json:"via"`
	RpcIDs   []string          `json:"rpcids"`
	Bl       string            `json:"bl"`
	At       string            `json:"at"`
	Freq     []json.RawMessage `json:"freq"`
	FreqRaw  string            `json:"freqRaw"`
	Status   int               `json:"status"`
	Response string            `json:"response"`
	T        int64             `json:"t"`
	Href     string            `json:"href"`
}

// CodegenResult is everything the UI needs to turn a capture into code.
type CodegenResult struct {
	RpcIDs   []string             `json:"rpcids"`
	Steps    []tasks.Step         `json:"steps"`
	Go       string               `json:"go"`
	Decoded  []batchexec.Response `json:"decoded,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
}

// GenerateSnippet turns one sniffed capture into registry steps plus a
// ready-to-paste Go call.
func GenerateSnippet(raw json.RawMessage) (*CodegenResult, error) {
	var entry capture
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("api: 抓包记录不是合法 JSON: %w", err)
	}

	calls, err := extractCalls(entry.Freq)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, fmt.Errorf("api: 这条记录里没有可用的 RPC 调用（url=%s）", entry.URL)
	}

	result := &CodegenResult{RpcIDs: entry.RpcIDs}
	if len(result.RpcIDs) == 0 {
		for _, call := range calls {
			result.RpcIDs = append(result.RpcIDs, call.rpcID)
		}
	}

	for _, call := range calls {
		step := tasks.Step{RpcID: call.rpcID}
		if call.args != nil {
			encoded, err := json.Marshal(call.args)
			if err == nil {
				step.Args = string(encoded)
			}
		}
		result.Steps = append(result.Steps, step)
	}

	if entry.Response != "" {
		if decoded, err := batchexec.Decode(entry.Response); err == nil {
			result.Decoded = decoded
		} else {
			result.Warnings = append(result.Warnings, "响应解码失败: "+err.Error())
		}
	}
	if entry.Bl == "" {
		result.Warnings = append(result.Warnings, "URL 里没有 bl，可能需要从页面的 WIZ_global_data.cfb2h 取")
	}

	result.Go = renderGo(calls)
	return result, nil
}

type rpcCall struct {
	rpcID string
	args  any
}

// extractCalls unpacks the doubly encoded f.req structure:
// [[["rpcid","<args as a JSON string>",null,"1"], ...]]
func extractCalls(freq []json.RawMessage) ([]rpcCall, error) {
	if len(freq) == 0 {
		return nil, nil
	}
	var wrapper [][]json.RawMessage
	if err := json.Unmarshal(freq[0], &wrapper); err != nil {
		return nil, fmt.Errorf("api: f.req 外层结构无法解析: %w", err)
	}

	var calls []rpcCall
	for _, frame := range wrapper {
		if len(frame) < 2 {
			continue
		}
		var rpcID string
		if err := json.Unmarshal(frame[0], &rpcID); err != nil || rpcID == "" {
			continue
		}
		var argsText string
		if err := json.Unmarshal(frame[1], &argsText); err != nil {
			return nil, fmt.Errorf("api: %s 的参数不是 JSON 字符串: %w", rpcID, err)
		}
		var args any
		if argsText != "" {
			if err := json.Unmarshal([]byte(argsText), &args); err != nil {
				return nil, fmt.Errorf("api: %s 的参数无法二次解析: %w", rpcID, err)
			}
		}
		calls = append(calls, rpcCall{rpcID: rpcID, args: args})
	}
	return calls, nil
}

// renderGo produces gofmt-clean source for a batch call.
func renderGo(calls []rpcCall) string {
	var builder strings.Builder
	builder.WriteString("package snippet\n\nimport (\n\t\"context\"\n\n\t\"gtasks/internal/batchexec\"\n)\n\nfunc call(client *batchexec.Client) (*batchexec.BatchResult, error) {\n\tctx := context.Background()\n\treturn client.Batch(ctx, []batchexec.Call{\n")
	for _, call := range calls {
		builder.WriteString("\t\t{RpcID: ")
		builder.WriteString(strconv.Quote(call.rpcID))
		builder.WriteString(", Args: ")
		builder.WriteString(goLiteral(call.args))
		builder.WriteString("},\n")
	}
	builder.WriteString("\t}, batchexec.Options{})\n}\n")

	formatted, err := format.Source([]byte(builder.String()))
	if err != nil {
		// Fall back to the unformatted source rather than failing outright.
		return builder.String()
	}
	return string(formatted)
}

// goLiteral renders a decoded JSON value as Go source.
func goLiteral(value any) string {
	switch typed := value.(type) {
	case nil:
		return "nil"
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case string:
		return strconv.Quote(typed)
	case []any:
		if len(typed) == 0 {
			return "[]any{}"
		}
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, goLiteral(item))
		}
		return "[]any{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		if len(typed) == 0 {
			return "map[string]any{}"
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, strconv.Quote(key)+": "+goLiteral(typed[key]))
		}
		return "map[string]any{" + strings.Join(parts, ", ") + "}"
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "nil"
		}
		return "json.RawMessage(" + strconv.Quote(string(encoded)) + ")"
	}
}

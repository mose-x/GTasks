package batchexec

import (
	"context"
	"encoding/json"
	"fmt"

	"gtasks/internal/bridge"
)

// DefaultPath is the batchexecute endpoint used by the Tasks web app. It is
// only a fallback: the page publishes its own mount point in
// WIZ_global_data.Im6cmf and the bridge prefers that, so a Google-side rename
// does not break the client.
const DefaultPath = "/_/TasksWebEmbedUi/data/batchexecute"

// Session is a snapshot of the Google page the bridge is attached to.
type Session struct {
	URL                string         `json:"url"`
	Origin             string         `json:"origin"`
	Pathname           string         `json:"pathname"`
	Title              string         `json:"title"`
	UserAgent          string         `json:"userAgent"`
	ReadyState         string         `json:"readyState"`
	At                 string         `json:"at"`
	Bl                 string         `json:"bl"`
	LoggedIn           bool           `json:"loggedIn"`
	WizKeys            []string       `json:"wizKeys"`
	WizScalars         map[string]any `json:"wizScalars"`
	VisibleCookieNames []string       `json:"visibleCookieNames"`
	FetchPatched       bool           `json:"fetchPatched"`
	XHRPatched         bool           `json:"xhrPatched"`
}

// Options tunes a single batch request.
type Options struct {
	// Path overrides the batchexecute endpoint. Defaults to DefaultPath.
	Path string `json:"path,omitempty"`
	// SourcePath fills the source-path query parameter. Defaults to the page path.
	SourcePath string `json:"sourcePath,omitempty"`
	// RT selects the response framing. Defaults to "c" (chunked), which Google's
	// own client uses. Use "" for the plain single-body form.
	RT string `json:"rt,omitempty"`
	// At overrides the SNlM0e CSRF token.
	At string `json:"at,omitempty"`
	// Bl overrides the build label.
	Bl string `json:"bl,omitempty"`
	// AllowAnonymous permits a call without an `at` token.
	AllowAnonymous bool `json:"allowAnonymous,omitempty"`
}

// BatchResult is the decoded outcome of one batch request.
type BatchResult struct {
	Status    int        `json:"status"`
	URL       string     `json:"url"`
	Path      string     `json:"path"`
	At        string     `json:"at"`
	Bl        string     `json:"bl"`
	RpcIDs    []string   `json:"rpcids"`
	ElapsedMs int64      `json:"elapsedMs"`
	Raw       string     `json:"raw"`
	Responses []Response `json:"responses"`
	// Auth 是页面对本次响应的判定：ok / unauthorized / redirected / http<码>。
	// unauthorized 或 redirected 说明会话已失效，调用方应引导重新登录。
	Auth string `json:"auth,omitempty"`
}

// AuthBad reports whether an auth classification means the session is unusable.
func AuthBad(state string) bool {
	return state == "unauthorized" || state == "redirected"
}

// Client drives batchexecute through the page bridge. Every request is issued
// by JavaScript running on the Google origin, so session cookies attach
// automatically and no credential ever reaches Go.
type Client struct {
	bridge *bridge.Bridge
}

// NewClient returns a client bound to the given bridge.
func NewClient(br *bridge.Bridge) *Client {
	return &Client{bridge: br}
}

// Probe reports the state of the session page.
func (c *Client) Probe(ctx context.Context) (*Session, error) {
	raw, err := c.bridge.Call(ctx, "probe", nil)
	if err != nil {
		return nil, err
	}
	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, fmt.Errorf("batchexec: 解析会话信息失败: %w", err)
	}
	return &session, nil
}

// Batch sends one or more RPCs in a single request and decodes the result.
func (c *Client) Batch(ctx context.Context, calls []Call, options Options) (*BatchResult, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("batchexec: 至少需要一个 RPC")
	}
	if options.Path == "" {
		options.Path = DefaultPath
	}
	if options.RT == "" {
		options.RT = "c"
	}

	rpcIDs := make([]string, 0, len(calls))
	freqs := make([]any, 0, len(calls))
	for _, call := range calls {
		rpcIDs = append(rpcIDs, call.RpcID)
		if call.Args == nil {
			freqs = append(freqs, []any{})
		} else {
			freqs = append(freqs, call.Args)
		}
	}

	raw, err := c.bridge.Call(ctx, "batch", map[string]any{
		"rpcids": rpcIDs,
		"freqs":  freqs,
		"opts":   options,
	})
	if err != nil {
		return nil, err
	}

	var result BatchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("batchexec: 解析批次结果失败: %w", err)
	}
	responses, err := Decode(result.Raw)
	if err != nil {
		// Keep the raw body so the caller can still inspect it.
		result.Responses = nil
		return &result, fmt.Errorf("batchexec: %w", err)
	}
	result.Responses = responses
	return &result, nil
}

// InitDataKey describes one server-rendered payload embedded in the page HTML.
type InitDataKey struct {
	Key  string `json:"key"`
	Hash string `json:"hash"`
	// Size is the JSON length of the payload, used to decide how to page it.
	Size int `json:"size"`
}

// InitData is one AF_initDataCallback payload. Tasks ships its first screen
// (every task list and task) this way instead of an RPC, so reading it is what
// makes the client render real data before any rpcid is known.
type InitData struct {
	Key       string          `json:"key"`
	Hash      string          `json:"hash"`
	Found     bool            `json:"found"`
	Truncated bool            `json:"truncated"`
	Size      int             `json:"size"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// InitDataKeys lists the embedded payloads the page has received so far.
func (c *Client) InitDataKeys(ctx context.Context) ([]InitDataKey, error) {
	raw, err := c.bridge.Call(ctx, "initDataKeys", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Keys []InitDataKey `json:"keys"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("batchexec: 解析初始化数据清单失败: %w", err)
	}
	return payload.Keys, nil
}

// InitData fetches one embedded payload by key.
func (c *Client) InitData(ctx context.Context, key string) (*InitData, error) {
	raw, err := c.bridge.Call(ctx, "initData", map[string]any{"key": key})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Key       string `json:"key"`
		Hash      string `json:"hash"`
		Found     bool   `json:"found"`
		Truncated bool   `json:"truncated"`
		Size      int    `json:"size"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("batchexec: 解析初始化数据失败: %w", err)
	}
	result := &InitData{
		Key:       payload.Key,
		Hash:      payload.Hash,
		Found:     payload.Found,
		Truncated: payload.Truncated,
		Size:      payload.Size,
	}
	if payload.Found && !payload.Truncated && json.Valid([]byte(payload.Data)) {
		result.Data = json.RawMessage(payload.Data)
	}
	return result, nil
}

// EvalResult carries the outcome of one in-page script evaluation.
type EvalResult struct {
	Truncated bool            `json:"truncated"`
	Size      int             `json:"size"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// Eval runs a script inside the session page and returns its JSON value. It is
// the reverse-engineering escape hatch: inspecting globals and driving read-only
// UI interactions is how the rpcid catalogue gets discovered.
func (c *Client) Eval(ctx context.Context, script string) (*EvalResult, error) {
	raw, err := c.bridge.Call(ctx, "evalScript", map[string]any{"js": script})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Truncated bool   `json:"truncated"`
		Size      int    `json:"size"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("batchexec: 解析 eval 结果失败: %w", err)
	}
	result := &EvalResult{Truncated: payload.Truncated, Size: payload.Size}
	if json.Valid([]byte(payload.Result)) {
		result.Result = json.RawMessage(payload.Result)
	}
	return result, nil
}

// HealthState is the outcome of one lightweight session probe.
type HealthState struct {
	// State is ok / anonymous / unauthorized / redirected / http<码>.
	State  string `json:"state"`
	Status int    `json:"status"`
	At     string `json:"at"`
}

// Health asks the page to issue a minimal batchexecute call with a rpcid that
// cannot exist. A live session answers 200 with an error frame; a dead cookie
// answers 401 or a redirect into the Google login flow. Unlike Probe it proves
// the session still works, not merely that the page once loaded logged in.
func (c *Client) Health(ctx context.Context) (*HealthState, error) {
	raw, err := c.bridge.Call(ctx, "health", nil)
	if err != nil {
		return nil, err
	}
	var state HealthState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("batchexec: 解析会话健康状态失败: %w", err)
	}
	return &state, nil
}

// SniffControl toggles the in-page batchexecute recorder.
type SniffControl struct {
	Sniffing bool  `json:"sniffing"`
	Sniffed  int64 `json:"sniffed"`
}

// SniffStart begins recording batchexecute traffic generated by the page.
func (c *Client) SniffStart(ctx context.Context) (*SniffControl, error) {
	return c.sniff(ctx, "sniffStart")
}

// SniffStop pauses recording. Captures already sent to Go are kept.
func (c *Client) SniffStop(ctx context.Context) (*SniffControl, error) {
	return c.sniff(ctx, "sniffStop")
}

// SniffStatus reports whether the recorder is active.
func (c *Client) SniffStatus(ctx context.Context) (*SniffControl, error) {
	return c.sniff(ctx, "sniffStatus")
}

func (c *Client) sniff(ctx context.Context, method string) (*SniffControl, error) {
	raw, err := c.bridge.Call(ctx, method, nil)
	if err != nil {
		return nil, err
	}
	var control SniffControl
	if err := json.Unmarshal(raw, &control); err != nil {
		return nil, fmt.Errorf("batchexec: 解析抓包状态失败: %w", err)
	}
	return &control, nil
}

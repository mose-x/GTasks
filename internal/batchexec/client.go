package batchexec

import (
	"context"
	"encoding/json"
	"fmt"

	"gtasks/internal/bridge"
)

// DefaultPath is the batchexecute endpoint used by the Tasks web app.
const DefaultPath = "/_/TasksUi/data/batchexecute"

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

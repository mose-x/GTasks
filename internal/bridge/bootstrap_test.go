package bridge

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

//go:embed bootstrap_harness.mjs
var harnessSource string

type harnessRequest struct {
	URL  string `json:"url"`
	Init struct {
		Method      string            `json:"method"`
		Credentials string            `json:"credentials"`
		Headers     map[string]string `json:"headers"`
		Body        string            `json:"body"`
	} `json:"init"`
}

type harnessOutput struct {
	Messages []string          `json:"messages"`
	Requests []harnessRequest  `json:"requests"`
	Storage  map[string]string `json:"storage"`
}

type harnessMessage struct {
	P     string          `json:"p"`
	Kind  string          `json:"kind"`
	ID    uint64          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
	Info  json.RawMessage `json:"info"`
	Entry json.RawMessage `json:"entry"`
}

// runHarness executes bootstrap.js under Node against a fake logged-in
// tasks.google.com page and returns everything it posted and requested.
func runHarness(t *testing.T, scripts []string) harnessOutput {
	t.Helper()
	return runHarnessSpec(t, scripts, nil)
}

// runHarnessSpec is runHarness with extra harness knobs (location, readyState).
func runHarnessSpec(t *testing.T, scripts []string, extra map[string]any) harnessOutput {
	t.Helper()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node 不可用，跳过 bootstrap.js 集成测试")
	}

	dir := t.TempDir()
	bootstrapPath := filepath.Join(dir, "bootstrap.js")
	if err := os.WriteFile(bootstrapPath, []byte(BootstrapJS), 0o600); err != nil {
		t.Fatalf("写入 bootstrap.js: %v", err)
	}
	harnessPath := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harnessPath, []byte(harnessSource), 0o600); err != nil {
		t.Fatalf("写入 harness: %v", err)
	}

	specFields := map[string]any{
		"scripts":     scripts,
		"fetchBody":   ")]}'\n\n[[\"wrb.fr\",\"AbCdE\",\"[\\\"ok\\\"]\",null,null,null,1]]",
		"fetchStatus": 200,
	}
	for key, value := range extra {
		specFields[key] = value
	}
	spec, err := json.Marshal(specFields)
	if err != nil {
		t.Fatalf("编码 spec: %v", err)
	}
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, spec, 0o600); err != nil {
		t.Fatalf("写入 spec: %v", err)
	}

	output, err := exec.Command(node, harnessPath, bootstrapPath, specPath).CombinedOutput()
	if err != nil {
		t.Fatalf("harness 执行失败: %v\n%s", err, output)
	}

	var result harnessOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("解析 harness 输出失败: %v\n%s", err, output)
	}
	return result
}

func decodeMessages(t *testing.T, output harnessOutput) []harnessMessage {
	t.Helper()
	decoded := make([]harnessMessage, 0, len(output.Messages))
	for _, raw := range output.Messages {
		var message harnessMessage
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			// "wails:runtime:ready" is a bare reserved string, not JSON.
			continue
		}
		if message.P != Protocol {
			t.Fatalf("消息缺少协议标记: %s", raw)
		}
		decoded = append(decoded, message)
	}
	return decoded
}

func findResult(t *testing.T, messages []harnessMessage, id uint64) harnessMessage {
	t.Helper()
	for _, message := range messages {
		if message.Kind == "result" && message.ID == id {
			return message
		}
	}
	t.Fatalf("找不到 id=%d 的 result，实到消息: %+v", id, messages)
	return harnessMessage{}
}

// The bootstrap must report readiness itself, otherwise Wails keeps ExecJS
// queued forever on an external origin.
func TestBootstrapUnlocksExecJS(t *testing.T) {
	output := runHarness(t, []string{BootstrapJS})

	found := false
	for _, raw := range output.Messages {
		if raw == "wails:runtime:ready" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootstrap 没有回发 wails:runtime:ready，ExecJS 将永远被门控。实到: %v", output.Messages)
	}

	messages := decodeMessages(t, output)
	if len(messages) == 0 || messages[0].Kind != "ready" {
		t.Fatalf("第一条 JSON 消息应为 ready，实到: %+v", messages)
	}

	var info struct {
		At       string `json:"at"`
		Bl       string `json:"bl"`
		LoggedIn bool   `json:"loggedIn"`
		Origin   string `json:"origin"`
	}
	if err := json.Unmarshal(messages[0].Info, &info); err != nil {
		t.Fatalf("解析 ready.info: %v", err)
	}
	if info.At != "AT-token-123" || info.Bl != "boq.tasks.ui.20261118.00" || !info.LoggedIn {
		t.Fatalf("会话信息不正确: %+v", info)
	}
	if info.Origin != "https://tasks.google.com" {
		t.Fatalf("origin 不正确: %s", info.Origin)
	}
}

// Windows registers the script for the whole webview lifetime, so it also runs
// in the redirect stub and on any non-Google page. It must stay silent there.
func TestBootstrapStaysInertOffGoogleHosts(t *testing.T) {
	output := runHarnessSpec(t, []string{BootstrapJS}, map[string]any{
		"location": map[string]string{
			"href":     "https://evil.example.com/login",
			"origin":   "https://evil.example.com",
			"pathname": "/login",
			"hostname": "evil.example.com",
		},
	})
	if len(output.Messages) != 0 {
		t.Fatalf("非 Google 域名不应发出任何消息，实到: %v", output.Messages)
	}
}

func TestBootstrapHostFallsBackToOrigin(t *testing.T) {
	// WebKitGTK reports a full URI and some documents lack location.hostname;
	// the guard must still recognise the origin form.
	output := runHarnessSpec(t, []string{BootstrapJS}, map[string]any{
		"location": map[string]string{
			"href":     "https://accounts.google.com/signin",
			"origin":   "https://accounts.google.com",
			"pathname": "/signin",
			"hostname": "",
		},
	})
	if len(output.Messages) == 0 {
		t.Fatal("accounts.google.com 应被识别为 Google 域名")
	}
}

// Document-start injection announces before WIZ_global_data exists; the deferred
// announce on load must report the logged-in state.
func TestBootstrapReannouncesOnLoad(t *testing.T) {
	output := runHarnessSpec(t, []string{BootstrapJS}, map[string]any{
		"readyState": "loading",
	})
	messages := decodeMessages(t, output)
	var readies []harnessMessage
	for _, message := range messages {
		if message.Kind == "ready" {
			readies = append(readies, message)
		}
	}
	if len(readies) != 2 {
		t.Fatalf("应在 document-start 与 load 各 announce 一次，实到 %d 次", len(readies))
	}
	var late struct {
		At       string `json:"at"`
		LoggedIn bool   `json:"loggedIn"`
	}
	if err := json.Unmarshal(readies[1].Info, &late); err != nil {
		t.Fatalf("解析第二次 ready.info: %v", err)
	}
	if !late.LoggedIn || late.At != "AT-token-123" {
		t.Fatalf("load 之后应报告已登录，实到: %+v", late)
	}
}

func TestBootstrapProbe(t *testing.T) {
	script, err := dispatchScript(11, "probe", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	output := runHarness(t, []string{BootstrapJS, script})
	result := findResult(t, decodeMessages(t, output), 11)
	if !result.OK {
		t.Fatalf("probe 失败: %s", result.Error)
	}
	if !strings.Contains(string(result.Data), `"userAgent":"harness-ua"`) {
		t.Fatalf("probe 未返回页面信息: %s", result.Data)
	}
}

func TestBootstrapBatchIssuesCorrectRequest(t *testing.T) {
	script, err := dispatchScript(22, "batch", map[string]any{
		"rpcids": []string{"AbCdE", "FgHiJ"},
		"freqs":  []any{[]any{}, []any{"task-1", nil, 3}},
		"opts":   map[string]any{"path": "/_/TasksUi/data/batchexecute", "rt": "c"},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	output := runHarness(t, []string{BootstrapJS, script})
	if len(output.Requests) != 1 {
		t.Fatalf("应发出 1 个请求，实到 %d", len(output.Requests))
	}
	request := output.Requests[0]

	if !strings.HasPrefix(request.URL, "/_/TasksUi/data/batchexecute?") {
		t.Fatalf("endpoint 不正确: %s", request.URL)
	}
	for _, want := range []string{"rpcids=AbCdE%2CFgHiJ", "bl=boq.tasks.ui.20261118.00", "rt=c", "source-path=%2F", "_reqid="} {
		if !strings.Contains(request.URL, want) {
			t.Fatalf("URL 缺少 %q: %s", want, request.URL)
		}
	}

	if request.Init.Method != "POST" {
		t.Fatalf("应为 POST，实到 %s", request.Init.Method)
	}
	if request.Init.Credentials != "include" {
		t.Fatalf("必须携带 cookie，实到 credentials=%s", request.Init.Credentials)
	}
	if request.Init.Headers["X-Same-Domain"] != "1" {
		t.Fatalf("缺少 X-Same-Domain 头: %v", request.Init.Headers)
	}

	values, err := url.ParseQuery(request.Init.Body)
	if err != nil {
		t.Fatalf("请求体不是合法表单: %v", err)
	}
	if values.Get("at") != "AT-token-123" {
		t.Fatalf("at 不正确: %q", values.Get("at"))
	}

	var outer [][]json.RawMessage
	if err := json.Unmarshal([]byte(values.Get("f.req")), &outer); err != nil {
		t.Fatalf("f.req 不是合法的批次结构: %v", err)
	}
	if len(outer) != 1 || len(outer[0]) != 2 {
		t.Fatalf("f.req 结构不正确: %s", values.Get("f.req"))
	}
	var second []json.RawMessage
	if err := json.Unmarshal(outer[0][1], &second); err != nil {
		t.Fatalf("第二个调用帧解析失败: %v", err)
	}
	var rpcID string
	_ = json.Unmarshal(second[0], &rpcID)
	var args string
	_ = json.Unmarshal(second[1], &args)
	if rpcID != "FgHiJ" || args != `["task-1",null,3]` {
		t.Fatalf("第二个调用编码不正确: rpcid=%q args=%s", rpcID, args)
	}

	result := findResult(t, decodeMessages(t, output), 22)
	if !result.OK {
		t.Fatalf("batch 调用失败: %s", result.Error)
	}
	var data struct {
		Status int    `json:"status"`
		Raw    string `json:"raw"`
		At     string `json:"at"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("解析 batch 结果: %v", err)
	}
	if data.Status != 200 || !strings.Contains(data.Raw, "wrb.fr") || data.At != "AT-token-123" {
		t.Fatalf("batch 结果不正确: %+v", data)
	}
}

// Wails re-runs the bootstrap after every navigation. A second copy in the same
// document must not disarm the recorder, and the fetch/XHR wrappers installed by
// the first copy must keep reporting.
func TestBootstrapReinjectionKeepsRecorderArmed(t *testing.T) {
	start, err := dispatchScript(51, "sniffStart", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	status, err := dispatchScript(53, "sniffStatus", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	batch, err := dispatchScript(52, "batch", map[string]any{
		"rpcids": []string{"AbCdE"},
		"freqs":  []any{[]any{}},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	output := runHarness(t, []string{BootstrapJS, start, BootstrapJS, status, batch})
	messages := decodeMessages(t, output)

	var control struct {
		Sniffing bool  `json:"sniffing"`
		Sniffed  int64 `json:"sniffed"`
	}
	if err := json.Unmarshal(findResult(t, messages, 53).Data, &control); err != nil {
		t.Fatalf("解析 sniffStatus: %v", err)
	}
	if !control.Sniffing {
		t.Fatalf("重注入后抓包开关被重置: %+v", control)
	}

	var entry struct {
		RpcIDs []string `json:"rpcids"`
		Via    string   `json:"via"`
		Freq   []any    `json:"freq"`
		Href   string   `json:"href"`
	}
	found := false
	for _, message := range messages {
		if message.Kind != "sniff" {
			continue
		}
		found = true
		if err := json.Unmarshal(message.Entry, &entry); err != nil {
			t.Fatalf("解析抓包条目: %v", err)
		}
	}
	if !found {
		t.Fatalf("重注入后 fetch 包装器不再上报，实到消息: %+v", messages)
	}
	if len(entry.RpcIDs) != 1 || entry.RpcIDs[0] != "AbCdE" || entry.Via != "fetch" {
		t.Fatalf("抓包条目不正确: %+v", entry)
	}
	if entry.Href != "https://tasks.google.com/" {
		t.Fatalf("抓包条目缺少页面地址: %+v", entry)
	}
}

func healthScript(t *testing.T, id uint64) string {
	t.Helper()
	script, err := dispatchScript(id, "health", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	return script
}

func decodeHealth(t *testing.T, messages []harnessMessage, id uint64) struct {
	State  string `json:"state"`
	Status int    `json:"status"`
} {
	t.Helper()
	var health struct {
		State  string `json:"state"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(findResult(t, messages, id).Data, &health); err != nil {
		t.Fatalf("解析 health 结果: %v", err)
	}
	return health
}

// 会话活着：200 + 一条错误帧。
func TestBootstrapHealthReportsOK(t *testing.T) {
	output := runHarness(t, []string{BootstrapJS, healthScript(t, 61)})
	health := decodeHealth(t, decodeMessages(t, output), 61)
	if health.State != "ok" || health.Status != 200 {
		t.Fatalf("健康状态不正确: %+v", health)
	}
}

// cookie 失效时 Google 会 302 到登录页，fetch 跟随之后 response.redirected 为真。
func TestBootstrapHealthDetectsLoginRedirect(t *testing.T) {
	output := runHarnessSpec(t, []string{BootstrapJS, healthScript(t, 62)}, map[string]any{
		"fetchRedirected": true,
		"fetchUrl":        "https://accounts.google.com/ServiceLogin",
	})
	health := decodeHealth(t, decodeMessages(t, output), 62)
	if health.State != "redirected" {
		t.Fatalf("应识别登录重定向，实到: %+v", health)
	}
}

// at 被拒绝时 batchexecute 回 401。
func TestBootstrapHealthDetectsUnauthorized(t *testing.T) {
	output := runHarnessSpec(t, []string{BootstrapJS, healthScript(t, 63)}, map[string]any{
		"fetchStatus": 401,
	})
	health := decodeHealth(t, decodeMessages(t, output), 63)
	if health.State != "unauthorized" {
		t.Fatalf("应识别 401，实到: %+v", health)
	}
}

// 未登录时不应发请求，直接报 anonymous。
func TestBootstrapHealthAnonymousWithoutToken(t *testing.T) {
	output := runHarnessSpec(t, []string{BootstrapJS, healthScript(t, 64)}, map[string]any{
		"location": map[string]string{
			"href":     "https://accounts.google.com/signin",
			"origin":   "https://accounts.google.com",
			"pathname": "/signin",
			"hostname": "accounts.google.com",
		},
		"wizEmpty": true,
	})
	messages := decodeMessages(t, output)
	health := decodeHealth(t, messages, 64)
	if health.State != "anonymous" {
		t.Fatalf("无 at 时应报 anonymous，实到: %+v", health)
	}
	if len(output.Requests) != 0 {
		t.Fatalf("anonymous 时不应发请求，实到 %d", len(output.Requests))
	}
}

// 普通 batch 调用也要带上 auth 判定，调用方才能区分业务错误与会话失效。
func TestBootstrapBatchCarriesAuthState(t *testing.T) {
	script, err := dispatchScript(65, "batch", map[string]any{
		"rpcids": []string{"AbCdE"},
		"freqs":  []any{[]any{}},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	output := runHarnessSpec(t, []string{BootstrapJS, script}, map[string]any{"fetchStatus": 403})
	result := findResult(t, decodeMessages(t, output), 65)
	var data struct {
		Auth   string `json:"auth"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("解析 batch 结果: %v", err)
	}
	if data.Auth != "unauthorized" || data.Status != 403 {
		t.Fatalf("auth 判定不正确: %+v", data)
	}
}

// 健康检查自己的请求不能被抓包记录污染（rpcid=AAAAA 是假的）。
func TestBootstrapHealthIsNotSniffed(t *testing.T) {
	start, err := dispatchScript(66, "sniffStart", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	output := runHarness(t, []string{BootstrapJS, start, healthScript(t, 67)})
	for _, message := range decodeMessages(t, output) {
		if message.Kind == "sniff" {
			t.Fatalf("健康检查不应产生抓包记录: %s", message.Entry)
		}
	}
}

// batchexecute 的挂载点由页面自己公布（WIZ_global_data.Im6cmf）。写死路径曾经
// 让所有请求 404，因为 Tasks 早已从 /_/TasksUi 迁走。
func TestBootstrapBatchPathFollowsMountPoint(t *testing.T) {
	script, err := dispatchScript(71, "batch", map[string]any{
		"rpcids": []string{"AbCdE"},
		"freqs":  []any{[]any{}},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	output := runHarness(t, []string{BootstrapJS, script})
	if len(output.Requests) != 1 {
		t.Fatalf("应发出 1 个请求，实到 %d", len(output.Requests))
	}
	if got := output.Requests[0].URL; !strings.HasPrefix(got, "/_/TasksWebEmbedUi/data/batchexecute?") {
		t.Fatalf("默认 endpoint 应取自 Im6cmf，实到: %s", got)
	}

	renamed := runHarnessSpec(t, []string{BootstrapJS, script}, map[string]any{"mount": "/_/RenamedUi/"})
	if got := renamed.Requests[0].URL; !strings.HasPrefix(got, "/_/RenamedUi/data/batchexecute?") {
		t.Fatalf("endpoint 应跟随页面公布的挂载点，实到: %s", got)
	}
}

// 健康检查也必须走同一个 endpoint，否则永远报 http404。
func TestBootstrapHealthUsesMountPoint(t *testing.T) {
	output := runHarness(t, []string{BootstrapJS, healthScript(t, 72)})
	health := decodeHealth(t, decodeMessages(t, output), 72)
	if health.State != "ok" || health.Status != 200 {
		t.Fatalf("健康状态不正确: %+v", health)
	}
	if len(output.Requests) != 1 {
		t.Fatalf("应发出 1 个请求，实到 %d", len(output.Requests))
	}
	if got := output.Requests[0].URL; !strings.HasPrefix(got, "/_/TasksWebEmbedUi/data/batchexecute?") {
		t.Fatalf("健康检查 endpoint 不正确: %s", got)
	}
}

// 刷新后 bootstrap 重新注入，必须抢在页面自己的请求之前恢复抓包，否则初始化
// RPC（也就是最需要的 rpcid）全都抓不到。
func TestBootstrapRecorderSelfArmsAfterReload(t *testing.T) {
	script, err := dispatchScript(73, "batch", map[string]any{
		"rpcids": []string{"AbCdE"},
		"freqs":  []any{[]any{}},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	off := runHarnessSpec(t, []string{BootstrapJS, script}, nil)
	if len(off.Storage) != 0 {
		t.Fatalf("默认不应开启抓包: %v", off.Storage)
	}
	for _, message := range decodeMessages(t, off) {
		if message.Kind == "sniff" {
			t.Fatalf("默认不应产生抓包记录: %s", message.Entry)
		}
	}

	on := runHarnessSpec(t, []string{BootstrapJS, script}, map[string]any{"storage": map[string]string{"gtasks.sniff": "1"}})
	sniffed := 0
	for _, message := range decodeMessages(t, on) {
		if message.Kind == "sniff" {
			sniffed++
		}
	}
	if sniffed == 0 {
		t.Fatal("localStorage 里开着抓包时应自动恢复记录")
	}
}

// sniffStart/sniffStop 要写回 localStorage，刷新后才能延续。
func TestBootstrapSniffTogglePersists(t *testing.T) {
	start, err := dispatchScript(74, "sniffStart", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	stop, err := dispatchScript(75, "sniffStop", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	on := runHarness(t, []string{BootstrapJS, start})
	if on.Storage["gtasks.sniff"] != "1" {
		t.Fatalf("sniffStart 应持久化开关，实到: %v", on.Storage)
	}
	off := runHarness(t, []string{BootstrapJS, start, stop})
	if _, exists := off.Storage["gtasks.sniff"]; exists {
		t.Fatalf("sniffStop 应清除开关，实到: %v", off.Storage)
	}
}

// HTTP 4xx/5xx 也要把载荷交回 Go：只回一句 "HTTP 404" 会让调用方无法区分
// endpoint 写错、会话失效还是被限流。
func TestBootstrapBatchKeepsPayloadOnHTTPError(t *testing.T) {
	script, err := dispatchScript(76, "batch", map[string]any{
		"rpcids": []string{"AbCdE"},
		"freqs":  []any{[]any{}},
	})
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}
	output := runHarnessSpec(t, []string{BootstrapJS, script}, map[string]any{
		"fetchStatus": 404,
		"fetchBody":   "<html>404</html>",
	})
	result := findResult(t, decodeMessages(t, output), 76)
	if !result.OK {
		t.Fatalf("拿到 HTTP 响应就算成功，错误交给调用方判定: %s", result.Error)
	}
	var data struct {
		Status int    `json:"status"`
		Auth   string `json:"auth"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("解析 batch 结果: %v", err)
	}
	if data.Status != 404 || data.Auth != "http404" || data.Path == "" {
		t.Fatalf("应保留状态码与路径: %+v", data)
	}
}

func TestBootstrapRejectsUnknownMethod(t *testing.T) {
	script, err := dispatchScript(33, "doesNotExist", nil)
	if err != nil {
		t.Fatalf("dispatchScript: %v", err)
	}

	output := runHarness(t, []string{BootstrapJS, script})
	result := findResult(t, decodeMessages(t, output), 33)
	if result.OK {
		t.Fatal("未知方法不应返回 ok")
	}
	if !strings.Contains(result.Error, "doesNotExist") {
		t.Fatalf("错误信息应包含方法名，实到: %s", result.Error)
	}
}

package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"gtasks/internal/batchexec"
)

// recordingClient 是一次性的假 batchexecute 客户端：按顺序回放预置结果，并把每
// 次请求的 args 原样记下来，供断言报文结构用。
type recordingClient struct {
	results []*batchexec.BatchResult
	errors  []error
	calls   []any
	session *batchexec.Session
}

func (c *recordingClient) Batch(_ context.Context, calls []batchexec.Call, _ batchexec.Options) (*batchexec.BatchResult, error) {
	index := len(c.calls)
	c.calls = append(c.calls, calls[0].Args)
	if index < len(c.results) {
		return c.results[index], c.errors[index]
	}
	return syncOK(int64(9000+index), 0), nil
}

func (c *recordingClient) CachedSession() *batchexec.Session { return c.session }

// syncOK 造一个成功响应：尾部带着下一次请求必须沿用的同步时间戳。
func syncOK(seconds, nanos int64) *batchexec.BatchResult {
	data := fmt.Sprintf(`[[],null,[[%d,%d]]]`, seconds, nanos)
	return &batchexec.BatchResult{
		Status: 200,
		Auth:   "ok",
		Bl:     "boq_test",
		Responses: []batchexec.Response{{
			RpcID: syncRPCID,
			Data:  json.RawMessage(data),
			Raw:   data,
		}},
	}
}

// syncFail 造一个被同步通道拒绝的响应，错误码放在 wrb.fr 帧的错误槽里。
func syncFail(code int64) *batchexec.BatchResult {
	payload := fmt.Sprintf(`[%d]`, code)
	return &batchexec.BatchResult{
		Status: 200,
		Auth:   "ok",
		Responses: []batchexec.Response{{
			RpcID: syncRPCID,
			Error: json.RawMessage(payload),
		}},
	}
}

type fakeSnapshots struct {
	snapshot *Snapshot
	err      error
	calls    int
}

func (f *fakeSnapshots) Snapshot(_ context.Context) (*Snapshot, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.snapshot, nil
}

func newTestWriter(t *testing.T, client *recordingClient) (*SyncWriter, *fakeSnapshots) {
	t.Helper()
	snapshots := &fakeSnapshots{snapshot: &Snapshot{
		Account:    "123456789012345678901",
		ServerTime: Timestamp{Seconds: 1000, Nanos: 5},
		ListOrder:  []string{"~default", "list-a", "list-b"},
	}}
	return NewSyncWriter(client, snapshots), snapshots
}

// argsOf 拆出一次请求的 args 数组。
func argsOf(t *testing.T, call any) []any {
	t.Helper()
	args, ok := call.([]any)
	if !ok {
		t.Fatalf("args 类型是 %T，期望 []any", call)
	}
	return args
}

// syncStateOf 拆出 args 里的同步状态段：轮询是 [[时间戳]]，写入是
// [[时间戳], null, 事件组]。
func syncStateOf(t *testing.T, call any) []any {
	t.Helper()
	head, ok := argsOf(t, call)[0].([]any)
	if !ok || len(head) < 2 {
		t.Fatalf("args[0] 结构不对：%v", argsOf(t, call)[0])
	}
	state, ok := head[1].([]any)
	if !ok {
		t.Fatalf("同步状态段不是数组：%v", head[1])
	}
	return state
}

// stampOf 取出请求携带的同步时间戳：同步状态段的第 0 位就是 [秒,纳秒]。
func stampOf(t *testing.T, call any) []any {
	t.Helper()
	state := syncStateOf(t, call)
	stamp, ok := state[0].([]any)
	if !ok || len(stamp) != 2 {
		t.Fatalf("同步状态段第 0 位不是 [秒,纳秒]：%v", state[0])
	}
	return stamp
}

// eventsOf 取出写入请求里的事件数组。
func eventsOf(t *testing.T, call any) []any {
	t.Helper()
	state := syncStateOf(t, call)
	if len(state) < 3 {
		t.Fatalf("这不是一个写入请求：%v", state)
	}
	groups, ok := state[2].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("事件组结构不对：%v", state[2])
	}
	group, ok := groups[0].([]any)
	if !ok || len(group) == 0 {
		t.Fatalf("事件组元素结构不对：%v", groups[0])
	}
	events, ok := group[0].([]any)
	if !ok {
		t.Fatalf("事件数组结构不对：%v", group[0])
	}
	return events
}

// eventOf 取出唯一的事件，多于一个就报错。
func eventOf(t *testing.T, call any) []any {
	t.Helper()
	events := eventsOf(t, call)
	if len(events) != 1 {
		t.Fatalf("事件数 = %d, 期望 1", len(events))
	}
	event, ok := events[0].([]any)
	if !ok {
		t.Fatalf("事件不是数组：%v", events[0])
	}
	return event
}

func jsonOf(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(body)
}

func TestFieldPatchRendersDenseValueArray(t *testing.T) {
	cases := []struct {
		name    string
		numbers []int
		values  map[int]any
		want    string
	}{
		{"标题", []int{fieldTaskTitle}, map[int]any{fieldTaskTitle: "买菜"}, `[[null,[2]],[null,"买菜"]]`},
		{"备注", []int{fieldTaskNotes}, map[int]any{fieldTaskNotes: "两行\n备注"}, `[[null,[3]],[null,null,"两行\n备注"]]`},
		{"清空备注", []int{fieldTaskNotes}, map[int]any{}, `[[null,[3]],[]]`},
		{
			"加星带时间",
			[]int{fieldTaskStarred, fieldTaskStarredAt},
			map[int]any{fieldTaskStarred: 1, fieldTaskStarredAt: []any{100, 200}},
			`[[null,[14,15]],[null,null,null,null,null,null,null,null,null,null,null,null,null,1,[100,200]]]`,
		},
		{
			"取消星标发空值数组",
			[]int{fieldTaskStarred, fieldTaskStarredAt},
			map[int]any{},
			`[[null,[14,15]],[]]`,
		},
		{
			"完成",
			[]int{fieldTaskCompleted, fieldTaskCompletedAt},
			map[int]any{fieldTaskCompleted: 1, fieldTaskCompletedAt: []any{7, 8}},
			`[[null,[1,5]],[1,null,null,null,[7,8]]]`,
		},
		{"删除", []int{fieldTaskDeleted}, map[int]any{fieldTaskDeleted: 1}, `[[null,[6]],[null,null,null,null,null,1]]`},
		{
			"截止日期多包一层",
			[]int{fieldTaskDue},
			map[int]any{fieldTaskDue: []any{[]any{1795251600, 0}}},
			`[[null,[18]],[null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,[[1795251600,0]]]]`,
		},
		{
			"新建任务的字段声明",
			[]int{fieldTaskDeleted, fieldTaskCompleted, fieldTaskTitle, fieldTaskCompletedAt, fieldTaskCreated},
			map[int]any{fieldTaskTitle: "新任务"},
			`[[null,[6,1,2,5,13]],[null,"新任务"]]`,
		},
		{"清单改名", []int{fieldListTitle}, map[int]any{fieldListTitle: "购物"}, `[[null,[1]],["购物"]]`},
		{
			"清单顺序 csv",
			[]int{fieldListOrderCSV},
			map[int]any{fieldListOrderCSV: "~default,a,b"},
			`[[null,[3]],[null,null,"~default,a,b"]]`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := jsonOf(t, fieldPatch(testCase.numbers, testCase.values))
			if got != testCase.want {
				t.Fatalf("fieldPatch = %s, 期望 %s", got, testCase.want)
			}
		})
	}
}

func TestSyncStampChainsFromEveryResponse(t *testing.T) {
	client := &recordingClient{
		results: []*batchexec.BatchResult{syncOK(2000, 7), syncOK(3000, 9), syncOK(4000, 1)},
		errors:  []error{nil, nil, nil},
	}
	writer, snapshots := newTestWriter(t, client)
	ctx := context.Background()

	if _, err := writer.SetTitle(ctx, "task-1", "第一个标题"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("调用次数 = %d, 期望 2（一次轮询 + 一次写入）", len(client.calls))
	}
	if snapshots.calls != 1 {
		t.Fatalf("快照读取次数 = %d, 期望 1", snapshots.calls)
	}
	// 第一次轮询用快照里的服务端时间当起点。
	if got := jsonOf(t, stampOf(t, client.calls[0])); got != "[1000,5]" {
		t.Fatalf("首次轮询时间戳 = %s, 期望 [1000,5]", got)
	}
	// 写入必须沿用轮询响应尾部的新时间戳。
	if got := jsonOf(t, stampOf(t, client.calls[1])); got != "[2000,7]" {
		t.Fatalf("写入携带的时间戳 = %s, 期望 [2000,7]", got)
	}

	if _, err := writer.SetTitle(ctx, "task-1", "第二个标题"); err != nil {
		t.Fatalf("第二次 SetTitle: %v", err)
	}
	if snapshots.calls != 1 {
		t.Fatalf("第二次写入不该再读快照，实到 %d 次", snapshots.calls)
	}
	if got := jsonOf(t, stampOf(t, client.calls[2])); got != "[3000,9]" {
		t.Fatalf("第三轮时间戳 = %s, 期望 [3000,9]（来自上一次写入的响应）", got)
	}
}

func TestSyncRequestCarriesAccountAndBuildLabel(t *testing.T) {
	client := &recordingClient{session: &batchexec.Session{Bl: "boq_from_page"}}
	writer, _ := newTestWriter(t, client)

	if _, err := writer.DeleteTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	args := argsOf(t, client.calls[0])
	if len(args) != 6 {
		t.Fatalf("args 长度 = %d, 期望 6", len(args))
	}
	if got := jsonOf(t, args[2]); got != `[null,["123456789012345678901"]]` {
		t.Fatalf("账户段 = %s", got)
	}
	if got := jsonOf(t, args[0]); got != `[null,[[1000,5]]]` {
		t.Fatalf("轮询状态段 = %s, 期望 [null,[[1000,5]]]（同步状态 = [[秒,纳秒]]）", got)
	}
	if got := buildLabelOf(t, args); got != "boq_from_page" {
		t.Fatalf("构建标签 = %q, 期望页面上报的 boq_from_page", got)
	}
	// 响应里带着页面真正用于请求 URL 的构建标签，之后的报文改用它，这样 Google
	// 换版本后写入层能自己跟上。
	if got := buildLabelOf(t, argsOf(t, client.calls[1])); got != "boq_test" {
		t.Fatalf("第二次请求的构建标签 = %q, 期望响应里的 boq_test", got)
	}
	if got := jsonOf(t, args[5]); got != "[]" {
		t.Fatalf("args 末段 = %s, 期望 []", got)
	}
}

func TestBuildLabelFallsBackWhenPageHasNotReported(t *testing.T) {
	client := &recordingClient{}
	writer, _ := newTestWriter(t, client)

	if _, err := writer.DeleteTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if got := buildLabelOf(t, argsOf(t, client.calls[0])); got != fallbackBuildLabel {
		t.Fatalf("构建标签 = %q, 期望兜底值 %q", got, fallbackBuildLabel)
	}
}

// buildLabelOf 取出 args 里第 5 段的构建标签。
func buildLabelOf(t *testing.T, args []any) string {
	t.Helper()
	section, ok := args[4].([]any)
	if !ok || len(section) < 2 {
		t.Fatalf("构建信息段结构不对：%v", args[4])
	}
	inner, ok := section[1].([]any)
	if !ok || len(inner) < 3 {
		t.Fatalf("构建信息内层结构不对：%v", section[1])
	}
	label, _ := inner[2].(string)
	return label
}

func TestCommitRetriesOnceOnStaleStamp(t *testing.T) {
	client := &recordingClient{
		results: []*batchexec.BatchResult{syncOK(2000, 0), syncFail(errCodeStale), syncOK(3000, 0), syncOK(4000, 0)},
		errors:  []error{nil, nil, nil, nil},
	}
	writer, _ := newTestWriter(t, client)

	if _, err := writer.SetStarred(context.Background(), "task-1", true); err != nil {
		t.Fatalf("时间戳过期后应自动重试成功，实到错误: %v", err)
	}
	if len(client.calls) != 4 {
		t.Fatalf("调用次数 = %d, 期望 4（轮询、失败、重新轮询、重试）", len(client.calls))
	}
	if got := jsonOf(t, stampOf(t, client.calls[3])); got != "[3000,0]" {
		t.Fatalf("重试携带的时间戳 = %s, 期望重新轮询得到的 [3000,0]", got)
	}
}

func TestCommitDoesNotRetryOtherCodes(t *testing.T) {
	client := &recordingClient{
		results: []*batchexec.BatchResult{syncOK(2000, 0), syncFail(13)},
		errors:  []error{nil, nil},
	}
	writer, _ := newTestWriter(t, client)

	_, err := writer.SetTitle(context.Background(), "task-1", "标题")
	if err == nil {
		t.Fatal("错误码 13 应该原样报错")
	}
	if len(client.calls) != 2 {
		t.Fatalf("调用次数 = %d, 期望 2（不重试）", len(client.calls))
	}
	var syncErr *SyncError
	if !asSyncError(err, &syncErr) || syncErr.Code != 13 {
		t.Fatalf("错误 = %v, 期望 *SyncError 且错误码 13", err)
	}
	if !strings.Contains(err.Error(), "13") {
		t.Fatalf("错误信息里没有错误码: %v", err)
	}
}

func asSyncError(err error, target **SyncError) bool {
	syncErr, ok := err.(*SyncError)
	if ok {
		*target = syncErr
	}
	return ok
}

func TestSessionFailureIsReported(t *testing.T) {
	result := syncOK(2000, 0)
	result.Auth = "unauthorized"
	client := &recordingClient{results: []*batchexec.BatchResult{result}, errors: []error{nil}}
	writer, _ := newTestWriter(t, client)

	_, err := writer.SetTitle(context.Background(), "task-1", "标题")
	if err == nil || !strings.Contains(err.Error(), "会话已失效") {
		t.Fatalf("错误 = %v, 期望提示会话失效", err)
	}
}

func TestPatchTaskEvents(t *testing.T) {
	due := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	cases := []struct {
		name  string
		run   func(*SyncWriter) (*batchexec.BatchResult, error)
		patch string
	}{
		{"改标题", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetTitle(context.Background(), "task-1", "新标题")
		}, `[[null,[2]],[null,"新标题"]]`},
		{"改备注", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetNotes(context.Background(), "task-1", "备注内容")
		}, `[[null,[3]],[null,null,"备注内容"]]`},
		{"清空备注", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetNotes(context.Background(), "task-1", "")
		}, `[[null,[3]],[]]`},
		{"加星", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetStarred(context.Background(), "task-1", true)
		}, ""},
		{"取消星标", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetStarred(context.Background(), "task-1", false)
		}, `[[null,[14,15]],[]]`},
		{"完成", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetCompleted(context.Background(), "task-1", true)
		}, ""},
		{"取消完成", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetCompleted(context.Background(), "task-1", false)
		}, `[[null,[1,5]],[]]`},
		{"设置截止日期", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.SetDue(context.Background(), "task-1", due)
		}, fmt.Sprintf(`[[null,[18]],[null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,[[%d,%d]]]]`, due.Unix(), due.Nanosecond())},
		{"清除截止日期", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.ClearDue(context.Background(), "task-1")
		}, `[[null,[18]],[]]`},
		{"删除任务", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.DeleteTask(context.Background(), "task-1")
		}, `[[null,[6]],[null,null,null,null,null,1]]`},
		{"恢复任务", func(w *SyncWriter) (*batchexec.BatchResult, error) {
			return w.RestoreTask(context.Background(), "task-1")
		}, `[[null,[6]],[]]`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &recordingClient{}
			writer, _ := newTestWriter(t, client)
			if _, err := testCase.run(writer); err != nil {
				t.Fatalf("执行失败: %v", err)
			}
			if len(client.calls) != 2 {
				t.Fatalf("调用次数 = %d, 期望 2", len(client.calls))
			}
			event := eventOf(t, client.calls[1])
			if event[1] != entityTask {
				t.Fatalf("实体类型 = %v, 期望 %d", event[1], entityTask)
			}
			if event[2] != "task-1" {
				t.Fatalf("实体 id = %v, 期望 task-1", event[2])
			}
			if got := jsonOf(t, event[3]); got != "[1]" {
				t.Fatalf("路径 = %s, 期望 [1]", got)
			}
			if event[25] != 1 {
				t.Fatalf("槽 25 = %v, 期望 1", event[25])
			}
			if id, ok := event[0].(string); !ok || len(id) != 16 {
				t.Fatalf("事件 id = %v, 期望 16 位字符串", event[0])
			}
			if testCase.patch == "" {
				// 带时间戳的 patch 只校验形状：字段号列表 + 非空值数组。
				raw := jsonOf(t, event[6])
				if !strings.HasPrefix(raw, `[[null,[`) || strings.HasSuffix(raw, `,[]]`) {
					t.Fatalf("patch = %s, 期望带值的字段 patch", raw)
				}
				return
			}
			if got := jsonOf(t, event[6]); got != testCase.patch {
				t.Fatalf("patch = %s, 期望 %s", got, testCase.patch)
			}
		})
	}
}

func TestCreateTaskSendsFourEvents(t *testing.T) {
	client := &recordingClient{}
	writer, _ := newTestWriter(t, client)

	if _, err := writer.CreateTask(context.Background(), "list-a", "  买牛奶  "); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	events := eventsOf(t, client.calls[1])
	if len(events) != 4 {
		t.Fatalf("事件数 = %d, 期望 4", len(events))
	}

	taskID, _ := events[0].([]any)[2].(string)
	if len(taskID) != 16 {
		t.Fatalf("客户端生成的任务 id = %q, 期望 16 位", taskID)
	}

	// 事件一：建实体，带标题。
	detail := events[0].([]any)
	if len(detail) != eventSlots-1 {
		t.Fatalf("建实体事件长度 = %d, 期望 %d（网页端不发尾部槽位）", len(detail), eventSlots-1)
	}
	if detail[1] != entityTask || detail[25] != 1 {
		t.Fatalf("建实体事件类型/槽 25 = %v/%v", detail[1], detail[25])
	}
	if got := jsonOf(t, detail[6]); got != `[[null,[6,1,2,5,13]],[null,"买牛奶"]]` {
		t.Fatalf("标题 patch = %s", got)
	}

	// 事件二：顺序占位。
	order := events[1].([]any)
	if order[2] != taskID || jsonOf(t, order[3]) != "[1,1]" {
		t.Fatalf("顺序占位事件 = %v / %v", order[2], order[3])
	}
	if got := jsonOf(t, order[11]); got != "[[],[]]" {
		t.Fatalf("槽 11 = %s, 期望 [[],[]]", got)
	}
	if order[26] != 1 {
		t.Fatalf("组内序号 = %v, 期望 1", order[26])
	}

	// 事件三：插入清单。
	insert := events[2].([]any)
	if insert[1] != entityTaskList || insert[2] != "list-a" {
		t.Fatalf("插入事件 = %v / %v", insert[1], insert[2])
	}
	slot8, _ := insert[8].([]any)
	if len(slot8) != 7 || slot8[0] != taskID || slot8[6] != 1 {
		t.Fatalf("槽 8 = %v, 期望 [任务id,null,null,null,null,null,1]", insert[8])
	}
	if insert[26] != 2 {
		t.Fatalf("组内序号 = %v, 期望 2", insert[26])
	}

	// 事件四：绑定所属清单。
	bind := events[3].([]any)
	if bind[2] != taskID || jsonOf(t, bind[3]) != "[1,2]" {
		t.Fatalf("绑定事件 = %v / %v", bind[2], bind[3])
	}
	if got := jsonOf(t, bind[10]); got != `[null,"list-a"]` {
		t.Fatalf("槽 10 = %s, 期望 [null,\"list-a\"]", got)
	}
	if bind[26] != 3 {
		t.Fatalf("组内序号 = %v, 期望 3", bind[26])
	}
}

func TestCreateTaskRejectsEmptyInput(t *testing.T) {
	client := &recordingClient{}
	writer, _ := newTestWriter(t, client)
	ctx := context.Background()

	if _, err := writer.CreateTask(ctx, "", "标题"); err == nil {
		t.Fatal("缺清单 id 应该报错")
	}
	if _, err := writer.CreateTask(ctx, "list-a", "   "); err == nil {
		t.Fatal("空标题应该报错")
	}
	if len(client.calls) != 0 {
		t.Fatalf("参数不合法时不该发请求，实到 %d 次", len(client.calls))
	}
}

func TestCreateTaskListRewritesOrderCSV(t *testing.T) {
	client := &recordingClient{}
	writer, snapshots := newTestWriter(t, client)

	if _, err := writer.CreateTaskList(context.Background(), "购物"); err != nil {
		t.Fatalf("CreateTaskList: %v", err)
	}
	// 顺序 csv 必须是当下的，所以要重新读一次快照（prepare 那次是初始化）。
	if snapshots.calls != 2 {
		t.Fatalf("快照读取次数 = %d, 期望 2", snapshots.calls)
	}
	events := eventsOf(t, client.calls[1])
	if len(events) != 2 {
		t.Fatalf("事件数 = %d, 期望 2", len(events))
	}

	list := events[0].([]any)
	if list[1] != entityTaskList {
		t.Fatalf("第一个事件类型 = %v, 期望 %d", list[1], entityTaskList)
	}
	if got := jsonOf(t, list[7]); got != `[[null,[1]],["购物"]]` {
		t.Fatalf("清单 patch = %s", got)
	}
	listID, _ := list[2].(string)
	if len(listID) != 16 {
		t.Fatalf("新清单 id = %q, 期望 16 位", listID)
	}

	reorder := events[1].([]any)
	if reorder[1] != entityListOrder || reorder[2] != nil {
		t.Fatalf("顺序事件 = %v / %v, 期望类型 %d 且没有实体 id", reorder[1], reorder[2], entityListOrder)
	}
	want := fmt.Sprintf(`[[null,[3]],[null,null,"~default,list-a,list-b,%s"]]`, listID)
	if got := jsonOf(t, reorder[5]); got != want {
		t.Fatalf("顺序 patch = %s, 期望 %s", got, want)
	}
}

func TestRenameTaskListPatchesListEntity(t *testing.T) {
	client := &recordingClient{}
	writer, _ := newTestWriter(t, client)

	if _, err := writer.RenameTaskList(context.Background(), "list-a", "工作"); err != nil {
		t.Fatalf("RenameTaskList: %v", err)
	}
	event := eventOf(t, client.calls[1])
	if event[1] != entityTaskList || event[2] != "list-a" {
		t.Fatalf("事件 = %v / %v", event[1], event[2])
	}
	if got := jsonOf(t, event[7]); got != `[[null,[1]],["工作"]]` {
		t.Fatalf("清单 patch = %s", got)
	}
	if event[6] != nil {
		t.Fatalf("清单事件不该带任务 patch：%v", event[6])
	}
}

func TestWriterNeedsAccountFromSnapshot(t *testing.T) {
	client := &recordingClient{}
	snapshots := &fakeSnapshots{snapshot: &Snapshot{ServerTime: Timestamp{Seconds: 1}}}
	writer := NewSyncWriter(client, snapshots)

	_, err := writer.SetTitle(context.Background(), "task-1", "标题")
	if err == nil || !strings.Contains(err.Error(), "账户 id") {
		t.Fatalf("错误 = %v, 期望提示拿不到账户 id", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("缺账户 id 时不该发请求，实到 %d 次", len(client.calls))
	}
}

func TestParseDue(t *testing.T) {
	t.Run("整天按 UTC 零点", func(t *testing.T) {
		due, keep, err := parseDue(map[string]any{"date": "2026-11-21"})
		if err != nil || !keep {
			t.Fatalf("keep = %v, err = %v", keep, err)
		}
		if due.Unix() != 1795219200 || due.Location() != time.UTC {
			t.Fatalf("due = %s (%d), 期望 2026-11-21T00:00:00Z", due, due.Unix())
		}
	})

	t.Run("带时分按本机时区", func(t *testing.T) {
		due, keep, err := parseDue(map[string]any{"date": "2026-11-21", "time": "09:00"})
		if err != nil || !keep {
			t.Fatalf("keep = %v, err = %v", keep, err)
		}
		want := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
		if !due.Equal(want) {
			t.Fatalf("due = %s, 期望 %s", due, want)
		}
	})

	t.Run("带秒", func(t *testing.T) {
		due, keep, err := parseDue(map[string]any{"date": "2026-11-21", "time": "09:00:30"})
		if err != nil || !keep {
			t.Fatalf("keep = %v, err = %v", keep, err)
		}
		if !due.Equal(time.Date(2026, 11, 21, 9, 0, 30, 0, time.Local)) {
			t.Fatalf("due = %s", due)
		}
	})

	t.Run("空日期视为清除", func(t *testing.T) {
		for _, value := range []any{nil, "", map[string]any{}, map[string]any{"date": "", "time": "09:00"}} {
			_, keep, err := parseDue(value)
			if err != nil || keep {
				t.Fatalf("%v: keep = %v, err = %v", value, keep, err)
			}
		}
	})

	t.Run("字符串日期", func(t *testing.T) {
		due, keep, err := parseDue("2026-12-01")
		if err != nil || !keep {
			t.Fatalf("keep = %v, err = %v", keep, err)
		}
		if due.Format("2006-01-02") != "2026-12-01" {
			t.Fatalf("due = %s", due)
		}
	})

	t.Run("非法日期报错", func(t *testing.T) {
		if _, _, err := parseDue(map[string]any{"date": "11/21/2026"}); err == nil {
			t.Fatal("非法日期应该报错")
		}
		if _, _, err := parseDue(42); err == nil {
			t.Fatal("非法类型应该报错")
		}
	})
}

func TestRawDueReadsBackWhatWasWritten(t *testing.T) {
	if due := rawDue(json.RawMessage(`[[1795219200,0]]`)); due == nil || due.Date != "2026-11-21" || due.Time != "" {
		t.Fatalf("整天 due = %+v, 期望 2026-11-21 且没有时分", due)
	}

	instant := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	raw := fmt.Sprintf(`[[%d,%d]]`, instant.Unix(), instant.Nanosecond())
	due := rawDue(json.RawMessage(raw))
	if due == nil || due.Date != "2026-11-21" || due.Time != "09:00" {
		t.Fatalf("带时分 due = %+v, 期望 2026-11-21 09:00", due)
	}

	for _, empty := range []string{`null`, `[]`, `[null]`, `[[0,0]]`} {
		if due := rawDue(json.RawMessage(empty)); due != nil {
			t.Fatalf("%s 解析出 %+v, 期望 nil", empty, due)
		}
	}
}

func TestSyncStampAndErrorParsing(t *testing.T) {
	stamp, ok := syncStamp(json.RawMessage(`[[["x"]],null,[[1795147040,469701000]]]`))
	if !ok || stamp[0] != 1795147040 || stamp[1] != 469701000 {
		t.Fatalf("syncStamp = %v, %v", stamp, ok)
	}
	for _, payload := range []string{`null`, `[]`, `[[],null]`, `[[],null,[[]]]`, `[[],null,[[0,0]]]`} {
		if _, ok := syncStamp(json.RawMessage(payload)); ok {
			t.Fatalf("%s 不该解析出时间戳", payload)
		}
	}

	err := syncErrorOf(json.RawMessage(`[3]`)).(*SyncError)
	if err.Code != 3 || !err.Stale() {
		t.Fatalf("错误码 = %d, stale = %v", err.Code, err.Stale())
	}
	internal := syncErrorOf(json.RawMessage(
		`[13,null,[["type.googleapis.com/google.internal.tasks.v1.ErrorInfo",[null,null,null,null,null,1]]]]`),
	).(*SyncError)
	if internal.Code != 13 || internal.Stale() || !strings.Contains(internal.Detail, "ErrorInfo") {
		t.Fatalf("内部错误 = %+v", internal)
	}
	if !strings.Contains(internal.Error(), "13") {
		t.Fatalf("错误信息 = %q", internal.Error())
	}
}

func TestRandomIDShape(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 200; index++ {
		id := randomID()
		if len(id) != 16 {
			t.Fatalf("id = %q, 期望 16 位", id)
		}
		if strings.ContainsAny(id, "=+/ ") {
			t.Fatalf("id = %q, 期望 base64url 字符集", id)
		}
		if seen[id] {
			t.Fatalf("id 重复：%q", id)
		}
		seen[id] = true
	}
}

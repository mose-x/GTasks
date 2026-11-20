package tasks

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gtasks/internal/batchexec"
)

// syncRPCID 是 Tasks 网页端唯一的写入通道。写操作没有各自独立的 rpcid：所有
// 变更都作为字段级 patch 事件塞进这一条双向同步流，响应再回带服务端自己的变更
// 和一个新的同步时间戳，下一次请求必须把它原样带回去。
const syncRPCID = "SOV0tb"

// syncSourcePath fills the source-path query parameter of the sync channel.
const syncSourcePath = "/tasks/"

// syncRegion is the fixed backend label the web client sends alongside the
// build label. It has never been observed to vary per account.
const syncRegion = "prod-00-us"

// fallbackBuildLabel is used until either the page or a response has reported a
// build label. A stale label is tolerated by the server, so this only has to be
// plausible for the very first request.
const fallbackBuildLabel = "boq_taskswebuiserver_20261115.15_p0"

// Entity kinds the sync channel distinguishes. They select which patch slot of
// an event is read.
const (
	entityListOrder = 2 // 账户级的清单顺序，单例，没有 id
	entityTask      = 3
	entityTaskList  = 4
)

// Field numbers of a task's detail message. They are 1-based, and a patch's
// value array is indexed by 字段号−1.
const (
	fieldTaskCompleted   = 1
	fieldTaskTitle       = 2
	fieldTaskNotes       = 3
	fieldTaskCompletedAt = 5
	fieldTaskDeleted     = 6
	fieldTaskStarred     = 14
	fieldTaskStarredAt   = 15
	fieldTaskDue         = 18
)

// fieldTaskCreated is only ever read back; writing it is refused.
const fieldTaskCreated = 13

// Field numbers of a task list and of the account level list order entity.
const (
	fieldListTitle    = 1
	fieldListOrderCSV = 3
)

// eventSlots is the width of one sync event. Only the slots listed in newEvent
// carry meaning; the rest stay null and the server ignores them.
const eventSlots = 28

// errCodeStale is the code the sync channel answers with when the timestamp a
// request carried has already been superseded. Polling once and retrying fixes
// it, which happens routinely because the web page polls the same channel.
const errCodeStale = 3

// SyncError is a refusal reported by the sync channel itself.
type SyncError struct {
	Code   int64
	Detail string
}

func (e *SyncError) Error() string {
	message := fmt.Sprintf("tasks: 同步通道返回错误码 %d", e.Code)
	switch e.Code {
	case errCodeStale:
		message = "tasks: 同步时间戳已过期（错误码 3）"
	case 13:
		message = "tasks: 同步通道内部错误（错误码 13），该字段可能不接受这种写法"
	}
	if e.Detail != "" {
		message += "：" + e.Detail
	}
	return message
}

// Stale reports whether retrying after a fresh poll can succeed.
func (e *SyncError) Stale() bool { return e.Code == errCodeStale }

// Snapshotter reads the dataset. *Service implements it, and the writer needs
// it for the account id and for the current list order.
type Snapshotter interface {
	Snapshot(ctx context.Context) (*Snapshot, error)
}

// SyncClient is the slice of the batchexecute client the writer uses. It is an
// interface so the protocol can be tested without a live Google page.
type SyncClient interface {
	Batch(ctx context.Context, calls []batchexec.Call, options batchexec.Options) (*batchexec.BatchResult, error)
	CachedSession() *batchexec.Session
}

// SyncWriter turns logical task operations into SOV0tb sync messages.
//
// Everything the web app can do to a task goes through this one channel, so the
// writer is the client's entire write path. It keeps no state beyond the last
// sync timestamp, the account id and the build label; the timestamp is chained
// from every response because a stale one is refused.
type SyncWriter struct {
	client    SyncClient
	snapshots Snapshotter

	mu      sync.Mutex
	stamp   []int64
	account string
	bl      string
}

// NewSyncWriter returns a writer that issues its calls through client.
func NewSyncWriter(client SyncClient, snapshots Snapshotter) *SyncWriter {
	return &SyncWriter{client: client, snapshots: snapshots}
}

// syncOps 是由 Go 直接合成同步报文实现的操作。它们的载荷是算出来的（字段级
// patch、链式时间戳、客户端生成的 id），没法用 operations.json 的静态模板表达，
// 所以走 Service.Execute 里的专用分支。
var syncOps = map[Operation]string{
	"createTask":      "四事件：建实体、登记顺序、插入清单、绑定清单",
	"updateTaskTitle": "任务 detail 字段 2",
	"updateTaskNotes": "任务 detail 字段 3",
	"setStarred":      "任务 detail 字段 14 + 15，取消星标发空值数组",
	"completeTask":    "任务 detail 字段 1 + 5，取消完成发空值数组",
	"uncompleteTask":  "同 completeTask",
	"setDueDateTime":  "任务 detail 字段 18，消息体 [[秒, 纳秒]]",
	"clearDue":        "任务 detail 字段 18 发空值数组",
	"deleteTask":      "任务 detail 字段 6 置 1，服务端自动维护清单顺序",
	"restoreTask":     "任务 detail 字段 6 发空值数组",
	"createTaskList":  "两事件：建清单 + 更新账户级顺序 csv",
	"renameTaskList":  "清单字段 1",
}

// IsSyncOp reports whether op is carried by the sync channel rather than by
// captured rpcid steps.
func IsSyncOp(op Operation) bool {
	_, ok := syncOps[op]
	return ok
}

// SyncOps lists the sync-carried operations in a stable order.
func SyncOps() []Operation {
	out := make([]Operation, 0, len(syncOps))
	for op := range syncOps {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SyncOpNotes describes how one sync operation is carried, for the workbench.
func SyncOpNotes(op Operation) string { return syncOps[op] }

/* --------------------------------------------------------------- 写入操作 */

// CreateTask adds a task to a list and returns the batch it was carried by. The
// id is generated here: the server accepts a client-minted task id, which is
// what the web app does too, so no extra round trip is needed to learn it.
func (w *SyncWriter) CreateTask(ctx context.Context, listID, title string) (*batchexec.BatchResult, error) {
	if listID == "" {
		return nil, errors.New("tasks: 新建任务需要清单 id")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("tasks: 任务标题不能为空")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.prepare(ctx); err != nil {
		return nil, err
	}

	taskID := randomID()
	stamp := clientStamp()
	// 网页端建一个任务发四个事件：先建任务实体（带标题），再登记它的顺序占位，
	// 然后把它插进清单，最后绑定所属清单。少任何一个都会在界面上留下半成品。
	detail := make([]any, eventSlots-1)
	detail[0] = randomID()
	detail[1] = entityTask
	detail[2] = taskID
	detail[3] = pathOf(1)
	detail[6] = fieldPatch(
		[]int{fieldTaskDeleted, fieldTaskCompleted, fieldTaskTitle, fieldTaskCompletedAt, fieldTaskCreated},
		map[int]any{fieldTaskTitle: title},
	)
	detail[9] = stamp
	detail[25] = 1

	order := newEvent(entityTask, taskID, pathOf(1, 1), stamp)
	order[11] = []any{[]any{}, []any{}}
	order[26] = 1
	order[27] = []any{1}

	insert := newEvent(entityTaskList, listID, pathOf(1, 1), stamp)
	insert[8] = []any{taskID, nil, nil, nil, nil, nil, 1}
	insert[26] = 2
	insert[27] = []any{1}

	bind := newEvent(entityTask, taskID, pathOf(1, 2), stamp)
	bind[10] = []any{nil, listID}
	bind[26] = 3
	bind[27] = []any{1, 1}

	return w.commit(ctx, []any{detail, order, insert, bind})
}

// SetTitle renames a task.
func (w *SyncWriter) SetTitle(ctx context.Context, taskID, title string) (*batchexec.BatchResult, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("tasks: 任务标题不能为空")
	}
	return w.patchTask(ctx, taskID, []int{fieldTaskTitle}, map[int]any{fieldTaskTitle: title})
}

// SetNotes replaces a task's notes. An empty string clears them.
func (w *SyncWriter) SetNotes(ctx context.Context, taskID, notes string) (*batchexec.BatchResult, error) {
	values := map[int]any{}
	if notes != "" {
		values[fieldTaskNotes] = notes
	}
	return w.patchTask(ctx, taskID, []int{fieldTaskNotes}, values)
}

// SetStarred sets or clears the star. Both fields have to travel together: the
// server keeps 星标时间 alongside the flag, and clearing is an empty value array.
func (w *SyncWriter) SetStarred(ctx context.Context, taskID string, starred bool) (*batchexec.BatchResult, error) {
	numbers := []int{fieldTaskStarred, fieldTaskStarredAt}
	values := map[int]any{}
	if starred {
		values[fieldTaskStarred] = 1
		values[fieldTaskStarredAt] = clientStamp()
	}
	return w.patchTask(ctx, taskID, numbers, values)
}

// SetCompleted checks or unchecks a task, mirroring the web app by writing the
// completion time together with the flag.
func (w *SyncWriter) SetCompleted(ctx context.Context, taskID string, completed bool) (*batchexec.BatchResult, error) {
	numbers := []int{fieldTaskCompleted, fieldTaskCompletedAt}
	values := map[int]any{}
	if completed {
		values[fieldTaskCompleted] = 1
		values[fieldTaskCompletedAt] = clientStamp()
	}
	return w.patchTask(ctx, taskID, numbers, values)
}

// SetDue stamps a task's due date. The value is a message holding one
// timestamp, so the pair has to be wrapped one level deeper than every other
// timestamp field.
func (w *SyncWriter) SetDue(ctx context.Context, taskID string, due time.Time) (*batchexec.BatchResult, error) {
	values := map[int]any{fieldTaskDue: []any{[]any{due.Unix(), int64(due.Nanosecond())}}}
	return w.patchTask(ctx, taskID, []int{fieldTaskDue}, values)
}

// ClearDue removes a task's due date.
func (w *SyncWriter) ClearDue(ctx context.Context, taskID string) (*batchexec.BatchResult, error) {
	return w.patchTask(ctx, taskID, []int{fieldTaskDue}, map[int]any{})
}

// DeleteTask moves a task to the trash. The server maintains the list order and
// writes the deletion time itself, so one field is enough.
func (w *SyncWriter) DeleteTask(ctx context.Context, taskID string) (*batchexec.BatchResult, error) {
	return w.patchTask(ctx, taskID, []int{fieldTaskDeleted}, map[int]any{fieldTaskDeleted: 1})
}

// RestoreTask undoes a deletion.
func (w *SyncWriter) RestoreTask(ctx context.Context, taskID string) (*batchexec.BatchResult, error) {
	return w.patchTask(ctx, taskID, []int{fieldTaskDeleted}, map[int]any{})
}

// RenameTaskList retitles a list.
func (w *SyncWriter) RenameTaskList(ctx context.Context, listID, title string) (*batchexec.BatchResult, error) {
	if listID == "" {
		return nil, errors.New("tasks: 重命名清单需要清单 id")
	}
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("tasks: 清单名称不能为空")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.prepare(ctx); err != nil {
		return nil, err
	}
	event := newEvent(entityTaskList, listID, pathOf(1), clientStamp())
	event[7] = fieldPatch([]int{fieldListTitle}, map[int]any{fieldListTitle: title})
	return w.commit(ctx, []any{event})
}

// CreateTaskList adds a list to the sidebar. Two events are required: the list
// itself, and the account level order entity whose comma separated value has to
// name every undeleted list exactly. The server rejects an order that does not
// match, so the current order is read back first instead of being cached.
func (w *SyncWriter) CreateTaskList(ctx context.Context, title string) (*batchexec.BatchResult, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("tasks: 清单名称不能为空")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.prepare(ctx); err != nil {
		return nil, err
	}
	snapshot, err := w.snapshots.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if len(snapshot.ListOrder) == 0 {
		return nil, errors.New("tasks: 快照里没有清单顺序，无法新建清单")
	}

	listID := randomID()
	order := strings.Join(append(append([]string{}, snapshot.ListOrder...), listID), ",")
	stamp := clientStamp()

	list := newEvent(entityTaskList, listID, pathOf(1), stamp)
	list[7] = fieldPatch([]int{fieldListTitle}, map[int]any{fieldListTitle: title})

	reorder := newEvent(entityListOrder, "", pathOf(1), stamp)
	reorder[5] = fieldPatch([]int{fieldListOrderCSV}, map[int]any{fieldListOrderCSV: order})
	reorder[26] = 1

	return w.commit(ctx, []any{list, reorder})
}

/* --------------------------------------------------------------- 报文构造 */

// patchTask writes one field level patch against a task's detail message.
func (w *SyncWriter) patchTask(ctx context.Context, taskID string, numbers []int, values map[int]any) (*batchexec.BatchResult, error) {
	if taskID == "" {
		return nil, errors.New("tasks: 缺少任务 id")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.prepare(ctx); err != nil {
		return nil, err
	}
	event := newEvent(entityTask, taskID, pathOf(1), clientStamp())
	event[6] = fieldPatch(numbers, values)
	return w.commit(ctx, []any{event})
}

// newEvent returns an empty event of the given entity kind. Slot meanings, all
// discovered from captures:
//
//	0 客户端事件 id      1 实体类型        2 实体 id      3 路径
//	5 清单顺序 patch     6 任务 patch      7 清单 patch   8 清单内插入位置
//	9 客户端时间戳      10 [null, 清单 id] 11 子任务占位  25 固定 1
//	26 组内序号         27 尾部（服务端不校验）
func newEvent(kind int, id string, path []any, stamp []any) []any {
	event := make([]any, eventSlots)
	event[0] = randomID()
	event[1] = kind
	if id != "" {
		event[2] = id
	}
	event[3] = path
	event[9] = stamp
	event[25] = 1
	event[27] = []any{nil, 1}
	return event
}

// pathOf renders an event path such as [1] or [1,2].
func pathOf(parts ...int) []any {
	path := make([]any, 0, len(parts))
	for _, part := range parts {
		path = append(path, part)
	}
	return path
}

// fieldPatch builds the patch form the sync channel expects:
//
//	[[null, [字段号…]], 值数组]
//
// The value array is dense and indexed by 字段号−1, with trailing empty slots
// trimmed. Writing 字段 2 is therefore [null, "标题"], and clearing 字段 14 和
// 15 is an empty array — that is how the web app unstars and unchecks.
func fieldPatch(numbers []int, values map[int]any) []any {
	highest := 0
	for _, number := range numbers {
		if number > highest {
			highest = number
		}
	}
	dense := make([]any, highest)
	for number, value := range values {
		if number >= 1 && number <= highest {
			dense[number-1] = value
		}
	}
	end := highest
	for end > 0 && dense[end-1] == nil {
		end--
	}
	listed := make([]any, 0, len(numbers))
	for _, number := range numbers {
		listed = append(listed, number)
	}
	return []any{[]any{nil, listed}, dense[:end]}
}

// clientStamp is the client side [秒, 纳秒] pair every event carries.
func clientStamp() []any {
	now := time.Now()
	return []any{now.Unix(), int64(now.Nanosecond())}
}

// randomID mints a 16 character base64url id, the shape Google uses for task
// and list ids as well as for per-event ids.
func randomID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand failing means the platform entropy source is broken; a
		// time derived id keeps the writer usable instead of erroring out.
		return base64.RawURLEncoding.EncodeToString(
			[]byte(fmt.Sprintf("%012d", time.Now().UnixNano()%1e12)),
		)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

/* ------------------------------------------------------------ 通道与状态 */

// prepare fills the account id, build label and first sync timestamp. The
// snapshot already carries the account and a server time, so no extra poll is
// needed before the first write.
func (w *SyncWriter) prepare(ctx context.Context) error {
	if w.bl == "" {
		if session := w.client.CachedSession(); session != nil {
			w.bl = session.Bl
		}
		if w.bl == "" {
			w.bl = fallbackBuildLabel
		}
	}
	if w.account != "" && w.stamp != nil {
		return nil
	}
	snapshot, err := w.snapshots.Snapshot(ctx)
	if err != nil {
		return err
	}
	if w.account == "" {
		w.account = snapshot.Account
	}
	if w.stamp == nil {
		w.stamp = []int64{snapshot.ServerTime.Seconds, snapshot.ServerTime.Nanos}
	}
	if w.account == "" {
		return errors.New("tasks: 快照里没有账户 id，无法写入（会话可能已失效）")
	}
	return nil
}

// commit sends one group of events. The timestamp is refreshed by a poll first
// because the page polls the same channel on its own timer, and a stale
// timestamp is refused; one retry after another poll covers the race.
func (w *SyncWriter) commit(ctx context.Context, events []any) (*batchexec.BatchResult, error) {
	var last *batchexec.BatchResult
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := w.poll(ctx); err != nil {
			return last, err
		}
		state := []any{w.stampValue(), nil, []any{[]any{events, []any{}}}}
		result, err := w.call(ctx, state)
		if result != nil {
			last = result
		}
		if err == nil {
			return result, nil
		}
		var syncErr *SyncError
		if !errors.As(err, &syncErr) || !syncErr.Stale() {
			return result, err
		}
	}
	return last, &SyncError{Code: errCodeStale, Detail: "重试后仍然被拒绝"}
}

// poll asks the channel for changes since the current timestamp. The response
// tail carries the timestamp every later request has to use. A poll's sync state
// is just the pair; a write appends the event groups after it.
func (w *SyncWriter) poll(ctx context.Context) (*batchexec.BatchResult, error) {
	return w.call(ctx, []any{w.stampValue()})
}

// stampValue renders the chained [秒, 纳秒] pair.
func (w *SyncWriter) stampValue() []any {
	if len(w.stamp) != 2 {
		now := time.Now()
		return []any{now.Unix(), int64(now.Nanosecond())}
	}
	return []any{w.stamp[0], w.stamp[1]}
}

// call issues one SOV0tb request and folds the response back into the writer's
// state: the new sync timestamp and, when the page has reported one, the build
// label.
func (w *SyncWriter) call(ctx context.Context, syncState []any) (*batchexec.BatchResult, error) {
	args := []any{
		[]any{nil, syncState},
		nil,
		[]any{nil, []any{w.account}},
		nil,
		[]any{randomID(), []any{nil, 4, w.bl, syncRegion, 3, 1, 2, nil, 1}},
		[]any{},
	}

	result, err := w.client.Batch(ctx, []batchexec.Call{{RpcID: syncRPCID, Args: args}},
		batchexec.Options{SourcePath: syncSourcePath})
	if result != nil {
		if result.Bl != "" {
			w.bl = result.Bl
		}
		for _, response := range result.Responses {
			if stamp, ok := syncStamp(response.Data); ok {
				w.stamp = stamp
			}
		}
	}
	if err != nil {
		return result, err
	}
	if batchexec.AuthBad(result.Auth) {
		return result, fmt.Errorf("tasks: 会话已失效（%s），请重新登录", result.Auth)
	}
	if result.Status != 0 && (result.Status < 200 || result.Status >= 300) {
		return result, fmt.Errorf("tasks: 同步通道收到 HTTP %d", result.Status)
	}
	if len(result.Responses) == 0 {
		return result, errors.New("tasks: 同步通道没有返回任何帧")
	}
	for _, response := range result.Responses {
		if len(response.Error) > 0 {
			return result, syncErrorOf(response.Error)
		}
	}
	return result, nil
}

// syncStamp reads the new sync timestamp out of a response payload, whose shape
// is [实体变更, null, [[秒, 纳秒]]].
func syncStamp(data json.RawMessage) ([]int64, bool) {
	root, err := decodeArray(data)
	if err != nil || len(root) < 3 {
		return nil, false
	}
	outer, err := decodeArray(at(root, 2))
	if err != nil || len(outer) == 0 {
		return nil, false
	}
	pair, err := decodeArray(at(outer, 0))
	if err != nil || len(pair) < 2 {
		return nil, false
	}
	seconds, nanos := rawInt(at(pair, 0)), rawInt(at(pair, 1))
	if seconds == 0 && nanos == 0 {
		return nil, false
	}
	return []int64{seconds, nanos}, true
}

// syncErrorOf turns the channel's error slot into a SyncError. The code is the
// first element; slot 2 optionally carries a google.internal.tasks.v1.ErrorInfo
// payload worth showing verbatim.
func syncErrorOf(raw json.RawMessage) error {
	fields, err := decodeArray(raw)
	if err != nil || len(fields) == 0 {
		return &SyncError{Code: -1, Detail: truncate(string(raw), 160)}
	}
	syncErr := &SyncError{Code: rawInt(at(fields, 0))}
	if len(fields) > 2 && !isNull(at(fields, 2)) {
		syncErr.Detail = truncate(string(fields[2]), 160)
	}
	return syncErr
}

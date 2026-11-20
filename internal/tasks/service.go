package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gtasks/internal/batchexec"
)

// Service executes registry-defined operations against the page bridge.
type Service struct {
	registry *Registry
	client   *batchexec.Client
	sync     *SyncWriter
}

// NewService wires a registry to a batchexecute client.
func NewService(registry *Registry, client *batchexec.Client) *Service {
	service := &Service{registry: registry, client: client}
	service.sync = NewSyncWriter(client, service)
	return service
}

// Registry exposes the underlying operation table.
func (s *Service) Registry() *Registry { return s.registry }

// Sync exposes the write channel, for callers that already hold typed values
// instead of the frontend's parameter map.
func (s *Service) Sync() *SyncWriter { return s.sync }

// Execute runs one operation, either over the sync channel or, for the read
// side and anything still described by captured rpcids, as a rendered batch.
func (s *Service) Execute(ctx context.Context, op Operation, params map[string]any) (*batchexec.BatchResult, error) {
	if s.sync != nil && IsSyncOp(op) {
		return s.runSyncOp(ctx, op, params)
	}
	calls, err := s.registry.Render(op, params)
	if err != nil {
		return nil, err
	}
	result, err := s.client.Batch(ctx, calls, batchexec.Options{})
	if result != nil && result.Status != 0 && (result.Status < 200 || result.Status >= 300) {
		return result, fmt.Errorf("tasks: %s 收到 HTTP %d（%s）", op, result.Status, result.Auth)
	}
	if err != nil {
		return result, fmt.Errorf("tasks: 执行 %s 失败: %w", op, err)
	}
	for _, response := range result.Responses {
		if len(response.Error) > 0 {
			return result, fmt.Errorf("tasks: %s 的 %s 返回错误: %s", op, response.RpcID, string(response.Error))
		}
	}
	return result, nil
}

// Snapshot reads the whole dataset: every list plus every task the server is
// willing to return in one shot. The web app renders 所有任务, 已加星标, each
// individual list and the 已完成 sections from this single payload without
// issuing another request, so it is the client's entire read path.
func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	result, err := s.Execute(ctx, OpSnapshot, nil)
	if err != nil {
		return nil, err
	}
	for _, response := range result.Responses {
		if len(response.Data) == 0 {
			continue
		}
		snapshot, err := ParseSnapshot(response.Data)
		if err != nil {
			return nil, err
		}
		return snapshot, nil
	}
	return nil, errors.New("tasks: 快照响应里没有可解析的数据（会话可能已失效）")
}

// SnapshotJSON is Snapshot rendered for the frontend.
func (s *Service) SnapshotJSON(ctx context.Context) (json.RawMessage, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("tasks: 序列化快照失败: %w", err)
	}
	return body, nil
}

/* --------------------------------------------------- 同步通道上的操作分派 */

// runSyncOp maps the frontend's parameter names onto the typed writer calls.
// Every one of these operations is carried by SOV0tb, whose payload has to be
// computed — field level patches, a chained sync timestamp and client-minted
// ids — so the static templates of the registry cannot express them.
func (s *Service) runSyncOp(ctx context.Context, op Operation, params map[string]any) (*batchexec.BatchResult, error) {
	taskID := paramString(params, "TaskID", "taskId", "id")
	listID := paramString(params, "TaskListID", "taskListId", "listId", "ListID")

	switch op {
	case "createTask":
		if parent := paramString(params, "ParentID", "parentId", "parent"); parent != "" {
			return nil, errors.New("tasks: 子任务的同步报文还没破解，目前只能在清单顶层新建任务")
		}
		return s.sync.CreateTask(ctx, listID, paramString(params, "Title", "title"))

	case "updateTaskTitle":
		return s.sync.SetTitle(ctx, taskID, paramString(params, "title", "Title"))

	case "updateTaskNotes":
		return s.sync.SetNotes(ctx, taskID, paramString(params, "notes", "Notes"))

	case "setStarred":
		starred, ok := paramBool(params, "Starred", "starred")
		if !ok {
			return nil, errors.New("tasks: setStarred 需要布尔参数 Starred")
		}
		return s.sync.SetStarred(ctx, taskID, starred)

	case "completeTask", "uncompleteTask":
		// 前端两个 op 都会带上目标状态；缺省时按 op 名字推断。
		completed, ok := paramBool(params, "Completed", "completed")
		if !ok {
			completed = op == "completeTask"
		}
		return s.sync.SetCompleted(ctx, taskID, completed)

	case "setDueDateTime", "clearDue":
		due, keep, err := parseDue(params["due"])
		if err != nil {
			return nil, err
		}
		if op == "clearDue" || !keep {
			return s.sync.ClearDue(ctx, taskID)
		}
		return s.sync.SetDue(ctx, taskID, due)

	case "deleteTask":
		return s.sync.DeleteTask(ctx, taskID)

	case "restoreTask":
		return s.sync.RestoreTask(ctx, taskID)

	case "createTaskList":
		return s.sync.CreateTaskList(ctx, paramString(params, "Title", "title"))

	case "renameTaskList":
		return s.sync.RenameTaskList(ctx, listID, paramString(params, "Title", "title"))
	}
	return nil, fmt.Errorf("tasks: 操作 %s 已登记为同步通道实现，但还没有对应的写入方法", op)
}

// paramString returns the first non-empty string among keys.
func paramString(params map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := params[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// paramBool reads a boolean that may have arrived as a JSON bool, a number or a
// string. The second result reports whether the key was present at all.
func paramBool(params map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		switch value := params[key].(type) {
		case bool:
			return value, true
		case float64:
			return value != 0, true
		case string:
			if parsed, err := strconv.ParseBool(value); err == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

// parseDue turns the frontend's {date, time} pair into an instant.
//
// A date on its own follows Google's own all-day convention and is stored as
// UTC midnight, so every timezone still reads the same calendar day. With a
// time of day the pair is interpreted in the machine's local zone and stored as
// the real instant it names — that is the only form Google's web and mobile
// clients render as the clock time the user picked.
func parseDue(value any) (time.Time, bool, error) {
	switch due := value.(type) {
	case nil:
		return time.Time{}, false, nil
	case string:
		if strings.TrimSpace(due) == "" {
			return time.Time{}, false, nil
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
			if parsed, err := time.ParseInLocation(layout, due, time.Local); err == nil {
				if layout == "2006-01-02" {
					return allDay(parsed), true, nil
				}
				return parsed, true, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("tasks: 无法解析日期 %q", due)
	case map[string]any:
		date := strings.TrimSpace(paramString(due, "date", "Date"))
		if date == "" {
			return time.Time{}, false, nil
		}
		clock := strings.TrimSpace(paramString(due, "time", "Time"))
		layouts := []string{"2006-01-02"}
		if clock != "" {
			layouts = []string{"2006-01-02T15:04", "2006-01-02T15:04:05"}
		}
		for _, layout := range layouts {
			text := date
			if clock != "" {
				text = date + "T" + clock
			}
			if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
				if clock == "" {
					return allDay(parsed), true, nil
				}
				return parsed, true, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("tasks: 无法解析日期时间 %q %q", date, clock)
	}
	return time.Time{}, false, errors.New("tasks: due 需要是 {date, time} 对象或日期字符串")
}

// allDay 把一个只有日期的值折成 UTC 零点，这是 Google 存整天截止时间的约定，
// 也是读取侧判断「这是整天还是带时分」的依据。
func allDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

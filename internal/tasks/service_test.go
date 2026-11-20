package tasks

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestService 组一个不依赖真实页面的 Service：registry 用内置目录，写入层用
// 假客户端，快照用固定数据。
func newTestService(t *testing.T, client *recordingClient) (*Service, *fakeSnapshots) {
	t.Helper()
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	snapshots := &fakeSnapshots{snapshot: &Snapshot{
		Account:    "123456789012345678901",
		ServerTime: Timestamp{Seconds: 1000},
		ListOrder:  []string{"~default", "list-a"},
	}}
	service := &Service{registry: registry, sync: NewSyncWriter(client, snapshots)}
	return service, snapshots
}

// fullParams 是一份对所有写入操作都够用的参数，方便逐一走一遍分派表。
func fullParams() map[string]any {
	return map[string]any{
		"TaskID":     "task-1",
		"TaskListID": "list-a",
		"Title":      "标题",
		"Starred":    true,
		"Completed":  true,
		"title":      "标题",
		"notes":      "备注",
		"due":        map[string]any{"date": "2026-11-21", "time": "09:00"},
	}
}

// lastPatch 取出最近一次写入请求里那个事件的 patch（任务在槽 6，清单在槽 7，
// 账户级顺序在槽 5）。
func lastPatch(t *testing.T, client *recordingClient) string {
	t.Helper()
	event := eventOf(t, client.calls[len(client.calls)-1])
	for _, slot := range []int{5, 6, 7} {
		if event[slot] != nil {
			return jsonOf(t, event[slot])
		}
	}
	t.Fatalf("事件里没有 patch：%v", event)
	return ""
}

func TestExecuteRoutesEverySyncOperation(t *testing.T) {
	for _, op := range SyncOps() {
		t.Run(string(op), func(t *testing.T) {
			client := &recordingClient{}
			service, _ := newTestService(t, client)
			if _, err := service.Execute(context.Background(), op, fullParams()); err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			// 一次写入 = 一次轮询 + 一次提交。
			if len(client.calls) != 2 {
				t.Fatalf("调用次数 = %d, 期望 2", len(client.calls))
			}
		})
	}
}

func TestExecuteSyncOpParameterMapping(t *testing.T) {
	cases := []struct {
		name   string
		op     Operation
		params map[string]any
		want   string
	}{
		{
			name:   "改标题用小写 title",
			op:     "updateTaskTitle",
			params: map[string]any{"TaskID": "task-1", "title": "买牛奶"},
			want:   `[[null,[2]],[null,"买牛奶"]]`,
		},
		{
			name:   "改备注",
			op:     "updateTaskNotes",
			params: map[string]any{"taskId": "task-1", "notes": "两斤"},
			want:   `[[null,[3]],[null,null,"两斤"]]`,
		},
		{
			name:   "取消星标",
			op:     "setStarred",
			params: map[string]any{"TaskID": "task-1", "Starred": false},
			want:   `[[null,[14,15]],[]]`,
		},
		{
			name:   "星标也可以传字符串布尔",
			op:     "setStarred",
			params: map[string]any{"TaskID": "task-1", "starred": "true"},
			want:   "",
		},
		{
			name:   "completeTask 缺省即完成",
			op:     "completeTask",
			params: map[string]any{"TaskID": "task-1"},
			want:   "",
		},
		{
			name:   "uncompleteTask 缺省即取消",
			op:     "uncompleteTask",
			params: map[string]any{"TaskID": "task-1"},
			want:   `[[null,[1,5]],[]]`,
		},
		{
			name:   "带时分的截止日期",
			op:     "setDueDateTime",
			params: map[string]any{"TaskID": "task-1", "due": map[string]any{"date": "2026-11-21", "time": "09:00"}},
			want:   "",
		},
		{
			name:   "清空日期即清除截止",
			op:     "setDueDateTime",
			params: map[string]any{"TaskID": "task-1", "due": map[string]any{"date": "", "time": ""}},
			want:   `[[null,[18]],[]]`,
		},
		{
			name:   "删除任务",
			op:     "deleteTask",
			params: map[string]any{"TaskID": "task-1"},
			want:   `[[null,[6]],[null,null,null,null,null,1]]`,
		},
		{
			name:   "恢复任务",
			op:     "restoreTask",
			params: map[string]any{"TaskID": "task-1"},
			want:   `[[null,[6]],[]]`,
		},
		{
			name:   "重命名清单",
			op:     "renameTaskList",
			params: map[string]any{"TaskListID": "list-a", "Title": "工作"},
			want:   `[[null,[1]],["工作"]]`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &recordingClient{}
			service, _ := newTestService(t, client)
			if _, err := service.Execute(context.Background(), testCase.op, testCase.params); err != nil {
				t.Fatalf("%s: %v", testCase.op, err)
			}
			got := lastPatch(t, client)
			if testCase.want == "" {
				// 含时间戳的 patch 只校验字段号，时间值每次都不同。
				if strings.HasPrefix(got, `[[null,[`) && strings.HasSuffix(got, `,[]]`) {
					t.Fatalf("patch = %s, 期望带值", got)
				}
				return
			}
			if got != testCase.want {
				t.Fatalf("patch = %s, 期望 %s", got, testCase.want)
			}
		})
	}
}

func TestExecuteDueUsesLocalTimeForClockAndUTCForAllDay(t *testing.T) {
	client := &recordingClient{}
	service, _ := newTestService(t, client)

	clock := time.Date(2026, 11, 21, 9, 0, 0, 0, time.Local)
	_, err := service.Execute(context.Background(), "setDueDateTime", map[string]any{
		"TaskID": "task-1",
		"due":    map[string]any{"date": "2026-11-21", "time": "09:00"},
	})
	if err != nil {
		t.Fatalf("带时分: %v", err)
	}
	want := `[[null,[18]],[null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,[[` +
		strconv.FormatInt(clock.Unix(), 10) + `,0]]]]`
	if got := lastPatch(t, client); got != want {
		t.Fatalf("带时分 patch = %s, 期望 %s", got, want)
	}

	_, err = service.Execute(context.Background(), "setDueDateTime", map[string]any{
		"TaskID": "task-1",
		"due":    map[string]any{"date": "2026-11-21"},
	})
	if err != nil {
		t.Fatalf("整天: %v", err)
	}
	want = `[[null,[18]],[null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,[[1795219200,0]]]]`
	if got := lastPatch(t, client); got != want {
		t.Fatalf("整天 patch = %s, 期望 %s（UTC 零点）", got, want)
	}
}

func TestExecuteRejectsUnsupportedWrites(t *testing.T) {
	client := &recordingClient{}
	service, _ := newTestService(t, client)
	ctx := context.Background()

	// 子任务的报文还没破解，必须明确拒绝，而不是悄悄建一个顶层任务。
	_, err := service.Execute(ctx, "createTask", map[string]any{
		"TaskListID": "list-a", "Title": "子任务", "ParentID": "task-1",
	})
	if err == nil || !strings.Contains(err.Error(), "子任务") {
		t.Fatalf("错误 = %v, 期望提示子任务未实现", err)
	}

	// 缺参数的写入不该发请求。
	for _, bad := range []map[string]any{
		{"Title": "没有清单"},
		{"TaskListID": "list-a"},
	} {
		if _, err := service.Execute(ctx, "createTask", bad); err == nil {
			t.Fatalf("%v 应该报错", bad)
		}
	}
	if _, err := service.Execute(ctx, "setStarred", map[string]any{"TaskID": "task-1"}); err == nil {
		t.Fatal("setStarred 缺 Starred 应该报错")
	}
	if _, err := service.Execute(ctx, "updateTaskTitle", map[string]any{"TaskID": "task-1"}); err == nil {
		t.Fatal("空标题应该报错")
	}
	if len(client.calls) != 0 {
		t.Fatalf("参数不合法时不该发请求，实到 %d 次", len(client.calls))
	}

	// 还没破解的操作继续走 registry，报「尚未配置 rpcid」而不是静默成功。
	if _, err := service.Execute(ctx, "moveTask", map[string]any{"TaskID": "task-1"}); err == nil ||
		!strings.Contains(err.Error(), "尚未配置 rpcid") {
		t.Fatalf("错误 = %v, 期望提示还没固化 rpcid", err)
	}
}

func TestSyncOpsAreAllInTheCatalogue(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	for _, op := range SyncOps() {
		definition, ok := registry.Lookup(op)
		if !ok {
			t.Fatalf("内置操作表缺少 %s，前端不会显示它", op)
		}
		if definition.Ready() {
			t.Fatalf("%s 由同步通道实现，不该再带 steps", op)
		}
		if !IsSyncOp(op) {
			t.Fatalf("%s 没被登记为同步通道实现", op)
		}
		if SyncOpNotes(op) == "" {
			t.Fatalf("%s 缺少实现说明", op)
		}
	}
}

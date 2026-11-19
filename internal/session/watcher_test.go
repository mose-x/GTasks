package session

import (
	"sync"
	"testing"
	"time"

	"gtasks/internal/batchexec"
	"gtasks/internal/bridge"
)

// An unbound bridge makes every probe fail immediately, which exercises the
// watcher loop without needing a live page.
func newTestWatcher(t *testing.T, interval time.Duration) *Watcher {
	t.Helper()
	return NewWatcher(batchexec.NewClient(bridge.New()), interval)
}

func TestNewWatcherDefaults(t *testing.T) {
	watcher := newTestWatcher(t, 0)
	if !watcher.AutoManage() {
		t.Fatal("自动隐藏会话窗口应默认开启")
	}
	if !watcher.AutoSniff() {
		t.Fatal("自动抓包应默认开启")
	}
	if watcher.Usable() {
		t.Fatal("尚未观测时不应报告可用")
	}
	watcher.SetAutoManage(false)
	watcher.SetAutoSniff(false)
	if watcher.AutoManage() || watcher.AutoSniff() {
		t.Fatal("开关未生效")
	}
}

func TestWatcherReportsProbeFailure(t *testing.T) {
	watcher := newTestWatcher(t, 20*time.Millisecond)

	var mu sync.Mutex
	var calls int
	watcher.SetChangeHandler(func(current, previous Status) {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	watcher.Start()
	// Start kicks off one immediate observation; give the loop a moment.
	time.Sleep(120 * time.Millisecond)
	watcher.Stop()

	status := watcher.Status()
	if status.Ready || status.LoggedIn {
		t.Fatalf("探测失败时不应报告就绪: %+v", status)
	}
	if status.Error == "" {
		t.Fatal("应记录探测错误")
	}
	if status.CheckedAt == 0 {
		t.Fatal("应记录观测时间")
	}

	mu.Lock()
	got := calls
	mu.Unlock()
	if got == 0 {
		t.Fatal("首次观测应触发变更回调")
	}
}

func TestWatcherStopIsIdempotent(t *testing.T) {
	watcher := newTestWatcher(t, 20*time.Millisecond)
	watcher.Start()
	watcher.Stop()
	watcher.Stop() // must not block or panic
	watcher.Refresh()
}

func TestEqualStatusIgnoresTimestamp(t *testing.T) {
	base := Status{Ready: true, LoggedIn: true, At: "x", CheckedAt: 1}
	same := Status{Ready: true, LoggedIn: true, At: "x", CheckedAt: 999}
	if !equalStatus(base, same) {
		t.Fatal("仅时间戳不同应视为未变化")
	}
	changed := same
	changed.LoggedIn = false
	if equalStatus(base, changed) {
		t.Fatal("登录状态变化应视为已变化")
	}
}

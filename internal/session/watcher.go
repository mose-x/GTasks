// Package session tracks whether the embedded Google page is signed in.
//
// The webview owns the credentials; Go only ever sees derived facts such as
// "is there an SNlM0e token on the page". The watcher polls that state so the
// UI can show the login window when it is needed and hide it again afterwards.
package session

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"gtasks/internal/batchexec"
)

// DefaultInterval is how often the watcher re-probes the page.
const DefaultInterval = 4 * time.Second

// probeTimeout bounds a single probe so a stuck page cannot wedge the loop.
const probeTimeout = 6 * time.Second

// healthEvery is how many polls sit between two session health checks
// (DefaultInterval * healthEvery ≈ half a minute). A previous bad result
// re-checks on every poll instead.
const healthEvery = 8

// Status is one observation of the session page.
type Status struct {
	Ready     bool   `json:"ready"`
	LoggedIn  bool   `json:"loggedIn"`
	URL       string `json:"url,omitempty"`
	Origin    string `json:"origin,omitempty"`
	At        string `json:"at,omitempty"`
	Bl        string `json:"bl,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
	// Health is the last lightweight RPC probe outcome: ok / unauthorized /
	// redirected / http<code>, empty while nothing has been checked yet.
	Health string `json:"health,omitempty"`
	// Expired is true when Health proves the session no longer works even
	// though the page still looks logged in.
	Expired   bool   `json:"expired,omitempty"`
	CheckedAt int64  `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

// usable reports whether the bridge can currently issue RPCs.
func (s Status) usable() bool { return s.Ready && s.LoggedIn && s.Error == "" }

// ChangeHandler is invoked whenever the observed status changes.
type ChangeHandler func(current, previous Status)

// Watcher polls the session page in the background.
type Watcher struct {
	client   *batchexec.Client
	interval time.Duration

	mu         sync.RWMutex
	status     Status
	observed   bool
	polls      int
	autoManage bool
	autoSniff  bool
	sniffing   bool
	handler    ChangeHandler

	cancel context.CancelFunc
	done   chan struct{}
}

// NewWatcher returns a watcher. It is idle until Start is called.
func NewWatcher(client *batchexec.Client, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Watcher{
		client:     client,
		interval:   interval,
		autoManage: true,
		autoSniff:  true,
		done:       make(chan struct{}),
	}
}

// SetChangeHandler registers the callback used for status transitions.
func (w *Watcher) SetChangeHandler(handler ChangeHandler) {
	w.mu.Lock()
	w.handler = handler
	w.mu.Unlock()
}

// SetAutoManage toggles automatic show/hide of the session window. The watcher
// itself never touches windows; the change handler installed in main does.
func (w *Watcher) SetAutoManage(enabled bool) {
	w.mu.Lock()
	w.autoManage = enabled
	w.mu.Unlock()
}

// AutoManage reports whether automatic window management is on.
func (w *Watcher) AutoManage() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.autoManage
}

// SetAutoSniff toggles starting the in-page recorder as soon as the bridge is up.
func (w *Watcher) SetAutoSniff(enabled bool) {
	w.mu.Lock()
	w.autoSniff = enabled
	w.mu.Unlock()
}

// AutoSniff reports whether the recorder starts automatically.
func (w *Watcher) AutoSniff() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.autoSniff
}

// Status returns the most recent observation.
func (w *Watcher) Status() Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

// Sniffing reports whether the in-page recorder was started by the watcher.
func (w *Watcher) Sniffing() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sniffing
}

// Start begins polling. It returns immediately; Stop ends the loop.
func (w *Watcher) Start() {
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go w.loop(ctx, done)
}

// Stop ends the polling loop and waits for it to return.
func (w *Watcher) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.cancel = nil
	w.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Refresh performs a single probe outside the polling loop.
func (w *Watcher) Refresh() Status {
	return w.observe()
}

func (w *Watcher) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	w.observe()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.observe()
		}
	}
}

// observe probes the page once and fires the change handler on transitions.
func (w *Watcher) observe() Status {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	session, err := w.client.Probe(ctx)
	cancel()

	current := Status{CheckedAt: time.Now().UnixMilli()}
	switch {
	case err != nil:
		current.Error = err.Error()
	case session != nil:
		current.Ready = true
		current.LoggedIn = session.LoggedIn
		current.URL = session.URL
		current.Origin = session.Origin
		current.At = session.At
		current.Bl = session.Bl
		current.UserAgent = session.UserAgent
	}

	// Probe only proves the page once loaded while signed in: the SPA keeps its
	// stale WIZ_global_data after the cookies die, so a logged-in looking page
	// can still fail every RPC. A minimal batchexecute call settles it.
	if current.Ready && current.LoggedIn {
		w.mu.Lock()
		w.polls++
		due := w.polls%healthEvery == 1 || batchexec.AuthBad(w.status.Health)
		previousHealth := w.status.Health
		w.mu.Unlock()

		current.Health = previousHealth
		if due {
			healthCtx, healthCancel := context.WithTimeout(context.Background(), probeTimeout)
			health, err := w.client.Health(healthCtx)
			healthCancel()
			// A failed health call is not itself proof of expiry; keep the
			// previous verdict and try again on the next poll.
			if err == nil {
				current.Health = health.State
			}
		}
		current.Expired = batchexec.AuthBad(current.Health)
	}

	w.mu.Lock()
	previous := w.status
	changed := !w.observed || !equalStatus(previous, current)
	w.status = current
	w.observed = true
	handler := w.handler
	autoSniff := w.autoSniff
	w.mu.Unlock()

	// Keep the in-page recorder armed. The bootstrap is re-injected after every
	// navigation and a fresh copy starts with recording off, so this re-asserts
	// it on each poll rather than only once; sniffStart is idempotent. Failures
	// are non-fatal: the UI can still start the recorder by hand.
	if autoSniff && current.Ready {
		sniffCtx, sniffCancel := context.WithTimeout(context.Background(), probeTimeout)
		_, err := w.client.SniffStart(sniffCtx)
		sniffCancel()
		w.mu.Lock()
		w.sniffing = err == nil
		w.mu.Unlock()
	}

	if changed && handler != nil {
		handler(current, previous)
	}
	return current
}

func equalStatus(a, b Status) bool {
	// CheckedAt moves on every poll; ignore it when comparing.
	a.CheckedAt, b.CheckedAt = 0, 0
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(encodedA) == string(encodedB)
}

// Usable reports whether RPCs can currently be issued.
func (w *Watcher) Usable() bool { return w.Status().usable() }

package notify

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/ydethe/webprogress/internal/bus"
	"github.com/ydethe/webprogress/internal/models"
)

// Resolver returns the effective Config for a user. The server wires this to
// "persisted settings if present, else env defaults".
type Resolver func(ctx context.Context, userSub string) Config

// Dispatcher watches the bus of routed progress updates and emits a one-shot
// notification per task when it completes (fraction >= 1) or when it has gone
// silent for longer than the user's stall timeout. All task bookkeeping runs on
// the single Run goroutine, so the state map needs no locking; only the outbound
// HTTP send is off-loaded to a short-lived goroutine via the sink.
type Dispatcher struct {
	resolve      Resolver
	client       *http.Client
	scanInterval time.Duration // how often stalled tasks are swept for
	retain       time.Duration // how long finished/stalled tasks linger before pruning
	now          func() time.Time
	tasks        map[string]*taskState
	// sink performs the actual delivery. The default sends over HTTP in a
	// detached goroutine; tests replace it to capture emissions synchronously.
	sink func(userSub string, cfg Config, msg Message)
}

type taskState struct {
	payload  models.ClientPayload
	lastSeen time.Time
	done     bool // completion already notified
	stalled  bool // stall already notified (reset by a fresh update)
}

// NewDispatcher builds a Dispatcher using resolve to look up each user's config.
func NewDispatcher(resolve Resolver) *Dispatcher {
	d := &Dispatcher{
		resolve:      resolve,
		client:       &http.Client{Timeout: 10 * time.Second},
		scanInterval: 15 * time.Second,
		retain:       10 * time.Minute,
		now:          time.Now,
		tasks:        make(map[string]*taskState),
	}
	d.sink = d.send
	return d
}

// Run subscribes to the hub and processes updates until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context, hub *bus.Hub) {
	updates, cancel := hub.Subscribe()
	defer cancel()

	ticker := time.NewTicker(d.scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case routed, ok := <-updates:
			if !ok {
				return
			}
			d.observe(ctx, routed)
		case <-ticker.C:
			d.scanStalled(ctx)
		}
	}
}

// taskID namespaces a task key by its owning user so two users reporting the
// same (script, host, description) are tracked independently.
func taskID(userSub, taskKey string) string { return userSub + "\x00" + taskKey }

// observe records an update and fires the completion notification the first
// time a task reaches 100%.
func (d *Dispatcher) observe(ctx context.Context, routed bus.RoutedPayload) {
	id := taskID(routed.UserSub, routed.Payload.TaskKey())
	st := d.tasks[id]
	if st == nil {
		st = &taskState{}
		d.tasks[id] = st
	}
	st.payload = routed.Payload
	st.lastSeen = d.now()
	st.stalled = false // a fresh update clears any prior stall

	if routed.Payload.Fraction() >= 1 {
		if !st.done {
			st.done = true
			d.sink(routed.UserSub, d.resolve(ctx, routed.UserSub), completionMessage(routed.Payload))
		}
		return
	}
	st.done = false
}

// scanStalled fires a one-shot stall notification for any live task whose last
// update is older than its owner's stall timeout, and prunes long-idle tasks.
func (d *Dispatcher) scanStalled(ctx context.Context) {
	now := d.now()
	cache := map[string]Config{}
	resolve := func(sub string) Config {
		if c, ok := cache[sub]; ok {
			return c
		}
		c := d.resolve(ctx, sub)
		cache[sub] = c
		return c
	}

	for id, st := range d.tasks {
		sub, _, _ := splitTaskID(id)
		idle := now.Sub(st.lastSeen)

		if st.done || st.stalled {
			if idle > d.retain {
				delete(d.tasks, id)
			}
			continue
		}
		cfg := resolve(sub)
		if cfg.StallSeconds > 0 && idle >= time.Duration(cfg.StallSeconds)*time.Second {
			st.stalled = true
			d.sink(sub, cfg, stallMessage(st.payload, idle))
		}
	}
}

func splitTaskID(id string) (userSub, taskKey string, ok bool) {
	for i := 0; i < len(id); i++ {
		if id[i] == 0 {
			return id[:i], id[i+1:], true
		}
	}
	return id, "", false
}

// send is the default sink: it drops disabled configs and delivers everything
// else over HTTP in a detached goroutine so the Run loop never blocks on I/O.
func (d *Dispatcher) send(userSub string, cfg Config, msg Message) {
	if !cfg.Enabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := Send(ctx, d.client, cfg, msg); err != nil {
			log.Printf("notify: %s notification for %s failed: %v", msg.Event, userSub, err)
		}
	}()
}

// ---- message rendering -----------------------------------------------------

func taskInfo(p models.ClientPayload) TaskInfo {
	return TaskInfo{
		Script:      p.ScriptName(),
		Host:        p.UserHostname,
		Login:       p.UserLogin,
		Description: taskLabel(p),
		Progress:    p.Progress,
		Total:       p.Total,
		Fraction:    p.Fraction(),
	}
}

func completionMessage(p models.ClientPayload) Message {
	return Message{
		Event: "complete",
		Title: "✅ " + taskLabel(p) + " complete",
		Body: fmt.Sprintf("%s finished on %s (%s %s)",
			p.ScriptName(), hostLabel(p), fmtCount(p.Total), unitOf(p)),
		Task: taskInfo(p),
	}
}

func stallMessage(p models.ClientPayload, idle time.Duration) Message {
	return Message{
		Event: "stalled",
		Title: "⚠️ " + taskLabel(p) + " stalled",
		Body: fmt.Sprintf("%s on %s: no update for %s, %.0f%% done (%s/%s %s)",
			p.ScriptName(), hostLabel(p), fmtDuration(idle),
			p.Fraction()*100, fmtCount(p.Progress), fmtCount(p.Total), unitOf(p)),
		Task: taskInfo(p),
	}
}

// TestMessage is the payload sent by the "send a test" action so a user can
// confirm their channel works without waiting for a real task.
func TestMessage() Message {
	return Message{
		Event: "test",
		Title: "🔔 webprogress test",
		Body:  "Your notification channel is configured correctly.",
	}
}

func taskLabel(p models.ClientPayload) string {
	if p.Description == "" {
		return "task"
	}
	return p.Description
}

func hostLabel(p models.ClientPayload) string {
	if p.UserHostname == "" {
		return "unknown host"
	}
	return p.UserHostname
}

func unitOf(p models.ClientPayload) string {
	if p.Unit == "" {
		return "it"
	}
	return p.Unit
}

func fmtCount(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

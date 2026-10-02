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

// Dispatcher watches the bus of routed progress updates and emits one-shot
// notifications per task — on completion (fraction >= 1), on a stall (silent past
// the user's stall timeout), and on death (silent past the task's cadence-derived
// dead threshold). Which of these actually fire is gated by the task's criticity
// (see models.Criticity): TRIVIAL fires nothing, STANDARD fires only on death,
// and CRITICAL fires on all three. All task bookkeeping runs on the single Run
// goroutine, so the state map needs no locking; only the outbound HTTP send is
// off-loaded to a short-lived goroutine via the sink.
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
	payload     models.ClientPayload
	lastSeen    time.Time
	deadSeconds float64 // cadence-derived dead threshold from the latest update (0 = unknown)
	done        bool    // completion already notified
	stalled     bool    // stall already notified (reset by a fresh update)
	dead        bool    // dead already notified (reset by a fresh update)
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

// observe records an update and, for a CRITICAL task, fires the completion
// notification the first time it reaches 100%. A fresh update also clears any
// prior stall/dead so those can fire again if the task goes silent once more.
func (d *Dispatcher) observe(ctx context.Context, routed bus.RoutedPayload) {
	id := taskID(routed.UserSub, routed.Payload.TaskKey())
	st := d.tasks[id]
	if st == nil {
		st = &taskState{}
		d.tasks[id] = st
	}
	st.payload = routed.Payload
	st.lastSeen = d.now()
	st.deadSeconds = routed.DeadSeconds
	st.stalled = false // a fresh update clears any prior stall
	st.dead = false    // and any prior death — the task is reporting again

	if routed.Payload.Fraction() >= 1 {
		if !st.done {
			st.done = true
			if routed.Payload.EffectiveCriticity().NotifyOnComplete() {
				d.sink(routed.UserSub, d.resolve(ctx, routed.UserSub), completionMessage(routed.Payload))
			}
		}
		return
	}
	st.done = false
}

// scanStalled sweeps live tasks and fires their one-shot silence notifications,
// each gated by the task's criticity: a dead notification (STANDARD and CRITICAL)
// once idle passes the task's cadence-derived dead threshold, and a stall
// notification (CRITICAL only) once idle passes the owner's stall timeout. Tasks
// that have gone terminal (completed or dead) are pruned after they have lingered
// past the retain window.
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

		if st.done || st.dead {
			if idle > d.retain {
				delete(d.tasks, id)
			}
			continue
		}
		crit := st.payload.EffectiveCriticity()
		cfg := resolve(sub)
		// Dead: presumed gone. Uses the task's cadence-derived threshold so it
		// mirrors the dashboard's own aging; fires for STANDARD and CRITICAL.
		if st.deadSeconds > 0 && idle >= time.Duration(st.deadSeconds*float64(time.Second)) && crit.NotifyOnDead() {
			st.dead = true
			d.sink(sub, cfg, deadMessage(st.payload, idle))
			continue
		}
		// Stall: recoverable silence past the user's own timeout; CRITICAL only.
		if !st.stalled && cfg.StallSeconds > 0 && idle >= time.Duration(cfg.StallSeconds)*time.Second && crit.NotifyOnStall() {
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
		Library:     p.LibraryLabel(),
		Criticity:   string(p.EffectiveCriticity()),
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

func deadMessage(p models.ClientPayload, idle time.Duration) Message {
	return Message{
		Event: "dead",
		Title: "💀 " + taskLabel(p) + " dead",
		Body: fmt.Sprintf("%s on %s: no update for %s, presumed dead at %.0f%% (%s/%s %s)",
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

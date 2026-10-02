package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ydethe/webprogress/internal/bus"
	"github.com/ydethe/webprogress/internal/models"
)

// recorder is a synchronous sink that captures emitted notifications so tests
// can assert on the Dispatcher's decisions without real HTTP delivery.
type recorder struct {
	mu   sync.Mutex
	msgs []Message
}

func (r *recorder) sink(userSub string, cfg Config, msg Message) {
	if !cfg.Enabled() {
		return
	}
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
}

func newTestDispatcher(rec *recorder, cfg Config, now *time.Time) *Dispatcher {
	d := NewDispatcher(func(context.Context, string) Config { return cfg })
	d.sink = rec.sink
	d.now = func() time.Time { return *now }
	return d
}

func payload(frac float64) models.ClientPayload {
	// Criticity defaults to CRITICAL so a bare payload exercises every
	// notification (stall, dead, completion); tests that need another level set
	// it explicitly via critPayload.
	return critPayload(frac, models.CriticityCritical)
}

func critPayload(frac float64, crit models.Criticity) models.ClientPayload {
	return models.ClientPayload{
		UserHostname: "host", Description: "job", Script: "deploy.sh",
		Progress: frac * 10, Total: 10, Criticity: crit,
	}
}

func TestCompletionFiresOnce(t *testing.T) {
	rec := &recorder{}
	now := time.Now()
	d := newTestDispatcher(rec, Config{Channel: ChannelSlack, SlackWebhookURL: "x"}, &now)
	ctx := context.Background()

	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(0.5)})
	if len(rec.msgs) != 0 {
		t.Fatalf("mid-progress should not notify, got %d", len(rec.msgs))
	}
	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(1)})
	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(1)}) // duplicate
	if len(rec.msgs) != 1 || rec.msgs[0].Event != "complete" {
		t.Fatalf("want exactly one completion, got %+v", rec.msgs)
	}
}

func TestStallFiresAfterTimeout(t *testing.T) {
	rec := &recorder{}
	now := time.Now()
	d := newTestDispatcher(rec, Config{Channel: ChannelSlack, SlackWebhookURL: "x", StallSeconds: 60}, &now)
	ctx := context.Background()

	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(0.5)})

	// Not yet past the timeout.
	now = now.Add(30 * time.Second)
	d.scanStalled(ctx)
	if len(rec.msgs) != 0 {
		t.Fatalf("stall fired too early: %+v", rec.msgs)
	}

	// Past the timeout — one stall, and only one even if scanned again.
	now = now.Add(40 * time.Second)
	d.scanStalled(ctx)
	d.scanStalled(ctx)
	if len(rec.msgs) != 1 || rec.msgs[0].Event != "stalled" {
		t.Fatalf("want exactly one stall, got %+v", rec.msgs)
	}

	// A fresh update clears the stall, so it can fire again later.
	now = now.Add(5 * time.Second)
	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(0.6)})
	now = now.Add(61 * time.Second)
	d.scanStalled(ctx)
	if len(rec.msgs) != 2 {
		t.Fatalf("want a second stall after a fresh update, got %+v", rec.msgs)
	}
}

func TestCompletedTaskDoesNotStall(t *testing.T) {
	rec := &recorder{}
	now := time.Now()
	d := newTestDispatcher(rec, Config{Channel: ChannelSlack, SlackWebhookURL: "x", StallSeconds: 60}, &now)
	ctx := context.Background()

	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(1)}) // completes
	now = now.Add(10 * time.Minute)
	d.scanStalled(ctx)
	// Only the completion message; no stall for a finished task.
	if len(rec.msgs) != 1 || rec.msgs[0].Event != "complete" {
		t.Fatalf("finished task should not stall: %+v", rec.msgs)
	}
}

func TestStallDisabledWhenTimeoutZero(t *testing.T) {
	rec := &recorder{}
	now := time.Now()
	d := newTestDispatcher(rec, Config{Channel: ChannelSlack, SlackWebhookURL: "x", StallSeconds: 0}, &now)
	ctx := context.Background()

	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", Payload: payload(0.5)})
	now = now.Add(time.Hour)
	d.scanStalled(ctx)
	if len(rec.msgs) != 0 {
		t.Fatalf("stall should be disabled with StallSeconds=0: %+v", rec.msgs)
	}
}

// A dead notification fires once the task has been silent past its cadence-derived
// dead threshold (carried on the routed payload), independently of the user's
// stall timeout.
func TestDeadFiresAfterDeadThreshold(t *testing.T) {
	rec := &recorder{}
	now := time.Now()
	d := newTestDispatcher(rec, Config{Channel: ChannelSlack, SlackWebhookURL: "x"}, &now)
	ctx := context.Background()

	d.observe(ctx, bus.RoutedPayload{UserSub: "u1", DeadSeconds: 100, Payload: critPayload(0.5, models.CriticityStandard)})

	// Before the dead threshold: nothing.
	now = now.Add(99 * time.Second)
	d.scanStalled(ctx)
	if len(rec.msgs) != 0 {
		t.Fatalf("dead fired too early: %+v", rec.msgs)
	}

	// Past it: exactly one dead, even scanned twice.
	now = now.Add(2 * time.Second)
	d.scanStalled(ctx)
	d.scanStalled(ctx)
	if len(rec.msgs) != 1 || rec.msgs[0].Event != "dead" {
		t.Fatalf("want exactly one dead, got %+v", rec.msgs)
	}
}

// The criticity level gates which notifications fire: TRIVIAL fires nothing,
// STANDARD only on death, CRITICAL on stall, death, and completion.
func TestCriticityGatesNotifications(t *testing.T) {
	cfg := Config{Channel: ChannelSlack, SlackWebhookURL: "x", StallSeconds: 60}

	cases := []struct {
		crit     models.Criticity
		wantIncr []string // events, in order, for stall → dead → (restart) complete
	}{
		{models.CriticityTrivial, nil},
		{models.CriticityStandard, []string{"dead"}},
		{models.CriticityCritical, []string{"stalled", "dead", "complete"}},
	}
	for _, c := range cases {
		t.Run(string(c.crit), func(t *testing.T) {
			rec := &recorder{}
			now := time.Now()
			d := newTestDispatcher(rec, cfg, &now)
			ctx := context.Background()

			// A live update with a known dead threshold, then let it go silent far
			// past both the stall timeout (60s) and the dead threshold (300s).
			d.observe(ctx, bus.RoutedPayload{UserSub: "u1", DeadSeconds: 300, Payload: critPayload(0.5, c.crit)})
			now = now.Add(61 * time.Second)
			d.scanStalled(ctx) // crosses the stall timeout
			now = now.Add(300 * time.Second)
			d.scanStalled(ctx) // crosses the dead threshold
			// Then the task comes back and completes.
			d.observe(ctx, bus.RoutedPayload{UserSub: "u1", DeadSeconds: 300, Payload: critPayload(1, c.crit)})

			var got []string
			for _, m := range rec.msgs {
				got = append(got, m.Event)
			}
			if len(got) != len(c.wantIncr) {
				t.Fatalf("%s: events = %v, want %v", c.crit, got, c.wantIncr)
			}
			for i := range got {
				if got[i] != c.wantIncr[i] {
					t.Fatalf("%s: events = %v, want %v", c.crit, got, c.wantIncr)
				}
			}
		})
	}
}

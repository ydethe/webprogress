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
	return models.ClientPayload{
		UserHostname: "host", Description: "job", Script: "deploy.sh",
		Progress: frac * 10, Total: 10,
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

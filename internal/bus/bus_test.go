package bus

import (
	"testing"
	"time"

	"github.com/ydethe/webprogress/internal/models"
)

func TestFanOut(t *testing.T) {
	h := New()
	ch1, cancel1 := h.Subscribe()
	ch2, cancel2 := h.Subscribe()
	defer cancel1()
	defer cancel2()

	want := RoutedPayload{UserSub: "u1", Payload: models.ClientPayload{Description: "job"}}
	h.Publish(want)

	for i, ch := range []<-chan RoutedPayload{ch1, ch2} {
		select {
		case got := <-ch:
			if got.UserSub != "u1" || got.Payload.Description != "job" {
				t.Fatalf("subscriber %d got %+v", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

func TestCancelUnsubscribes(t *testing.T) {
	h := New()
	ch, cancel := h.Subscribe()
	cancel()

	h.Publish(RoutedPayload{UserSub: "u1"})

	// After cancel the channel is closed and drained.
	if _, open := <-ch; open {
		t.Fatal("expected closed channel after cancel")
	}
}

func TestPublishNonBlockingWhenBufferFull(t *testing.T) {
	h := New()
	_, cancel := h.Subscribe() // never drained
	defer cancel()

	done := make(chan struct{})
	go func() {
		// subBuffer+10 publishes must not block even though nobody reads.
		for range subBuffer + 10 {
			h.Publish(RoutedPayload{UserSub: "u"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}
}

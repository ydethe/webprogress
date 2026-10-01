package server

import (
	"testing"
	"time"

	"github.com/ydethe/webprogress/internal/models"
)

// The thresholds track the run's observed cadence: once two updates ten seconds
// apart have been seen, a task is stalled after 2× and dead after 10× that gap.
func TestCadenceTrackerDerivesThresholdsFromCadence(t *testing.T) {
	now := time.Now()
	tr := newCadenceTracker(30)
	tr.now = func() time.Time { return now }

	// First update: cadence unknown, so the configured default (30s) applies.
	stall, dead := tr.observe("user-1", "run-1")
	if stall != 60 || dead != 300 {
		t.Fatalf("first update thresholds = %v/%v, want 60/300 (default)", stall, dead)
	}

	// Second update 10s later: cadence is now 10s.
	now = now.Add(10 * time.Second)
	stall, dead = tr.observe("user-1", "run-1")
	if stall != 20 || dead != 100 {
		t.Fatalf("cadence thresholds = %v/%v, want 20/100", stall, dead)
	}
}

// A very fast task is floored at minCadence so network jitter does not flap it.
func TestCadenceTrackerFloorsFastCadence(t *testing.T) {
	now := time.Now()
	tr := newCadenceTracker(30)
	tr.now = func() time.Time { return now }

	tr.observe("user-1", "run-1")
	now = now.Add(100 * time.Millisecond) // updating ten times a second
	stall, _ := tr.observe("user-1", "run-1")
	if stall != models.StallMultiple*minCadence {
		t.Fatalf("stall = %v, want floored to %v", stall, models.StallMultiple*minCadence)
	}
}

// With the default disabled, a task cannot be judged until its cadence is known.
func TestCadenceTrackerNoDefaultLeavesFirstUpdateUnjudged(t *testing.T) {
	tr := newCadenceTracker(0)
	stall, dead := tr.observe("user-1", "run-1")
	if stall != 0 || dead != 0 {
		t.Fatalf("thresholds = %v/%v, want 0/0 when default disabled and cadence unknown", stall, dead)
	}
}

// Each run key carries its own cadence: a restart under a new key starts fresh,
// unaffected by the gap since the previous run was last seen.
func TestCadenceTrackerPerRunKey(t *testing.T) {
	now := time.Now()
	tr := newCadenceTracker(30)
	tr.now = func() time.Time { return now }

	tr.observe("user-1", "run-1")
	now = now.Add(10 * time.Second)
	tr.observe("user-1", "run-1") // run-1 cadence is now 10s

	// A long time later a restart reports under a new key; its first update has no
	// cadence yet, so it uses the default rather than run-1's stale estimate.
	now = now.Add(time.Hour)
	stall, dead := tr.observe("user-1", "run-2")
	if stall != 60 || dead != 300 {
		t.Fatalf("new run thresholds = %v/%v, want 60/300 (default, not run-1's)", stall, dead)
	}
}

func TestCadenceTrackerIsolatesUsers(t *testing.T) {
	now := time.Now()
	tr := newCadenceTracker(30)
	tr.now = func() time.Time { return now }

	// Two users share the same run key; one user's cadence must not leak to the
	// other, who is still on its first (default) update.
	tr.observe("user-1", "run-1")
	now = now.Add(5 * time.Second)
	tr.observe("user-1", "run-1") // user-1 cadence is 5s

	stall, dead := tr.observe("user-2", "run-1")
	if stall != 60 || dead != 300 {
		t.Fatalf("user-2 thresholds = %v/%v, want 60/300 (its own default)", stall, dead)
	}
}

func TestCadenceTrackerPrunesIdleEntries(t *testing.T) {
	now := time.Now()
	tr := newCadenceTracker(30)
	tr.now = func() time.Time { return now }

	tr.observe("user-1", "run-1")
	if len(tr.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(tr.entries))
	}

	// Jump past both the retain window and the prune interval; the next call
	// sweeps the stale entry (a different run key, so it is not just reused).
	now = now.Add(cadenceRetain + cadencePrune + time.Minute)
	tr.observe("user-1", "run-2")
	if _, ok := tr.entries["user-1\x00run-1"]; ok {
		t.Fatal("idle entry was not pruned")
	}
}

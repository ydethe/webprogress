package server

import (
	"sync"
	"time"

	"github.com/ydethe/webprogress/internal/models"
)

// A task reports on a cadence that follows from its rate: tqdm redraws at a
// roughly steady interval. cadenceTracker measures that interval per run so the
// dashboard can tell when a task has gone quiet — it ages a card to stalled after
// StallMultiple cadences of silence and to dead after DeadMultiple.
//
// Identity is the client's business, not the tracker's. Each run carries its own
// client-assigned uuid (see models.ClientPayload.InstanceKey), so a restart
// arrives under a new key and starts a fresh cadence estimate on its own. The
// tracker keeps, per run, only the time it was last seen and an
// exponentially-weighted estimate of the gap between its updates.
type cadenceTracker struct {
	mu              sync.Mutex
	now             func() time.Time
	retain          time.Duration // forget a run untouched for this long
	defaultInterval float64       // assumed cadence until the run's own is observed
	entries         map[string]*cadenceEntry
	lastPrune       time.Time
}

type cadenceEntry struct {
	lastSeen time.Time
	interval float64 // EWMA of seconds between updates; 0 until a gap is seen
}

const (
	cadenceRetain = time.Hour
	cadencePrune  = 10 * time.Minute
	// cadenceAlpha weights the latest inter-update gap against the running
	// estimate, smoothing out bursty updates so a single short gap does not make
	// the task look stalled on the next normal one.
	cadenceAlpha = 0.5
	// minCadence floors the cadence used for the thresholds so a very fast task
	// (many updates a second) is not flagged stalled on ordinary network jitter.
	minCadence = 2.0
)

func newCadenceTracker(defaultInterval float64) *cadenceTracker {
	return &cadenceTracker{
		now:             time.Now,
		retain:          cadenceRetain,
		defaultInterval: defaultInterval,
		entries:         make(map[string]*cadenceEntry),
	}
}

// observe records that the run instanceKey reported now and returns the silence
// thresholds (seconds) derived from its cadence. The key is namespaced by user so
// two users reporting the same run key are tracked independently. stall and dead
// are both 0 when the cadence is unknown and no default is configured, which
// leaves the task unjudged until its cadence is observed.
func (t *cadenceTracker) observe(userSub, instanceKey string) (stall, dead float64) {
	key := userSub + "\x00" + instanceKey
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	e := t.entries[key]
	if e == nil {
		e = &cadenceEntry{}
		t.entries[key] = e
	} else if gap := now.Sub(e.lastSeen).Seconds(); gap > 0 {
		if e.interval == 0 {
			e.interval = gap
		} else {
			e.interval = cadenceAlpha*gap + (1-cadenceAlpha)*e.interval
		}
	}
	e.lastSeen = now

	t.pruneLocked(now)

	// Until the run's own cadence is known, fall back to the configured default so
	// even a report-once-and-die task is eventually aged out.
	cadence := e.interval
	if cadence <= 0 {
		cadence = t.defaultInterval
	}
	if cadence <= 0 {
		return 0, 0
	}
	if cadence < minCadence {
		cadence = minCadence
	}
	return models.StallMultiple * cadence, models.DeadMultiple * cadence
}

// pruneLocked drops runs untouched for longer than retain so the map does not
// grow without bound. It scans at most once per cadencePrune interval, since a
// full scan on every update would be wasteful. The caller holds t.mu.
func (t *cadenceTracker) pruneLocked(now time.Time) {
	if now.Sub(t.lastPrune) < cadencePrune {
		return
	}
	t.lastPrune = now
	for key, e := range t.entries {
		if now.Sub(e.lastSeen) > t.retain {
			delete(t.entries, key)
		}
	}
}

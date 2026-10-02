// Package models holds the shared wire contract between the progress reporter
// (the Python tqdm client) and this server. The JSON field names here must match
// exactly what the client serializes; changing one is a contract change that
// affects both sides at once.
package models

import "time"

// ProtocolVersion is the version of the wire protocol this server speaks. The
// server advertises it (together with its build version) from GET /version so a
// client can discover it before reporting and adapt the update message it sends
// — for example, only populating fields the server's protocol understands. It is
// bumped whenever the shared contract changes in a way clients must adapt to.
//
// v2 adds the optional `tags` field to ClientPayload.
// v3 adds the client-assigned `uuid` field, one per task run, so the dashboard
// tells successive runs of the same task apart instead of reviving the old card.
// v4 adds the reporter's `library`/`library_version` (which client library and
// version is reporting) and the task `criticity` (which gates notifications).
const ProtocolVersion = 4

// ServerInfo is the handshake response returned by GET /version. A client reads
// it first, before it starts reporting, so it can tailor the protocol it uses to
// what this server supports. Name and Version identify the build; Protocol is the
// wire-contract version (see ProtocolVersion).
type ServerInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// ClientPayload is one progress update as sent in the body of POST /handler.
//
// The client always sends all fields. Numeric values decode to float64 so that
// both integer and float inputs are accepted. user_src_address arrives empty and
// is stamped by the server from the request source; key is the credential token
// used to authenticate and route the update and is never displayed.
type ClientPayload struct {
	UserHostname   string  `json:"user_hostname"`
	UserLogin      string  `json:"user_login"`
	UserSrcAddress string  `json:"user_src_address"`
	Script         string  `json:"script"`
	Progress       float64 `json:"progress"`
	Total          float64 `json:"total"`
	Description    string  `json:"description"`
	Elapsed        float64 `json:"elapsed"`
	Unit           string  `json:"unit"`
	UnitScale      bool    `json:"unit_scale"`
	Rate           float64 `json:"rate"`
	UnitDivisor    float64 `json:"unit_divisor"`
	Initial        float64 `json:"initial"`
	Colour         string  `json:"colour"`
	Key            string  `json:"key"`
	// UUID is the client-assigned identity of this task run. The client mints a
	// fresh value for each run (each tqdm instance), so when a task is restarted
	// the server sees a new uuid and opens a new dashboard card instead of
	// reviving the previous run's — which may have aged to stalled or dead. Added
	// in protocol v3; an older client omits the field and it decodes to "", in
	// which case the server falls back to the TaskKey (see InstanceKey).
	UUID string `json:"uuid"`
	// Tags are optional free-form labels the client may attach to a task (e.g.
	// "gpu", "nightly"). They are displayed as chips on the dashboard and can be
	// filtered on; they are not part of the task identity. Added in protocol v2;
	// an older client omits the field and it decodes to nil.
	Tags []string `json:"tags"`
	// Library and LibraryVersion identify the reporting client library and its
	// version — e.g. "webprogress" and "1.2.3", shown on the dashboard as the chip
	// "webprogress@1.2.3" (see LibraryLabel) and filterable there. They are display
	// metadata, not part of the task identity. Added in protocol v4; an older
	// client omits them and they decode to "".
	Library        string `json:"library"`
	LibraryVersion string `json:"library_version"`
	// Criticity is the task's importance, set by the reporter, that gates which
	// notifications fire for it (see Criticity and EffectiveCriticity). It is shown
	// as a colour-coded chip on the dashboard and filterable there. Added in
	// protocol v4; an older client omits it and it decodes to "", treated as the
	// default CriticityStandard.
	Criticity Criticity `json:"criticity"`
}

// TaskKey identifies a task on the dashboard: the triple (script, origin host,
// description). Two updates sharing this key drive the same progress indicator.
// The script is part of the identity so two scripts reporting the same
// description on the same host stay distinct tasks.
func (p ClientPayload) TaskKey() string {
	return p.Script + ":" + p.UserHostname + ":" + p.Description
}

// InstanceKey identifies one run of a task — the unit the dashboard draws as a
// single card. The client assigns a fresh UUID per run, so restarting a task
// yields a new key and therefore a new card rather than reviving the old one. A
// client too old to send a uuid falls back to the TaskKey, which cannot tell
// successive runs apart (the pre-v3 behaviour).
func (p ClientPayload) InstanceKey() string {
	if p.UUID != "" {
		return p.UUID
	}
	return p.TaskKey()
}

// Criticity is a task's importance level, assigned by the reporter, that decides
// which out-of-band notifications fire for it. It is carried on the wire (added
// in protocol v4) and shown as a colour-coded chip on the dashboard. The empty
// value is treated as CriticityStandard (see EffectiveCriticity), so a pre-v4
// client that sends nothing still gets sensible behaviour.
type Criticity string

const (
	// CriticityTrivial silences every notification for the task.
	CriticityTrivial Criticity = "trivial"
	// CriticityStandard (the default) notifies only when the task is presumed
	// dead — i.e. it went silent for longer than its dead threshold.
	CriticityStandard Criticity = "standard"
	// CriticityCritical notifies in every case: on stall, on dead, and on
	// completion.
	CriticityCritical Criticity = "critical"
)

// EffectiveCriticity returns the task's criticity, mapping the empty/unknown
// value (e.g. from a pre-v4 client) to the default CriticityStandard.
func (p ClientPayload) EffectiveCriticity() Criticity {
	switch p.Criticity {
	case CriticityTrivial, CriticityCritical:
		return p.Criticity
	default:
		return CriticityStandard
	}
}

// NotifyOnComplete reports whether a completion notification should fire for a
// task of this criticity: only CRITICAL tasks are announced on completion.
func (c Criticity) NotifyOnComplete() bool { return c == CriticityCritical }

// NotifyOnStall reports whether a (recoverable) stall notification should fire:
// only CRITICAL tasks are announced on a stall.
func (c Criticity) NotifyOnStall() bool { return c == CriticityCritical }

// NotifyOnDead reports whether a dead (presumed-gone) notification should fire:
// STANDARD and CRITICAL tasks are both announced when they die; TRIVIAL is not.
func (c Criticity) NotifyOnDead() bool {
	return c == CriticityStandard || c == CriticityCritical
}

// LibraryLabel is the reporting library shown on the dashboard, rendered as
// "name@version" (e.g. "webprogress@1.2.3"). It is the library name alone when no
// version is known, and empty when the client sent no library. Derived, never
// carried on the wire.
func (p ClientPayload) LibraryLabel() string {
	if p.Library == "" {
		return ""
	}
	if p.LibraryVersion == "" {
		return p.Library
	}
	return p.Library + "@" + p.LibraryVersion
}

// ScriptName is the group a task belongs to on the dashboard. Tasks reported
// under the same script are shown together; an unset script falls back to a
// shared "(unscripted)" group. Derived, never carried on the wire.
func (p ClientPayload) ScriptName() string {
	if p.Script == "" {
		return "(unscripted)"
	}
	return p.Script
}

// Fraction is the fill of the progress indicator in [0, 1]. It is 0 when total
// is zero (nothing to divide by yet).
func (p ClientPayload) Fraction() float64 {
	if p.Total == 0 {
		return 0
	}
	return p.Progress / p.Total
}

// RemainingTime is the estimated seconds to completion, (total-progress)/rate.
// It is only meaningful once a non-zero rate exists; with rate == 0 it reports 0.
// This value is derived, never carried on the wire.
func (p ClientPayload) RemainingTime() float64 {
	if p.Rate == 0 {
		return 0
	}
	return (p.Total - p.Progress) / p.Rate
}

// ETA is the absolute estimated completion time: now plus RemainingTime. Derived,
// never carried on the wire.
func (p ClientPayload) ETA() time.Time {
	return time.Now().UTC().Add(time.Duration(p.RemainingTime() * float64(time.Second)))
}

// Liveness multiples. A task's silence is judged against its own update cadence
// (the interval at which it normally reports, which follows from its rate): once
// it has been silent for more than StallMultiple cadences it is shown as stalled,
// and past DeadMultiple it is shown as dead and dropped from the default view.
// The cadence is observed by the server (see server.cadenceTracker), not
// advertised by the client.
const (
	StallMultiple = 2
	DeadMultiple  = 10
)

// TaskStatus is a task's lifecycle state as shown on the dashboard. It is
// derived from the task's fraction and how long it has gone silent relative to
// its update cadence; it is never carried on the wire.
type TaskStatus string

const (
	StatusRunning  TaskStatus = "running"
	StatusFinished TaskStatus = "finished"
	StatusStalled  TaskStatus = "stalled"
	StatusDead     TaskStatus = "dead"
)

// Status returns the task's lifecycle state given how long it has been silent
// (idle = now − last update) and the stall/dead thresholds derived from its
// update cadence. A finished task (fraction ≥ 1) stays finished regardless of
// silence; zero thresholds mean liveness cannot be judged yet, so the task is
// only ever running or finished. This mirrors the per-second sweep the dashboard
// runs in the browser.
func (p ClientPayload) Status(idle, stall, dead time.Duration) TaskStatus {
	if p.Fraction() >= 1 {
		return StatusFinished
	}
	if dead > 0 && idle >= dead {
		return StatusDead
	}
	if stall > 0 && idle >= stall {
		return StatusStalled
	}
	return StatusRunning
}

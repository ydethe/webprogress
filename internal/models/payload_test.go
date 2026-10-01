package models

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// pythonBody is a payload as the Python tqdm client serializes it (model_dump),
// used to pin the wire contract.
const pythonBody = `{
  "user_hostname": "host-a",
  "user_login": "yann",
  "user_src_address": "",
  "script": "ingest.py",
  "progress": 25,
  "total": 100,
  "description": "download",
  "elapsed": 5.0,
  "unit": "it",
  "unit_scale": false,
  "rate": 5.0,
  "unit_divisor": 1000,
  "initial": 0,
  "colour": "#0000ff",
  "key": "wbk_secret"
}`

func TestUnmarshalWireContract(t *testing.T) {
	var p ClientPayload
	if err := json.Unmarshal([]byte(pythonBody), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.UserHostname != "host-a" || p.UserLogin != "yann" {
		t.Errorf("identity fields: %+v", p)
	}
	if p.Progress != 25 || p.Total != 100 {
		t.Errorf("progress/total: %v/%v", p.Progress, p.Total)
	}
	if p.UnitScale != false || p.Unit != "it" || p.UnitDivisor != 1000 {
		t.Errorf("unit fields: %+v", p)
	}
	if p.Colour != "#0000ff" || p.Key != "wbk_secret" {
		t.Errorf("colour/key: %q %q", p.Colour, p.Key)
	}
	if p.Script != "ingest.py" {
		t.Errorf("script: %q", p.Script)
	}
	// A pre-v2 client omits tags entirely; the field decodes to nil.
	if p.Tags != nil {
		t.Errorf("tags should be nil when absent, got %v", p.Tags)
	}
}

func TestStatus(t *testing.T) {
	// Zero thresholds (cadence not yet known): never stalls or dies.
	running := ClientPayload{Progress: 10, Total: 100}
	if got := running.Status(time.Hour, 0, 0); got != StatusRunning {
		t.Errorf("silent task with no thresholds = %q, want running", got)
	}
	if got := (ClientPayload{Progress: 100, Total: 100}).Status(0, 0, 0); got != StatusFinished {
		t.Errorf("full task = %q, want finished", got)
	}

	// With a 20s stall / 100s dead threshold (e.g. a 10s cadence).
	p := ClientPayload{Progress: 10, Total: 100}
	stall, dead := 20*time.Second, 100*time.Second
	cases := []struct {
		idle time.Duration
		want TaskStatus
	}{
		{5 * time.Second, StatusRunning},
		{20 * time.Second, StatusStalled},
		{99 * time.Second, StatusStalled},
		{100 * time.Second, StatusDead},
		{2 * time.Minute, StatusDead},
	}
	for _, c := range cases {
		if got := p.Status(c.idle, stall, dead); got != c.want {
			t.Errorf("Status(idle=%s) = %q, want %q", c.idle, got, c.want)
		}
	}

	// A finished task stays finished however long it is silent.
	done := ClientPayload{Progress: 100, Total: 100}
	if got := done.Status(time.Hour, stall, dead); got != StatusFinished {
		t.Errorf("finished silent task = %q, want finished", got)
	}
}

func TestUnmarshalTags(t *testing.T) {
	var p ClientPayload
	body := `{"user_hostname":"h","script":"s","description":"d","tags":["gpu","nightly"],"key":"k"}`
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "gpu" || p.Tags[1] != "nightly" {
		t.Errorf("tags = %v, want [gpu nightly]", p.Tags)
	}
}

func TestUnmarshalUUIDAndInstanceKey(t *testing.T) {
	var p ClientPayload
	body := `{"user_hostname":"h","script":"s","description":"d","uuid":"run-123","key":"k"}`
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// When the client sends a uuid, that is the run's identity.
	if p.UUID != "run-123" || p.InstanceKey() != "run-123" {
		t.Errorf("uuid=%q InstanceKey=%q, want run-123", p.UUID, p.InstanceKey())
	}
	// A pre-v3 client omits uuid; InstanceKey falls back to the TaskKey.
	old := ClientPayload{Script: "s", UserHostname: "h", Description: "d"}
	if old.UUID != "" || old.InstanceKey() != "s:h:d" {
		t.Errorf("fallback InstanceKey = %q, want the TaskKey s:h:d", old.InstanceKey())
	}
}

func TestTaskKeyAndFraction(t *testing.T) {
	p := ClientPayload{Script: "s", UserHostname: "h", Description: "d", Progress: 30, Total: 120}
	if p.TaskKey() != "s:h:d" {
		t.Errorf("TaskKey = %q", p.TaskKey())
	}
	// An unset script falls back to a shared group but keeps a distinct key.
	if (ClientPayload{UserHostname: "h", Description: "d"}).ScriptName() != "(unscripted)" {
		t.Error("empty script should group under (unscripted)")
	}
	if p.Fraction() != 0.25 {
		t.Errorf("Fraction = %v", p.Fraction())
	}
	// Zero total yields an empty bar rather than a division by zero.
	if (ClientPayload{Total: 0, Progress: 5}).Fraction() != 0 {
		t.Error("Fraction with total 0 should be 0")
	}
}

func TestRemainingTime(t *testing.T) {
	p := ClientPayload{Progress: 20, Total: 100, Rate: 4}
	if got := p.RemainingTime(); math.Abs(got-20) > 1e-9 {
		t.Errorf("RemainingTime = %v, want 20", got)
	}
	// Rate 0 is reported as 0, not +Inf.
	if (ClientPayload{Total: 100, Rate: 0}).RemainingTime() != 0 {
		t.Error("RemainingTime with rate 0 should be 0")
	}
}

package models

import (
	"encoding/json"
	"math"
	"testing"
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

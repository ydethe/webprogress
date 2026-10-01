// Package models holds the shared wire contract between the progress reporter
// (the Python tqdm client) and this server. The JSON field names here must match
// exactly what the client serializes; changing one is a contract change that
// affects both sides at once.
package models

import "time"

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
}

// TaskKey identifies a task on the dashboard: the pair (origin host, description).
// Two updates sharing this key drive the same progress indicator.
func (p ClientPayload) TaskKey() string {
	return p.UserHostname + ":" + p.Description
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

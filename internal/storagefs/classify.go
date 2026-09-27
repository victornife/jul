// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package storagefs

// State is the bounded advisory headroom state of one storage category.
type State string

const (
	StateOK          State = "ok"
	StateLow         State = "low"
	StateCritical    State = "critical"
	StateUnavailable State = "unavailable"
	StateError       State = "error"
)

// Hints are generic local guidance thresholds on the available fraction of a
// filesystem. They are operator hints, not SLOs: 10% of a 4 TB volume is a
// lot of room, 10% of a 64 MB tmpfs is seconds of access logging. They never
// affect readiness or any write path.
type Hints struct {
	LowRatio      float64
	CriticalRatio float64
}

// DefaultHints are the documented generic hints: low below 10% available,
// critical below 5% available.
var DefaultHints = Hints{LowRatio: 0.10, CriticalRatio: 0.05}

// Classify maps a probe result to its advisory state and, only when both the
// available and a positive total are real, the available ratio.
func (h Hints) Classify(r Result) (State, *float64) {
	switch r.Reason {
	case ReasonNone:
	case ReasonPermissionDenied, ReasonStatFailed:
		return StateError, nil
	default:
		return StateUnavailable, nil
	}
	if !r.HaveAvailable || !r.HaveTotal || r.Total == 0 {
		return StateUnavailable, nil
	}
	ratio := float64(r.Available) / float64(r.Total)
	switch {
	case ratio < h.CriticalRatio:
		return StateCritical, &ratio
	case ratio < h.LowRatio:
		return StateLow, &ratio
	default:
		return StateOK, &ratio
	}
}

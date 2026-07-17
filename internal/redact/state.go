// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package redact

import (
	"io"
	"strings"
)

// State is an immutable, self-contained redaction configuration: the set of
// secret values to mask and the minimum length a value must have to be worth
// masking. It is returned by secret resolution and installed as the live global
// state only at the reload commit boundary, so validation/preflight cannot
// mutate the serving redaction behavior.
type State struct {
	values map[string]struct{}
	minLen int
}

// NewState returns a redaction state that masks the supplied values and treats
// values shorter than minLen as non-secret. A minLen below 1 is normalized to
// DefaultMinLen.
func NewState(values []string, minLen int) State {
	if minLen < 1 {
		minLen = DefaultMinLen
	}
	m := make(map[string]struct{}, len(values))
	for _, v := range values {
		if len(v) >= minLen {
			m[v] = struct{}{}
		}
	}
	return State{values: m, minLen: minLen}
}

// EmptyState returns a redaction state with no registered secrets and the
// default minimum length.
func EmptyState() State {
	return State{values: map[string]struct{}{}, minLen: DefaultMinLen}
}

// WithValue returns a new State that also masks value. Values shorter than the
// state's minimum length are ignored.
func (s State) WithValue(value string) State {
	if len(value) < s.minLen {
		return s
	}
	out := s.Clone()
	out.values[value] = struct{}{}
	return out
}

// WithMinLen returns a new State with the given minimum length. Existing values
// shorter than the new floor remain in the registry (they were already deemed
// secret under a stricter policy), but future values added via WithValue will
// be filtered by the new floor.
func (s State) WithMinLen(minLen int) State {
	if minLen < 1 {
		minLen = DefaultMinLen
	}
	out := s.Clone()
	out.minLen = minLen
	return out
}

// Apply returns input with every registered secret value replaced by Mask.
func (s State) Apply(input string) string {
	if len(s.values) == 0 {
		return input
	}
	for v := range s.values {
		if strings.Contains(input, v) {
			input = strings.ReplaceAll(input, v, Mask)
		}
	}
	return input
}

// Writer returns an io.Writer that masks registered secrets before forwarding
// to w. It always reports len(p) bytes consumed so callers that check n ==
// len(p) remain satisfied even when masking shortens the output.
func (s State) Writer(w io.Writer) io.Writer {
	return &stateRedactingWriter{state: s, w: w}
}

// Count returns the number of distinct registered secret values.
func (s State) Count() int {
	return len(s.values)
}

// MinLen returns the state's minimum maskable length.
func (s State) MinLen() int {
	return s.minLen
}

// Clone returns a deep copy of the state.
func (s State) Clone() State {
	out := State{minLen: s.minLen}
	if len(s.values) == 0 {
		out.values = map[string]struct{}{}
	} else {
		out.values = make(map[string]struct{}, len(s.values))
		for k := range s.values {
			out.values[k] = struct{}{}
		}
	}
	return out
}

type stateRedactingWriter struct {
	state State
	w     io.Writer
}

func (rw *stateRedactingWriter) Write(p []byte) (int, error) {
	out := rw.state.Apply(string(p))
	if out == string(p) {
		return rw.w.Write(p)
	}
	if _, err := rw.w.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(p), nil
}

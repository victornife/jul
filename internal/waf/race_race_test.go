// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build race

package waf

// raceEnabled reports whether the race detector instruments this build; its
// shadow allocations make retained-heap calibration meaningless.
const raceEnabled = true

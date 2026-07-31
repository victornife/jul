// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build race

package app

// raceTimeScale widens the sub-second wall-clock budgets in the reload-timeout
// tests when the race detector is active. Those tests assert *relative* timing
// (one shared budget vs a reset budget, preflight-vs-reload attribution), which
// is preserved when every budget in a test scales by the same factor; the
// detector's ~10x scheduling overhead otherwise blows through windows
// calibrated for un-instrumented execution and turns a correct run into a
// spurious timeout. A no-op (== 1) build is provided for normal runs.
const raceTimeScale = 12

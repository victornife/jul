// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !race

package app

// raceTimeScale is 1 in normal (non-race) builds, so the reload-timeout tests
// keep their fast, tight wall-clock budgets. See racescale_race_test.go.
const raceTimeScale = 1

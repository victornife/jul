// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"

	"jul/internal/inspection"
)

type result struct {
	Kind       string `json:"kind"`
	GoVersion  string `json:"go_version,omitempty"`
	GOOS       string `json:"goos,omitempty"`
	GOARCH     string `json:"goarch,omitempty"`
	Repetition int    `json:"repetition"`
	Leases     int    `json:"leases"`
	Before     uint64 `json:"heap_alloc_before_bytes"`
	After      uint64 `json:"heap_alloc_after_bytes"`
	Delta      int64  `json:"heap_alloc_delta_bytes"`
}

var measurementLeaseCounts = []int{0, 256, 1024, 4096}
var measurementRepetitions = 7

func main() {
	if err := run(os.Stdout, measurementLeaseCounts, measurementRepetitions); err != nil {
		panic(err)
	}
}

func run(out io.Writer, leaseCounts []int, repetitions int) error {
	enc := json.NewEncoder(out)
	if err := enc.Encode(result{
		Kind:      "environment",
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}); err != nil {
		return fmt.Errorf("encode environment: %w", err)
	}

	for _, leases := range leaseCounts {
		for repetition := 1; repetition <= repetitions; repetition++ {
			before, after, err := measure(leases)
			if err != nil {
				return fmt.Errorf("measure %d leases: %w", leases, err)
			}
			delta := int64(after) - int64(before)
			if err := enc.Encode(result{
				Kind:       "sample",
				Repetition: repetition,
				Leases:     leases,
				Before:     before,
				After:      after,
				Delta:      delta,
			}); err != nil {
				return fmt.Errorf("encode sample: %w", err)
			}
		}
	}
	return nil
}

func measure(count int) (uint64, uint64, error) {
	limits := inspection.Limits{
		Mode:                      inspection.Observe,
		MaxBytes:                  1 << 30,
		MaxResponseHeaderBytes:    64 << 10,
		ConfiguredEvaluationSlots: 1,
		EvaluationSlots:           1,
		BaseChargeBytes:           inspection.BaseChargeBytes,
	}
	governor, err := inspection.New(limits, inspection.Hooks{})
	if err != nil {
		return 0, 0, fmt.Errorf("create governor: %w", err)
	}

	// Allocate the retention scaffolding before the baseline so the measured
	// delta excludes the harness's two slices while keeping every lease live.
	contexts := make([]context.Context, count)
	held := make([]*inspection.Lease, count)
	runtime.GC()
	debug.FreeOSMemory()
	var beforeStats runtime.MemStats
	runtime.ReadMemStats(&beforeStats)

	for i := 0; i < count; i++ {
		lease, ctx, owner, err := governor.AcquireLease(context.Background())
		if err != nil {
			return 0, 0, fmt.Errorf("acquire lease %d: %w", i, err)
		}
		if !owner {
			return 0, 0, fmt.Errorf("lease %d unexpectedly reused an owner", i)
		}
		held[i] = lease
		contexts[i] = ctx
	}

	runtime.GC()
	debug.FreeOSMemory()
	var afterStats runtime.MemStats
	runtime.ReadMemStats(&afterStats)
	runtime.KeepAlive(contexts)
	runtime.KeepAlive(held)
	runtime.KeepAlive(governor)
	return beforeStats.HeapAlloc, afterStats.HeapAlloc, nil
}

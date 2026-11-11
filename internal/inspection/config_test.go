// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"jul/internal/config"
)

func TestLimitsFromConfigDefaultsAndAutoSlots(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		startup     int
		wantSlots   int
		wantMode    Mode
		wantTimeout time.Duration
		wantBody    time.Duration
	}{
		{
			name:        "defaults",
			cfg:         &config.Config{},
			startup:     3,
			wantSlots:   3,
			wantMode:    Enforce,
			wantTimeout: time.Second,
			wantBody:    60 * time.Second,
		},
		{
			name: "worker threads override startup auto",
			cfg: &config.Config{Global: config.GlobalConfig{
				WorkerThreads: "7",
				Inspection: config.InspectionConfig{
					MaxConcurrentEvaluations: 0,
					EvaluationQueueTimeout:   durationPointer(0),
					BodyTimeout:              durationPointer(0),
				},
			}},
			startup:     3,
			wantSlots:   7,
			wantMode:    Enforce,
			wantTimeout: 0,
			wantBody:    0,
		},
		{
			name: "explicit slots are independent",
			cfg: &config.Config{Global: config.GlobalConfig{
				WorkerThreads: "9",
				Inspection: config.InspectionConfig{
					Mode:                     "observe",
					MaxConcurrentEvaluations: 2,
				},
			}},
			startup:     3,
			wantSlots:   2,
			wantMode:    Observe,
			wantTimeout: time.Second,
			wantBody:    60 * time.Second,
		},
		{
			name: "auto uses startup procs for worker auto",
			cfg: &config.Config{Global: config.GlobalConfig{
				WorkerThreads: "auto",
				Inspection: config.InspectionConfig{
					MaxConcurrentEvaluations: 0,
				},
			}},
			startup:     5,
			wantSlots:   5,
			wantMode:    Enforce,
			wantTimeout: time.Second,
			wantBody:    60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits, err := LimitsFromConfig(tt.cfg, tt.startup)
			if err != nil {
				t.Fatal(err)
			}
			if limits.EvaluationSlots != tt.wantSlots || limits.ConfiguredEvaluationSlots != tt.cfg.Global.Inspection.MaxConcurrentEvaluations {
				t.Fatalf("slots configured/effective = %d/%d, want %d/%d",
					limits.ConfiguredEvaluationSlots, limits.EvaluationSlots,
					tt.cfg.Global.Inspection.MaxConcurrentEvaluations, tt.wantSlots)
			}
			if limits.Mode != tt.wantMode || limits.EvaluationQueueTimeout != tt.wantTimeout || limits.BodyTimeout != tt.wantBody {
				t.Fatalf("resolved limits = %+v", limits)
			}
			if limits.MaxBytes != 256<<20 || limits.BaseChargeBytes != BaseChargeBytes {
				t.Fatalf("default byte limits = %+v", limits)
			}
			if limits.MaxResponseHeaderBytes != uint64(config.InspectionDefaultMaxResponseHeaderBytes) {
				t.Fatalf("default response-header ceiling = %d, want %d", limits.MaxResponseHeaderBytes, config.InspectionDefaultMaxResponseHeaderBytes)
			}
		})
	}
}

func TestLimitsFromConfigRejectsInvalidInputs(t *testing.T) {
	if _, err := LimitsFromConfig(nil, 1); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("nil config error = %v", err)
	}
	if _, err := LimitsFromConfig(&config.Config{}, 0); err != nil {
		t.Fatalf("runtime GOMAXPROCS fallback: %v", err)
	} else if runtime.GOMAXPROCS(0) < 1 {
		t.Fatal("runtime GOMAXPROCS must be positive")
	}
	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "invalid mode",
			cfg: &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
				Mode: "invalid",
			}}},
		},
		{
			name: "invalid worker threads",
			cfg: &config.Config{Global: config.GlobalConfig{
				WorkerThreads: "not-an-int",
				Inspection: config.InspectionConfig{
					MaxConcurrentEvaluations: 0,
				},
			}},
		},
		{
			name: "zero max bytes",
			cfg: &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
				MaxBufferedBytes: sizePointer(0),
			}}},
		},
		{
			name: "zero response-header ceiling",
			cfg: &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
				MaxResponseHeaderBytes: sizePointer(0),
			}}},
		},
		{
			name: "response-header estimate overflow",
			cfg: &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
				MaxResponseHeaderBytes: sizePointer(int64(^uint64(0) >> 1)),
			}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LimitsFromConfig(tc.cfg, 2); !errors.Is(err, ErrInvalidLimits) {
				t.Fatalf("LimitsFromConfig() error = %v", err)
			}
		})
	}
	enforceTooSmall := &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
		MaxBufferedBytes: sizePointer(1024),
	}}}
	if _, err := LimitsFromConfig(enforceTooSmall, 1); err == nil ||
		!errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("enforce accepted a budget below the known base + R3 minimum: %v", err)
	}
	observeTooSmall := &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
		Mode:             "observe",
		MaxBufferedBytes: sizePointer(1024),
	}}}
	if limits, err := LimitsFromConfig(observeTooSmall, 1); err != nil || limits.MaxBytes != 1024 {
		t.Fatalf("observe rejected a below-base measurement budget: limits=%+v err=%v", limits, err)
	}

	minimum := config.InspectionBaseChargeBytes +
		config.InspectionResponseHeaderEstimateFactor*config.InspectionDefaultMaxResponseHeaderBytes
	exactMinimum := &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
		MaxBufferedBytes: sizePointer(minimum),
	}}}
	if limits, err := LimitsFromConfig(exactMinimum, 1); err != nil || limits.MaxBytes != uint64(minimum) {
		t.Fatalf("enforce rejected exact known base + R3 minimum: limits=%+v err=%v", limits, err)
	}
}

func TestResponseHeaderCeilingSetsKnownR3MinimumWithoutChangingBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		headerBytes int64
		wantR3      uint64
	}{
		{name: "default 64 KiB", headerBytes: 64 << 10, wantR3: 3 << 20},
		{name: "raised 256 KiB", headerBytes: 256 << 10, wantR3: 12 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			minimum := config.InspectionBaseChargeBytes +
				config.InspectionResponseHeaderEstimateFactor*tc.headerBytes
			cfg := &config.Config{Global: config.GlobalConfig{Inspection: config.InspectionConfig{
				MaxBufferedBytes:       sizePointer(256 << 20),
				MaxResponseHeaderBytes: sizePointer(tc.headerBytes),
			}}}
			limits, err := LimitsFromConfig(cfg, 2)
			if err != nil {
				t.Fatal(err)
			}
			if limits.MaxBytes != 256<<20 || limits.MaxResponseHeaderBytes != uint64(tc.headerBytes) {
				t.Fatalf("raising response-header ceiling changed the aggregate budget: %+v", limits)
			}
			if got := uint64(config.InspectionResponseHeaderEstimateFactor) * limits.MaxResponseHeaderBytes; got != tc.wantR3 {
				t.Fatalf("maximum R3 estimate = %d, want %d", got, tc.wantR3)
			}

			cfg.Global.Inspection.MaxBufferedBytes = sizePointer(minimum)
			limits, err = LimitsFromConfig(cfg, 2)
			if err != nil || limits.MaxBytes != uint64(minimum) {
				t.Fatalf("exact base + R3 minimum rejected: limits=%+v err=%v", limits, err)
			}
		})
	}
}

func durationPointer(value time.Duration) *config.Duration {
	d := config.Duration(value)
	return &d
}

func sizePointer(value int64) *config.Size {
	s := config.Size(value)
	return &s
}

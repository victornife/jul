// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"
)

const BaseChargeBytes uint64 = uint64(config.InspectionBaseChargeBytes)

// LimitsFromConfig resolves configured auto slots against the candidate
// worker_threads value. The caller supplies the container-aware startup
// GOMAXPROCS value used when worker_threads is auto or omitted.
func LimitsFromConfig(cfg *config.Config, startupProcs int) (Limits, error) {
	if cfg == nil {
		return Limits{}, fmt.Errorf("%w: config is nil", ErrInvalidLimits)
	}
	if startupProcs <= 0 {
		startupProcs = runtime.GOMAXPROCS(0)
	}
	inspection := cfg.Global.Inspection
	mode := Mode(strings.TrimSpace(inspection.Mode))
	if mode == "" {
		mode = Enforce
	}
	maxBytes := uint64(256 << 20)
	if inspection.MaxBufferedBytes != nil {
		if *inspection.MaxBufferedBytes <= 0 {
			return Limits{}, fmt.Errorf("%w: global.inspection.max_buffered_bytes must be positive", ErrInvalidLimits)
		}
		maxBytes = uint64(inspection.MaxBufferedBytes.Bytes())
	}
	maxResponseHeaderBytes := uint64(config.InspectionDefaultMaxResponseHeaderBytes)
	if inspection.MaxResponseHeaderBytes != nil {
		if *inspection.MaxResponseHeaderBytes <= 0 {
			return Limits{}, fmt.Errorf("%w: global.inspection.max_response_header_bytes must be positive", ErrInvalidLimits)
		}
		maxResponseHeaderBytes = uint64(inspection.MaxResponseHeaderBytes.Bytes())
	}
	queueTimeout := time.Second
	if inspection.EvaluationQueueTimeout != nil {
		queueTimeout = inspection.EvaluationQueueTimeout.Std()
	}
	bodyTimeout := 60 * time.Second
	if inspection.BodyTimeout != nil {
		bodyTimeout = inspection.BodyTimeout.Std()
	}
	minimumBytes, err := config.InspectionMinimumKnownBudget(int64(maxResponseHeaderBytes))
	if err != nil {
		return Limits{}, fmt.Errorf("%w: global.inspection.max_response_header_bytes: %v", ErrInvalidLimits, err)
	}
	if mode == Enforce && maxBytes < uint64(minimumBytes) {
		return Limits{}, fmt.Errorf("%w: global.inspection.max_buffered_bytes is %d B; enforce mode requires at least %d B for the known base + R3 minimum", ErrInvalidLimits, maxBytes, minimumBytes)
	}
	effectiveSlots := inspection.MaxConcurrentEvaluations
	if effectiveSlots == 0 {
		effectiveSlots = startupProcs
		workerThreads := strings.TrimSpace(cfg.Global.WorkerThreads)
		if workerThreads != "" && workerThreads != "auto" {
			n, err := strconv.Atoi(workerThreads)
			if err != nil || n <= 0 {
				return Limits{}, fmt.Errorf("%w: invalid worker_threads value %q", ErrInvalidLimits, workerThreads)
			}
			effectiveSlots = n
		}
	}
	limits := Limits{
		Mode:                      mode,
		MaxBytes:                  maxBytes,
		MaxResponseHeaderBytes:    maxResponseHeaderBytes,
		ConfiguredEvaluationSlots: inspection.MaxConcurrentEvaluations,
		EvaluationSlots:           effectiveSlots,
		EvaluationQueueTimeout:    queueTimeout,
		BodyTimeout:               bodyTimeout,
		BaseChargeBytes:           BaseChargeBytes,
	}
	if err := limits.validate(); err != nil {
		return Limits{}, err
	}
	return limits, nil
}

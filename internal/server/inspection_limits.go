// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"fmt"

	"jul/internal/inspection"
	"jul/internal/lifecycle"
)

type inspectionLimitsComponent struct {
	governor *inspection.Governor
	limits   inspection.Limits
}

func (c *inspectionLimitsComponent) component() RuntimeComponent {
	return ComponentInspectionLimits
}

func (c *inspectionLimitsComponent) commit() retirement {
	if err := c.governor.PublishLimits(c.limits); err != nil {
		panic(fmt.Sprintf("server: prepared inspection limits became invalid: %v", err))
	}
	return nil
}

func (c *inspectionLimitsComponent) abort() {}

// withRouteMinimum attaches generation genID's route-aware minimum budget to
// limits and validates the result, so a too-small enforce budget is refused
// before publication (ADR 0032 §7).
func (s *Server) withRouteMinimum(limits inspection.Limits, genID uint64) (inspection.Limits, error) {
	if s.InspectionRequirement != nil {
		if minimum, route, ok := s.InspectionRequirement(genID); ok {
			limits.MinimumBytes = minimum
			limits.MinimumRoute = route
		}
	}
	if err := limits.Validate(); err != nil {
		return inspection.Limits{}, err
	}
	return limits, nil
}

// publishStartupInspectionLimits republishes the startup limits with the
// initial generation's route minimum attached.
func (s *Server) publishStartupInspectionLimits(genID uint64) error {
	if s.Inspection == nil || s.InspectionRequirement == nil {
		return nil
	}
	limits, err := inspection.LimitsFromConfig(s.cfg, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		return err
	}
	if limits, err = s.withRouteMinimum(limits, genID); err != nil {
		return err
	}
	return s.Inspection.PublishLimits(limits)
}

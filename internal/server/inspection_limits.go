// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"fmt"

	"jul/internal/inspection"
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

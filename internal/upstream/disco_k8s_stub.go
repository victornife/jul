// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !kubernetes

package upstream

import (
	"fmt"

	"jul/internal/config"
)

// newKubernetesDiscoverer rejects a Kubernetes discovery config in a build
// without the "kubernetes" tag, the same model as other gated features: the
// reload or startup that referenced it fails with a clear, actionable error.
func newKubernetesDiscoverer(config.DiscoveryConfig) (Discoverer, error) {
	return nil, fmt.Errorf("kubernetes discovery requires a build with the \"kubernetes\" tag")
}

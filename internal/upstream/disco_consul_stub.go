//go:build !consul

package upstream

import (
	"fmt"

	"jul/internal/config"
)

// newConsulDiscoverer rejects a Consul discovery config in a build without the
// "consul" tag, the same model as other gated features: the reload or startup
// that referenced it fails with a clear, actionable error.
func newConsulDiscoverer(config.DiscoveryConfig) (Discoverer, error) {
	return nil, fmt.Errorf("consul discovery requires a build with the \"consul\" tag")
}

//go:build !acme

package server

import (
	"fmt"
	"time"

	"jul/internal/config"
)

// ACMECompiled reports whether this binary includes the ACME implementation.
// It is false in builds without the "acme" build tag.
const ACMECompiled = false

// NewACMEManager returns a clear error when any server block enables ACME but
// this binary was built without the "acme" build tag. When no block enables
// ACME it returns (nil, nil) so static-TLS and plain-HTTP configurations build
// and run normally. The signature matches the acme-tagged build so the
// composition root is identical regardless of build tags.
func NewACMEManager(servers []config.ServerConfig, _ func(domain string, notAfter time.Time)) (ACMEManager, error) {
	for _, srv := range servers {
		if srv.TLS != nil && srv.TLS.Enabled && srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			return nil, fmt.Errorf("tls.acme is enabled but this build was compiled without the %q build tag (rebuild with -tags acme)", "acme")
		}
	}
	return nil, nil
}

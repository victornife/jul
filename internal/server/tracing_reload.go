package server

import "jul/internal/config"

// TracingRestartRequired reports whether moving from old to next changes the
// tracing configuration, which cannot take effect via hot reload. The
// OpenTelemetry exporter pipeline and the global tracing seam are wired once at
// startup (see internal/observability), so doReload keeps the running tracer:
// a changed [observability.tracing] block would persist but never rewire the
// tracer until a restart. Mirroring ACMERestartRequired, the apply path rejects
// such a change with restart_required rather than accepting it silently, so the
// console's "applied" stays honest.
//
// TracingConfig is all comparable scalar fields, so a direct comparison detects
// any change; this matches the equality check doReload uses to log its
// defense-in-depth warning for reloads that bypass the admin gate (a direct file
// edit followed by SIGHUP).
func TracingRestartRequired(old, next *config.Config) (string, bool) {
	if old.Observability.Tracing == next.Observability.Tracing {
		return "", false
	}
	return "tracing settings changed; the OpenTelemetry tracer is wired once at startup and takes effect on restart", true
}

//go:build !otel

package observability

import (
	"context"
	"errors"
	"net/http"

	"jul/internal/config"
)

// TracingCompiled reports whether OpenTelemetry tracing is built into this
// binary. It is false in the default build; rebuild with `-tags otel` to
// compile the tracing pipeline.
const TracingCompiled = false

// Tracer is the no-op stand-in used when the `otel` build tag is absent. It
// keeps the composition root and middleware chain identical across builds.
type Tracer struct{}

// NewTracer reports an error when tracing is enabled in a binary built without
// the `otel` tag, mirroring how compression rejects an encoder that was not
// compiled in. A disabled config yields an inert Tracer.
func NewTracer(cfg config.TracingConfig) (*Tracer, error) {
	if cfg.Enabled {
		return nil, errors.New("[observability.tracing] enabled but this binary was built without the 'otel' build tag")
	}
	return &Tracer{}, nil
}

// Middleware is a pass-through in builds without tracing.
func (t *Tracer) Middleware(next http.Handler) http.Handler { return next }

// Shutdown is a no-op in builds without tracing.
func (t *Tracer) Shutdown(context.Context) error { return nil }

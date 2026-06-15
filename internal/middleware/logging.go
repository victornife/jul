package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// AccessRecord is the data captured for one completed request. It is the stable
// contract of the access-log seam: every sink receives the record by value, so
// new sinks consume structured fields rather than a pre-formatted line. Current
// and planned sinks: the slog text/JSON writer (default); file/syslog writers
// with rotation (Y1-10); the Console log-tail ring buffer (Y2-09); the audit
// mirror (Y3-08). Add fields here (never remove) as later sinks need them.
type AccessRecord struct {
	Time      time.Time
	Method    string
	Host      string
	Path      string
	Query     string
	Status    int
	Bytes     int64
	Duration  time.Duration
	Remote    string
	RequestID string
	TraceID   string
	UserAgent string
	Proto     string
}

// AccessSink consumes completed access-log records. The access-log middleware
// calls Log from every request goroutine, so implementations must be safe for
// concurrent use and should not block (buffer or drop instead) — a slow sink
// otherwise backs up request handling.
type AccessSink interface {
	Log(AccessRecord)
}

// SlogSink writes access records to an slog.Logger, reproducing the structured
// "access" line the server has always emitted. It is the default sink.
type SlogSink struct {
	log *slog.Logger
}

// NewSlogSink returns a Sink that logs each record at info level on log.
func NewSlogSink(log *slog.Logger) *SlogSink { return &SlogSink{log: log} }

// Log emits one structured access line. slog supplies its own time and level,
// so those are not duplicated from the record. The trace_id field is appended
// only when tracing populated one, so logs stay clean when tracing is off.
func (s *SlogSink) Log(rec AccessRecord) {
	attrs := []any{
		"method", rec.Method,
		"host", rec.Host,
		"path", rec.Path,
		"query", rec.Query,
		"status", rec.Status,
		"bytes", rec.Bytes,
		"duration_ms", float64(rec.Duration.Microseconds()) / 1000.0,
		"remote", rec.Remote,
		"request_id", rec.RequestID,
		"user_agent", rec.UserAgent,
	}
	if rec.TraceID != "" {
		attrs = append(attrs, "trace_id", rec.TraceID)
	}
	s.log.Info("access", attrs...)
}

// AccessLog returns middleware that builds one AccessRecord per completed
// request and fans it out to every sink. Sinks are the pluggable seam: the
// composition root assembles the set (slog today; Console ring and audit mirror
// later) and passes it here. With no sinks the middleware is a pass-through.
func AccessLog(sinks ...AccessSink) Middleware {
	return func(next http.Handler) http.Handler {
		if len(sinks) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := newRecorder(w)

			next.ServeHTTP(rec, r)

			record := AccessRecord{
				Time:      start,
				Method:    r.Method,
				Host:      r.Host,
				Path:      r.URL.Path,
				Query:     r.URL.RawQuery,
				Status:    rec.status,
				Bytes:     rec.bytes,
				Duration:  time.Since(start),
				Remote:    clientIP(r),
				RequestID: RequestIDFrom(r.Context()),
				TraceID:   TraceIDFrom(r.Context()),
				UserAgent: r.UserAgent(),
				Proto:     r.Proto,
			}
			for _, s := range sinks {
				s.Log(record)
			}
		})
	}
}

// clientIP extracts the remote IP without the port.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

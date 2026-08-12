// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"jul/internal/clientaddr"
)

// AccessRecord is the data captured for one completed request. It is the stable
// contract of the access-log seam: every sink receives the record by value, so
// new sinks consume structured fields rather than a pre-formatted line. Current
// and planned sinks: the slog text/JSON writer (default); file/syslog writers
// with rotation (Y1-10); the Console log-tail ring buffer (Y2-09); the audit
// mirror (Y3-08). Add fields here as later sinks need them; removing or
// repurposing one changes every sink's output, so it is a reviewed change.
type AccessRecord struct {
	Time     time.Time
	Method   string
	Host     string
	Path     string
	Query    string
	Status   int
	Bytes    int64
	Duration time.Duration
	// Client is the canonical client address derived by internal/clientaddr:
	// the transport peer unless the listener trusts the peer as a proxy.
	Client string
	// Peer is the direct transport peer. It equals Client for a direct client
	// and differs only when a trusted proxy asserted the client address.
	Peer      string
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
		"client_ip", rec.Client,
		"request_id", rec.RequestID,
		"user_agent", rec.UserAgent,
	}
	// peer_ip is emitted only when a trusted proxy actually changed the answer,
	// following the same "omit when it adds nothing" rule as trace_id.
	if rec.Peer != "" && rec.Peer != rec.Client {
		attrs = append(attrs, "peer_ip", rec.Peer)
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
			rec := NewRecorder(w)

			next.ServeHTTP(rec.Writer(), r)

			record := AccessRecord{
				Time:      start,
				Method:    r.Method,
				Host:      r.Host,
				Path:      r.URL.Path,
				Query:     r.URL.RawQuery,
				Status:    rec.Status(),
				Bytes:     rec.Bytes(),
				Duration:  time.Since(start),
				Client:    addrText(clientaddr.Client(r)),
				Peer:      addrText(clientaddr.Peer(r)),
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

// addrText renders an address for the log, returning "" for an address that
// could not be identified rather than a placeholder that reads like one.
func addrText(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

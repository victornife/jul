// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// CompressionOptions configures the Compression middleware. It is populated
// from config.CompressionConfig by the caller so this package stays free of a
// dependency on the config package.
type CompressionOptions struct {
	// Encoders lists allowed content codings in server-preference order. Each
	// must be a registered encoder ("gzip" always; "br"/"zstd" via build tags).
	Encoders []string
	// Level is the encoder compression level; 0 selects the encoder default.
	Level int
	// MinSize is the smallest response body (in bytes) that is compressed.
	MinSize int64
	// Types is the MIME allow-list matched against the response Content-Type.
	Types []string
	// OnCompress, when non-nil, is invoked with the chosen encoding each time a
	// response is compressed (used to drive metrics without importing them).
	OnCompress func(encoding string)
}

// encoder is a resettable streaming compressor. *gzip.Writer, *brotli.Writer
// and *zstd.Encoder all satisfy it.
type encoder interface {
	io.Writer
	io.Closer
	Flush() error
	Reset(w io.Writer)
}

// encoderConstructor builds a fresh encoder writing to w at a fixed level.
type encoderConstructor func(w io.Writer) encoder

// registry maps a content coding to a factory that, given a level, returns a
// constructor. Populated by init() (gzip) and build-tagged files (brotli/zstd).
var registry = map[string]func(level int) encoderConstructor{}

// registerEncoder adds a content coding to the registry. Called from init().
func registerEncoder(name string, factory func(level int) encoderConstructor) {
	registry[name] = factory
}

// EncoderAvailable reports whether the named content coding is compiled into
// this build. gzip is always available; br/zstd depend on build tags.
func EncoderAvailable(name string) bool {
	_, ok := registry[name]
	return ok
}

func init() {
	registerEncoder("gzip", func(level int) encoderConstructor {
		lvl := gzipLevel(level)
		return func(w io.Writer) encoder {
			gz, _ := gzip.NewWriterLevel(w, lvl)
			return gz
		}
	})
}

func gzipLevel(level int) int {
	switch {
	case level <= 0:
		return gzip.DefaultCompression
	case level > gzip.BestCompression:
		return gzip.BestCompression
	default:
		return level
	}
}

// encoderPool reuses encoder instances for one coding at a fixed level.
type encoderPool struct {
	name string
	cons encoderConstructor
	pool sync.Pool
}

func (p *encoderPool) get(w io.Writer) encoder {
	if v := p.pool.Get(); v != nil {
		e := v.(encoder)
		e.Reset(w)
		return e
	}
	return p.cons(w)
}

func (p *encoderPool) put(e encoder) { p.pool.Put(e) }

// compression holds the precomputed state shared by all requests.
type compression struct {
	pools      []*encoderPool // server preference order
	mime       mimeMatcher
	minSize    int
	onCompress func(string)
}

// NewCompression builds the Compression middleware. It returns an error if a
// configured encoder is not compiled into this build so the caller can fail
// startup/reload with a clear "not compiled in this build" message.
func NewCompression(opts CompressionOptions) (Middleware, error) {
	if len(opts.Encoders) == 0 {
		return func(next http.Handler) http.Handler { return next }, nil
	}
	c := &compression{
		mime:       newMimeMatcher(opts.Types),
		minSize:    int(opts.MinSize),
		onCompress: opts.OnCompress,
	}
	if c.minSize < 0 {
		c.minSize = 0
	}
	for _, name := range opts.Encoders {
		factory, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("compression encoder %q not compiled in this build (rebuild with -tags %s)", name, name)
		}
		c.pools = append(c.pools, &encoderPool{name: name, cons: factory(opts.Level)})
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ep := c.negotiate(r)
			if ep == nil {
				// Client accepts none of our codings: never compress, but the
				// response still varies by Accept-Encoding for caches.
				addVary(w.Header(), "Accept-Encoding")
				next.ServeHTTP(w, r)
				return
			}
			cw := &compressWriter{ResponseWriter: w, c: c, r: r, chosen: ep, minSize: c.minSize}
			defer cw.close()
			next.ServeHTTP(cw, r)
		})
	}, nil
}

// negotiate selects the best registered encoder for the request, honoring
// Accept-Encoding q-values with server preference as the tie-break.
func (c *compression) negotiate(r *http.Request) *encoderPool {
	header := r.Header.Get("Accept-Encoding")
	if header == "" {
		return nil
	}
	q := parseAcceptEncoding(header)
	var best *encoderPool
	bestQ := 0.0
	for _, ep := range c.pools {
		cq, ok := clientQuality(q, ep.name)
		if !ok || cq <= 0 {
			continue
		}
		if cq > bestQ {
			bestQ = cq
			best = ep
		}
	}
	return best
}

// compressWriter buffers the start of the response until it can decide whether
// to compress (min-size + MIME gate), then either compresses or passes through.
type compressWriter struct {
	http.ResponseWriter
	c       *compression
	r       *http.Request
	chosen  *encoderPool
	minSize int

	buf           []byte
	code          int
	sawHeader     bool // WriteHeader called by the handler (logically)
	headerWritten bool // header forwarded to the underlying writer
	decided       bool
	hijacked      bool    // connection taken over (WebSocket): do not finalize
	enc           encoder // nil once decided means pass-through
}

func (cw *compressWriter) WriteHeader(code int) {
	if cw.sawHeader {
		return
	}
	cw.sawHeader = true
	cw.code = code
	// Responses without a body, or already encoded, never get compressed. The
	// Vary header is added in flushHeader so it is guaranteed even for responses
	// that never call WriteHeader (e.g. an empty body).
	if !bodyAllowed(code) || cw.Header().Get("Content-Encoding") != "" {
		cw.startPassthrough()
	}
}

func (cw *compressWriter) Write(b []byte) (int, error) {
	if !cw.sawHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if cw.decided {
		if cw.enc != nil {
			return cw.enc.Write(b)
		}
		return cw.ResponseWriter.Write(b)
	}
	cw.buf = append(cw.buf, b...)
	if len(cw.buf) >= cw.minSize {
		cw.decide()
		return len(b), cw.flushBuf()
	}
	return len(b), nil
}

// Flush forces a decision so buffered bytes reach the client (SSE), then
// flushes the encoder and the underlying writer.
func (cw *compressWriter) Flush() {
	if !cw.sawHeader {
		cw.WriteHeader(http.StatusOK)
	}
	if !cw.decided {
		cw.decide()
	}
	_ = cw.flushBuf()
	if cw.enc != nil {
		_ = cw.enc.Flush()
	}
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer for WebSocket upgrades. The upgrade
// handshake writes no body, so nothing has been buffered or compressed. The
// writer is marked hijacked so close() does not touch the raw connection.
func (cw *compressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := cw.ResponseWriter.(http.Hijacker); ok {
		cw.hijacked = true
		return h.Hijack()
	}
	return nil, nil, errors.New("compress: underlying ResponseWriter does not support hijacking")
}

// decide chooses compression vs pass-through once enough bytes are buffered or a
// flush forces the choice. It applies the Range, Content-Type and MIME gates.
func (cw *compressWriter) decide() {
	if cw.decided {
		return
	}
	if cw.r.Header.Get("Range") != "" {
		cw.startPassthrough()
		return
	}
	ct := cw.Header().Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(cw.buf)
		cw.Header().Set("Content-Type", ct)
	}
	if !cw.c.mime.match(ct) {
		cw.startPassthrough()
		return
	}
	cw.startCompress(cw.chosen)
}

func (cw *compressWriter) startCompress(ep *encoderPool) {
	cw.decided = true
	h := cw.Header()
	h.Set("Content-Encoding", ep.name)
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	cw.flushHeader()
	cw.enc = ep.get(cw.ResponseWriter)
	if cw.c.onCompress != nil {
		cw.c.onCompress(ep.name)
	}
}

func (cw *compressWriter) startPassthrough() {
	cw.decided = true
	cw.enc = nil
	cw.flushHeader()
}

func (cw *compressWriter) flushHeader() {
	if cw.headerWritten {
		return
	}
	cw.headerWritten = true
	// Single choke point for the Vary guarantee: the response always varies by
	// Accept-Encoding, whatever the outcome, including empty-body responses that
	// reach this path straight from close().
	addVary(cw.Header(), "Accept-Encoding")
	if !cw.sawHeader {
		cw.code = http.StatusOK
	}
	cw.ResponseWriter.WriteHeader(cw.code)
}

func (cw *compressWriter) flushBuf() error {
	if len(cw.buf) == 0 {
		return nil
	}
	var err error
	if cw.enc != nil {
		_, err = cw.enc.Write(cw.buf)
	} else {
		_, err = cw.ResponseWriter.Write(cw.buf)
	}
	cw.buf = nil
	return err
}

// close finalizes the response. A body that never reached min_size (and was not
// forced out by a flush) passes through uncompressed.
func (cw *compressWriter) close() error {
	if cw.hijacked {
		return nil
	}
	if !cw.decided {
		cw.startPassthrough()
	}
	var err error
	if cw.enc != nil {
		if e := cw.flushBuf(); e != nil {
			err = e
		}
		if e := cw.enc.Close(); err == nil {
			err = e
		}
		cw.chosen.put(cw.enc)
		cw.enc = nil
		return err
	}
	return cw.flushBuf()
}

// bodyAllowed reports whether a status code may carry a response body.
func bodyAllowed(code int) bool {
	switch {
	case code >= 100 && code < 200:
		return false
	case code == http.StatusNoContent, code == http.StatusNotModified:
		return false
	}
	return true
}

// addVary appends value to the Vary header unless it (or "*") is already present.
func addVary(h http.Header, value string) {
	for _, v := range h.Values("Vary") {
		if v == "*" || strings.EqualFold(v, value) {
			return
		}
	}
	h.Add("Vary", value)
}

// parseAcceptEncoding parses an Accept-Encoding header into coding->q-value,
// lowercasing coding names. A missing q defaults to 1.0.
func parseAcceptEncoding(header string) map[string]float64 {
	out := make(map[string]float64)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		q := 1.0
		if i := strings.IndexByte(part, ';'); i >= 0 {
			name = strings.TrimSpace(part[:i])
			for _, p := range strings.Split(part[i+1:], ";") {
				p = strings.TrimSpace(p)
				if strings.HasPrefix(p, "q=") {
					if v, err := strconv.ParseFloat(strings.TrimSpace(p[2:]), 64); err == nil {
						q = v
					}
				}
			}
		}
		out[strings.ToLower(name)] = q
	}
	return out
}

// clientQuality returns the client's q-value for a coding, falling back to the
// wildcard "*" when the coding is not named explicitly.
func clientQuality(q map[string]float64, name string) (float64, bool) {
	if v, ok := q[name]; ok {
		return v, true
	}
	if v, ok := q["*"]; ok {
		return v, true
	}
	return 0, false
}

// mimeMatcher matches a response Content-Type against an allow-list of exact
// types ("application/json"), families ("text/*"), or a wildcard ("*"/"*/*").
type mimeMatcher struct {
	all      bool
	exact    map[string]bool
	families map[string]bool
}

func newMimeMatcher(types []string) mimeMatcher {
	m := mimeMatcher{exact: map[string]bool{}, families: map[string]bool{}}
	for _, t := range types {
		t = strings.ToLower(strings.TrimSpace(t))
		switch {
		case t == "":
			continue
		case t == "*" || t == "*/*":
			m.all = true
		case strings.HasSuffix(t, "/*"):
			m.families[strings.TrimSuffix(t, "/*")] = true
		default:
			m.exact[t] = true
		}
	}
	return m
}

func (m mimeMatcher) match(contentType string) bool {
	if m.all {
		return true
	}
	mt := mediaType(contentType)
	if mt == "" {
		return false
	}
	if m.exact[mt] {
		return true
	}
	if i := strings.IndexByte(mt, '/'); i > 0 {
		return m.families[mt[:i]]
	}
	return false
}

// mediaType extracts the lowercased media type from a Content-Type value,
// dropping any parameters such as "; charset=utf-8".
func mediaType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

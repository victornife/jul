// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yookoala/gofast"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/upstream"
)

// NewFastCGI builds a handler that forwards requests to a FastCGI (e.g.
// PHP-FPM) or uWSGI application server. The handler is selected by which of
// fastcgi_pass / uwsgi_pass is configured.
//
// Both targets are full upstream-pool members: they accept a named upstream or
// a literal socket, and therefore get load balancing, active health checking,
// failure accounting and admission on the same terms as proxy_pass. PHP-FPM's
// fixed pm.max_children makes bounding concurrency more valuable here than for
// a typical HTTP backend, not less.
//
// ctx bounds the registry lookup. srv is part of the router.Builder signature
// but is not needed here.
func NewFastCGI(ctx context.Context, _ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger) (http.Handler, error) {
	if loc.UWSGIPass != "" {
		return newUWSGIHandler(ctx, loc, upstreams, reg, log)
	}
	return newFastCGIHandler(ctx, loc, upstreams, reg, log)
}

// parseSocketAddress interprets a fastcgi/uwsgi target. It delegates to
// upstream.ParseSocketAddress so a target, an upstream server address and a
// health probe cannot disagree about what an address means.
func parseSocketAddress(pass string) (network, address string) {
	return upstream.ParseSocketAddress(pass)
}

// resolveCGIPool turns a fastcgi_pass or uwsgi_pass value into a backend pool.
// A name matching a configured upstream resolves through the registry, which
// owns the pool's lifecycle across reloads and runs its health checker; a
// literal socket builds an anonymous pool of one, exactly as a literal
// proxy_pass does.
func resolveCGIPool(ctx context.Context, pass string, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry) (*upstream.Pool, error) {
	if up, ok := upstreams[pass]; ok {
		if reg != nil {
			return reg.For(ctx, up, "")
		}
		return upstream.NewPool(up, "")
	}
	single := config.UpstreamConfig{
		Name:     pass,
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: pass, Weight: 1}},
		MaxFails: 3,
	}
	return upstream.NewPool(single, "")
}

// cgiDialer builds the dialer for a CGI-family backend, honouring the
// location's proxy_connect_timeout.
func cgiDialer(loc config.LocationConfig) *net.Dialer {
	timeout := loc.ProxyConnectTimeout.Std()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &net.Dialer{Timeout: timeout}
}

// newFastCGIHandler wires a gofast handler onto a pool-selected backend.
func newFastCGIHandler(ctx context.Context, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger) (http.Handler, error) {
	if strings.TrimSpace(loc.FastCGIPass) == "" {
		return nil, fmt.Errorf("fastcgi_pass is empty")
	}
	pool, err := resolveCGIPool(ctx, loc.FastCGIPass, upstreams, reg)
	if err != nil {
		return nil, err
	}

	h := &fastcgiHandler{
		pool:          pool,
		dialer:        cgiDialer(loc),
		log:           log,
		retryOverride: newLocationRetry(loc),
		session: gofast.Chain(
			gofast.BasicParamsMap, // CONTENT_*, REQUEST_*, SERVER_*, REMOTE_* ...
			gofast.MapHeader,      // HTTP_* request headers
			fcgiScriptParams(loc), // SCRIPT_FILENAME/NAME + config overrides (last = wins)
		)(gofast.BasicSession),
	}
	return newAdmittedHandler(h, pool.Admission(), nil), nil
}

// fastcgiHandler selects a backend per request and speaks FastCGI to it.
//
// It deliberately does not use gofast.ClientPool. That pool spawns an endless
// producer goroutine over an unbuffered channel with an eagerly dialled client
// blocked on the handoff, and offers no Close: every handler generation leaked
// one goroutine and one open backend connection, on every reload, for the life
// of the process. Because the pool was created with scale 0 it never actually
// reused a connection either, so dialling per request costs nothing that was
// previously being saved.
//
// It also does not use gofast.NewHandler. That handler hardcodes 502 for a dial
// failure and 500 for a session failure, and reports both with log.Printf to
// the *global* logger — its own SetLogger is not consulted on those paths. Jul
// owns the status mapping and the bounded reason taxonomy, so it drives the
// three exported steps itself: newClient, sessionHandler, WriteTo.
type fastcgiHandler struct {
	pool          *upstream.Pool
	dialer        *net.Dialer
	session       gofast.SessionHandler
	log           *slog.Logger
	retryOverride upstream.RetryOverride
}

func (h *fastcgiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The retry boundary is WriteTo: everything before it is recoverable
	// because nothing has reached the client, and everything after it is not.
	// Driving gofast's three exported steps directly is what makes that
	// boundary expressible at all.
	replayable := isIdempotent(r.Method) && replayableBody(r)

	var (
		pipe   *gofast.ResponsePipe
		client gofast.Client
		chosen upstream.Attempt
	)
	_, err := h.pool.Do(r.Context(), h.pool.RetryRequestFor(h.retryOverride, replayable),
		func(ctx context.Context, b upstream.Attempt, n int) upstream.AttemptResult {
			req := r
			if n > 1 && r.GetBody != nil {
				body, berr := r.GetBody()
				if berr != nil {
					return upstream.AttemptResult{Err: berr, Terminal: true}
				}
				req = r.Clone(ctx)
				req.Body = body
			}

			c, derr := gofast.SimpleClientFactory(func() (net.Conn, error) {
				return h.dialer.DialContext(ctx, b.Network, b.Address)
			})()
			if derr != nil {
				h.noteFailure(b, derr, "fastcgi dial failed")
				return upstream.AttemptResult{Err: derr}
			}

			p, serr := h.session(c, gofast.NewRequest(req))
			if serr != nil {
				_ = c.Close()
				h.noteFailure(b, serr, "fastcgi session failed")
				return upstream.AttemptResult{Err: serr}
			}

			h.pool.MarkSuccess(b)
			pipe, client, chosen = p, c, b
			// The connection and the backend slot must outlive the attempt:
			// the response has not been read yet.
			return upstream.AttemptResult{Retain: true}
		})
	if err != nil {
		h.fail(w, r, upstreamErrorStatus(err), err)
		return
	}
	defer h.pool.Release(chosen.Backend)
	defer func() {
		if cerr := client.Close(); cerr != nil && h.log != nil {
			h.log.Warn("fastcgi client close failed", "upstream", h.pool.Name(), "backend", chosen.Address, "error", cerr)
		}
	}()

	// Past this point a byte may reach the client, so nothing here is retried.
	errBuffer := new(bytes.Buffer)
	if werr := pipe.WriteTo(w, errBuffer); werr != nil && h.log != nil {
		h.log.Warn("fastcgi response write failed", "upstream", h.pool.Name(), "backend", chosen.Address, "error", werr)
	}
	if errBuffer.Len() > 0 && h.log != nil {
		h.log.Error("fastcgi application error stream",
			"upstream", h.pool.Name(),
			"backend", chosen.Address,
			"path", r.URL.Path,
			"request_id", middleware.RequestIDFrom(r.Context()),
			"stderr", errBuffer.String(),
		)
	}
}

// noteFailure records a failed attempt against passive health and logs it on
// the pool's shared throttle, so a backend outage cannot flood the log through
// the CGI path any more than through the HTTP one.
func (h *fastcgiHandler) noteFailure(b upstream.Attempt, err error, msg string) {
	tripped := h.pool.MarkFailure(b)
	if h.log == nil {
		return
	}
	if tripped || h.pool.AllowDialFailureLog() {
		h.log.Warn(msg, "upstream", h.pool.Name(), "backend", b.Address, "network", b.Network,
			"reason", upstream.ClassifyDialError(err), "error", err)
	}
}

func (h *fastcgiHandler) fail(w http.ResponseWriter, r *http.Request, code int, err error) {
	if h.log != nil {
		h.log.Error("fastcgi upstream error",
			"upstream", h.pool.Name(),
			"path", r.URL.Path,
			"status", code,
			"error", err,
			"request_id", middleware.RequestIDFrom(r.Context()),
		)
	}
	http.Error(w, fmt.Sprintf("%d %s", code, http.StatusText(code)), code)
}

// upstreamErrorStatus maps a backend-selection failure to a client status. Both
// causes are 503, but they are distinct errors so an operator is never told
// "no healthy backend" when the real answer is "every backend is at capacity".
func upstreamErrorStatus(err error) int {
	if errors.Is(err, upstream.ErrNoAvailableBackend) || errors.Is(err, upstream.ErrBackendAtCapacity) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadGateway
}

// fcgiScriptParams sets DOCUMENT_ROOT / SCRIPT_FILENAME / SCRIPT_NAME and then
// applies any explicit fastcgi_params overrides. Running innermost in the chain
// ensures these values win over the defaults set earlier.
func fcgiScriptParams(loc config.LocationConfig) gofast.Middleware {
	return func(inner gofast.SessionHandler) gofast.SessionHandler {
		return func(client gofast.Client, req *gofast.Request) (*gofast.ResponsePipe, error) {
			if req.Params == nil {
				req.Params = make(map[string]string)
			}
			scriptName := scriptNameFor(req.Raw.URL.Path, loc.Index)
			if loc.Root != "" {
				req.Params["DOCUMENT_ROOT"] = loc.Root
				req.Params["SCRIPT_FILENAME"] = filepath.Join(loc.Root, filepath.FromSlash(scriptName))
			}
			req.Params["SCRIPT_NAME"] = scriptName
			for k, v := range loc.FastCGIParams {
				req.Params[k] = v
			}
			return inner(client, req)
		}
	}
}

// scriptNameFor resolves the script path, appending an index file for
// directory requests.
func scriptNameFor(urlPath string, index []string) string {
	clean := path.Clean("/" + strings.TrimLeft(urlPath, "/"))
	if strings.HasSuffix(urlPath, "/") || clean == "/" {
		idx := "index.php"
		if len(index) > 0 {
			idx = index[0]
		}
		clean = path.Join(clean, idx)
	}
	return clean
}

// uwsgiHandler speaks the uWSGI packet protocol (modifier1 = 0, the WSGI
// variant) and forwards the CGI-style response back to the client.
type uwsgiHandler struct {
	pool          *upstream.Pool
	dialer        *net.Dialer
	loc           config.LocationConfig
	log           *slog.Logger
	retryOverride upstream.RetryOverride
}

func newUWSGIHandler(ctx context.Context, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger) (http.Handler, error) {
	if strings.TrimSpace(loc.UWSGIPass) == "" {
		return nil, fmt.Errorf("uwsgi_pass is empty")
	}
	pool, err := resolveCGIPool(ctx, loc.UWSGIPass, upstreams, reg)
	if err != nil {
		return nil, err
	}
	h := &uwsgiHandler{pool: pool, dialer: cgiDialer(loc), loc: loc, log: log, retryOverride: newLocationRetry(loc)}
	return newAdmittedHandler(h, pool.Admission(), nil), nil
}

func (h *uwsgiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Everything up to writeCGIResponse is recoverable, because nothing has
	// reached the client yet. That is the same boundary the HTTP and FastCGI
	// adapters use, expressed in this protocol's terms.
	replayable := isIdempotent(r.Method) && replayableBody(r)

	var (
		conn   net.Conn
		chosen upstream.Attempt
	)
	_, err := h.pool.Do(r.Context(), h.pool.RetryRequestFor(h.retryOverride, replayable),
		func(ctx context.Context, b upstream.Attempt, n int) upstream.AttemptResult {
			body := r.Body
			if n > 1 {
				// A body-less request needs no rewind; its body is already at
				// EOF, which is what "no body" means on a server request.
				body = http.NoBody
				if r.GetBody != nil {
					rewound, berr := r.GetBody()
					if berr != nil {
						return upstream.AttemptResult{Err: berr, Terminal: true}
					}
					body = rewound
				}
			}

			// The connect timeout comes from the location's proxy_connect_timeout
			// like every other transport, rather than a hardcoded ten seconds.
			c, derr := h.dialer.DialContext(ctx, b.Network, b.Address)
			if derr != nil {
				h.noteFailure(b, derr, "uwsgi dial failed")
				return upstream.AttemptResult{Err: derr}
			}
			if dl, ok := ctx.Deadline(); ok {
				_ = c.SetDeadline(dl)
			}
			if serr := h.sendRequest(c, r, body); serr != nil {
				_ = c.Close()
				h.noteFailure(b, serr, "uwsgi request send failed")
				return upstream.AttemptResult{Err: serr}
			}

			h.pool.MarkSuccess(b)
			conn, chosen = c, b
			// The connection and the backend slot must outlive the attempt: the
			// response has not been read yet.
			return upstream.AttemptResult{Retain: true}
		})
	if err != nil {
		h.fail(w, r, upstreamErrorStatus(err), err)
		return
	}
	defer h.pool.Release(chosen.Backend)
	defer conn.Close()

	// Past this point a byte may reach the client, so nothing here is retried.
	if err := writeCGIResponse(bufio.NewReader(conn), w); err != nil && !errors.Is(err, io.EOF) {
		// Headers may already be written; just log.
		if h.log != nil {
			h.log.Error("uwsgi response error", "path", r.URL.Path, "error", err,
				"request_id", middleware.RequestIDFrom(r.Context()))
		}
	}
}

// sendRequest writes the uWSGI var block and the body. It is the whole of one
// attempt's send side, so a failure anywhere in it is retryable against another
// backend: nothing has been read back, let alone written to the client.
func (h *uwsgiHandler) sendRequest(conn net.Conn, r *http.Request, body io.ReadCloser) error {
	params := buildCGIParams(h.loc, r)
	var vars bytes.Buffer
	for k, v := range params {
		writeUWSGIVar(&vars, k, v)
	}
	if vars.Len() > 0xffff {
		return fmt.Errorf("uwsgi var block too large (%d bytes)", vars.Len())
	}

	// Packet header: modifier1=0, datasize uint16 little-endian, modifier2=0.
	header := [4]byte{0, byte(vars.Len()), byte(vars.Len() >> 8), 0}
	if _, err := conn.Write(header[:]); err != nil {
		return err
	}
	if _, err := conn.Write(vars.Bytes()); err != nil {
		return err
	}
	// For server requests r.Body is always non-nil; it returns EOF when empty.
	if _, err := io.Copy(conn, body); err != nil {
		return err
	}
	// Signal end-of-request so the app server stops reading the body.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return nil
}

// noteFailure records a failed attempt against passive health and logs it on
// the pool's shared throttle.
func (h *uwsgiHandler) noteFailure(b upstream.Attempt, err error, msg string) {
	tripped := h.pool.MarkFailure(b)
	if h.log == nil {
		return
	}
	if tripped || h.pool.AllowDialFailureLog() {
		h.log.Warn(msg, "upstream", h.pool.Name(), "backend", b.Address, "network", b.Network,
			"reason", upstream.ClassifyDialError(err), "error", err)
	}
}

func (h *uwsgiHandler) fail(w http.ResponseWriter, r *http.Request, code int, err error) {
	if h.log != nil {
		h.log.Error("uwsgi upstream error", "upstream", h.pool.Name(), "path", r.URL.Path,
			"status", code, "error", err, "request_id", middleware.RequestIDFrom(r.Context()))
	}
	http.Error(w, fmt.Sprintf("%d %s", code, http.StatusText(code)), code)
}

// writeUWSGIVar appends a key/value pair using uWSGI's
// (uint16 len, bytes) framing in little-endian order.
func writeUWSGIVar(buf *bytes.Buffer, key, val string) {
	var sz [2]byte
	binary.LittleEndian.PutUint16(sz[:], uint16(len(key)))
	buf.Write(sz[:])
	buf.WriteString(key)
	binary.LittleEndian.PutUint16(sz[:], uint16(len(val)))
	buf.Write(sz[:])
	buf.WriteString(val)
}

// buildCGIParams constructs the CGI/1.1 environment passed to a uWSGI app.
func buildCGIParams(loc config.LocationConfig, r *http.Request) map[string]string {
	p := map[string]string{
		"GATEWAY_INTERFACE": "CGI/1.1",
		"SERVER_SOFTWARE":   "jul",
		"SERVER_PROTOCOL":   r.Proto,
		"REQUEST_METHOD":    r.Method,
		"REQUEST_URI":       r.RequestURI,
		"QUERY_STRING":      r.URL.RawQuery,
		"CONTENT_TYPE":      r.Header.Get("Content-Type"),
		"REDIRECT_STATUS":   "200",
	}
	if r.ContentLength >= 0 {
		p["CONTENT_LENGTH"] = strconv.FormatInt(r.ContentLength, 10)
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	p["SERVER_NAME"] = host
	p["SERVER_PORT"] = port

	// REMOTE_ADDR is the canonical client: the transport peer unless the
	// listener explicitly trusts the peer as a proxy. REMOTE_PORT stays the
	// transport port, which is the only port that exists — an asserted client
	// address carries none. JUL_PEER_ADDR keeps the direct peer available to
	// the application as a separate fact.
	client, peer := forwardedAddrs(r)
	switch {
	case client != "":
		p["REMOTE_ADDR"] = client
	case peer != "":
		p["REMOTE_ADDR"] = peer
	default:
		p["REMOTE_ADDR"] = r.RemoteAddr
	}
	if peer != "" {
		p["JUL_PEER_ADDR"] = peer
	}
	if _, rp, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		p["REMOTE_PORT"] = rp
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
		p["HTTPS"] = "on"
	}
	p["REQUEST_SCHEME"] = scheme

	scriptName := scriptNameFor(r.URL.Path, loc.Index)
	p["SCRIPT_NAME"] = scriptName
	p["PATH_INFO"] = r.URL.Path
	if loc.Root != "" {
		p["DOCUMENT_ROOT"] = loc.Root
		p["SCRIPT_FILENAME"] = filepath.Join(loc.Root, filepath.FromSlash(scriptName))
	}

	for name, vals := range r.Header {
		key := "HTTP_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		p[key] = strings.Join(vals, ", ")
	}
	// The inbound chain is attacker input unless it came from a trusted proxy,
	// and the application cannot tell the difference. Overwrite it with Jul's
	// own trusted chain rather than laundering what arrived.
	p["HTTP_X_FORWARDED_FOR"] = forwardedChain(client, peer)
	if p["HTTP_X_FORWARDED_FOR"] == "" {
		delete(p, "HTTP_X_FORWARDED_FOR")
	}

	// fastcgi_params doubles as the explicit param override map for uWSGI.
	for k, v := range loc.FastCGIParams {
		p[k] = v
	}
	return p
}

// writeCGIResponse parses a CGI-style response (optional HTTP status line, then
// headers, a blank line, and the body) and writes it to w.
func writeCGIResponse(br *bufio.Reader, w http.ResponseWriter) error {
	status := http.StatusOK
	first := true
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if line != "" || err == nil {
				break // end of header block
			}
			// Reached only when line == "" and err != nil.
			return err
		}

		if first && strings.HasPrefix(trimmed, "HTTP/") {
			if fields := strings.SplitN(trimmed, " ", 3); len(fields) >= 2 {
				if c, e := strconv.Atoi(fields[1]); e == nil {
					status = c
				}
			}
			first = false
			if err != nil {
				break
			}
			continue
		}
		first = false

		if idx := strings.IndexByte(trimmed, ':'); idx >= 0 {
			key := strings.TrimSpace(trimmed[:idx])
			val := strings.TrimSpace(trimmed[idx+1:])
			if strings.EqualFold(key, "Status") {
				if c, e := strconv.Atoi(strings.Fields(val)[0]); e == nil {
					status = c
				}
			} else {
				w.Header().Add(key, val)
			}
		}
		if err != nil {
			break
		}
	}

	w.WriteHeader(status)
	// lgtm[go/reflected-xss] – This forwards the upstream FastCGI/uWSGI response
	// body unchanged. User input flows to the upstream application, which is
	// responsible for sanitizing any output it generates.
	_, err := io.Copy(w, br)
	return err
}

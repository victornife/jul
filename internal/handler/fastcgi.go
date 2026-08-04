// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
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
)

// NewFastCGI builds a handler that forwards requests to a FastCGI (e.g.
// PHP-FPM) or uWSGI application server. The handler is selected by which of
// fastcgi_pass / uwsgi_pass is configured.
//
// The extra log parameter is bound via a closure in main so the resulting
// function still satisfies router.Builder. srv is part of that signature but
// is not needed here.
func NewFastCGI(_ config.ServerConfig, loc config.LocationConfig, log *slog.Logger) (http.Handler, error) {
	if loc.UWSGIPass != "" {
		return newUWSGIHandler(loc, log)
	}
	return newFastCGIHandler(loc, log)
}

// parseSocketAddress interprets a fastcgi/uwsgi target. Accepted forms:
//
//	unix:/run/php/php-fpm.sock   -> ("unix", "/run/php/php-fpm.sock")
//	tcp://127.0.0.1:9000         -> ("tcp",  "127.0.0.1:9000")
//	127.0.0.1:9000               -> ("tcp",  "127.0.0.1:9000")
func parseSocketAddress(pass string) (network, address string) {
	switch {
	case strings.HasPrefix(pass, "unix:"):
		return "unix", strings.TrimPrefix(pass, "unix:")
	case strings.HasPrefix(pass, "tcp://"):
		return "tcp", strings.TrimPrefix(pass, "tcp://")
	default:
		return "tcp", pass
	}
}

// newFastCGIHandler wires a gofast handler with a small client pool.
func newFastCGIHandler(loc config.LocationConfig, log *slog.Logger) (http.Handler, error) {
	network, address := parseSocketAddress(loc.FastCGIPass)
	if address == "" {
		return nil, fmt.Errorf("fastcgi_pass is empty")
	}

	connFactory := gofast.SimpleConnFactory(network, address)
	pool := gofast.NewClientPool(gofast.SimpleClientFactory(connFactory), 0, 30*time.Second)

	session := gofast.Chain(
		gofast.BasicParamsMap, // CONTENT_*, REQUEST_*, SERVER_*, REMOTE_* ...
		gofast.MapHeader,      // HTTP_* request headers
		fcgiScriptParams(loc), // SCRIPT_FILENAME/NAME + config overrides (last = wins)
	)(gofast.BasicSession)

	h := gofast.NewHandler(session, pool.CreateClient)
	if log != nil {
		h.SetLogger(slog.NewLogLogger(log.Handler(), slog.LevelError))
	}
	return h, nil
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
	network string
	address string
	loc     config.LocationConfig
	log     *slog.Logger
}

func newUWSGIHandler(loc config.LocationConfig, log *slog.Logger) (http.Handler, error) {
	network, address := parseSocketAddress(loc.UWSGIPass)
	if address == "" {
		return nil, fmt.Errorf("uwsgi_pass is empty")
	}
	return &uwsgiHandler{network: network, address: address, loc: loc, log: log}, nil
}

func (h *uwsgiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := net.DialTimeout(h.network, h.address, 10*time.Second)
	if err != nil {
		h.fail(w, r, http.StatusBadGateway, err)
		return
	}
	defer conn.Close()
	if dl, ok := r.Context().Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	params := buildCGIParams(h.loc, r)
	var vars bytes.Buffer
	for k, v := range params {
		writeUWSGIVar(&vars, k, v)
	}
	if vars.Len() > 0xffff {
		h.fail(w, r, http.StatusBadGateway, fmt.Errorf("uwsgi var block too large (%d bytes)", vars.Len()))
		return
	}

	// Packet header: modifier1=0, datasize uint16 little-endian, modifier2=0.
	header := [4]byte{0, byte(vars.Len()), byte(vars.Len() >> 8), 0}
	if _, err := conn.Write(header[:]); err != nil {
		h.fail(w, r, http.StatusBadGateway, err)
		return
	}
	if _, err := conn.Write(vars.Bytes()); err != nil {
		h.fail(w, r, http.StatusBadGateway, err)
		return
	}
	// For server requests r.Body is always non-nil; it returns EOF when empty.
	if _, err := io.Copy(conn, r.Body); err != nil {
		h.fail(w, r, http.StatusBadGateway, err)
		return
	}
	// Signal end-of-request so the app server stops reading the body.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	if err := writeCGIResponse(bufio.NewReader(conn), w); err != nil && !errors.Is(err, io.EOF) {
		// Headers may already be written; just log.
		if h.log != nil {
			h.log.Error("uwsgi response error", "path", r.URL.Path, "error", err,
				"request_id", middleware.RequestIDFrom(r.Context()))
		}
	}
}

func (h *uwsgiHandler) fail(w http.ResponseWriter, r *http.Request, code int, err error) {
	if h.log != nil {
		h.log.Error("uwsgi upstream error", "address", h.address, "path", r.URL.Path,
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

	if ra, rp, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		p["REMOTE_ADDR"] = ra
		p["REMOTE_PORT"] = rp
	} else {
		p["REMOTE_ADDR"] = r.RemoteAddr
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

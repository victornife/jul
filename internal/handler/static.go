// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"fmt"
	"html"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"jul/internal/config"
)

// staticHandler serves files from a directory, confined by os.Root so that no
// request path can escape the configured root (path traversal protection).
type staticHandler struct {
	root         *os.Root
	rootPath     string
	index        []string
	tryFiles     []string
	dirListing   bool
	allowHidden  bool
	cacheControl string
	errPages     *ErrorPages

	// precompressed serves sidecar .br/.gz files when present and acceptable;
	// precompressEncoders is the subset of {br,gzip} to consider.
	precompressed       bool
	precompressEncoders []string
}

// StaticOptions carries cross-cutting settings (derived from global config)
// that are not part of a single location block.
type StaticOptions struct {
	// Precompressed enables sidecar .br/.gz lookups for static files.
	Precompressed bool
	// Encoders limits which sidecar codings are served (subset of br, gzip).
	Encoders []string
}

// NewStatic builds a static file handler for a location with default options.
// Its signature matches router.Builder so it can be registered for the "static"
// action without the handler package importing the router (avoiding an import
// cycle).
func NewStatic(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
	return NewStaticWithOptions(srv, loc, StaticOptions{})
}

// NewStaticWithOptions builds a static file handler, applying cross-cutting
// options such as precompressed sidecar serving.
func NewStaticWithOptions(srv config.ServerConfig, loc config.LocationConfig, opts StaticOptions) (http.Handler, error) {
	root, err := os.OpenRoot(loc.Root)
	if err != nil {
		return nil, fmt.Errorf("static root %q: %w", loc.Root, err)
	}
	index := loc.Index
	if len(index) == 0 {
		index = []string{"index.html"}
	}
	ep, err := NewErrorPages(srv.ErrorPages)
	if err != nil {
		return nil, err
	}
	return &staticHandler{
		root:                root,
		rootPath:            loc.Root,
		index:               index,
		tryFiles:            loc.TryFiles,
		dirListing:          loc.DirectoryListing,
		allowHidden:         loc.AllowHidden,
		cacheControl:        loc.CacheControl,
		errPages:            ep,
		precompressed:       opts.Precompressed,
		precompressEncoders: opts.Encoders,
	}, nil
}

// Close releases the underlying os.Root directory handle. The server holds it
// open for its lifetime; reload (P9) closes superseded handlers.
func (h *staticHandler) Close() error {
	return h.root.Close()
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.errPages.Render(w, r, http.StatusMethodNotAllowed)
		return
	}

	upath := path.Clean("/" + r.URL.Path)

	if len(h.tryFiles) > 0 {
		for _, tf := range h.tryFiles {
			if h.tryServe(w, r, expandURI(tf, upath)) {
				return
			}
		}
		h.errPages.Render(w, r, http.StatusNotFound)
		return
	}

	if h.tryServe(w, r, upath) {
		return
	}
	h.errPages.Render(w, r, http.StatusNotFound)
}

// tryServe attempts to serve candidate. It returns true if a response was
// written, false if the candidate did not resolve to something servable.
func (h *staticHandler) tryServe(w http.ResponseWriter, r *http.Request, candidate string) bool {
	clean := path.Clean("/" + candidate)
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" {
		rel = "."
	}
	if h.isHidden(rel) {
		return false
	}

	info, err := h.root.Stat(rel)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return h.serveDir(w, r, rel)
	}
	return h.serveFile(w, r, rel, info)
}

func (h *staticHandler) serveDir(w http.ResponseWriter, r *http.Request, rel string) bool {
	for _, idx := range h.index {
		ip := path.Join(rel, idx)
		if h.isHidden(ip) {
			continue
		}
		if info, err := h.root.Stat(ip); err == nil && !info.IsDir() {
			return h.serveFile(w, r, ip, info)
		}
	}
	if h.dirListing {
		h.listDir(w, r, rel)
		return true
	}
	return false
}

func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, rel string, info os.FileInfo) bool {
	if h.servePrecompressed(w, r, rel) {
		return true
	}
	f, err := h.root.Open(rel)
	if err != nil {
		return false
	}
	defer f.Close()

	// Weak validators derived from size and mtime. http.ServeContent honors
	// If-None-Match / If-Range against the ETag and If-Modified-Since against
	// the modtime, and implements Range requests.
	etag := fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size())
	w.Header().Set("ETag", etag)
	if h.cacheControl != "" {
		w.Header().Set("Cache-Control", h.cacheControl)
	}
	http.ServeContent(w, r, rel, info.ModTime(), f)
	return true
}

// servePrecompressed serves a sidecar .br/.gz file for rel when precompressed
// serving is enabled, the client accepts the coding, and the sidecar exists.
// It returns true if it wrote a response. Range requests fall back to the
// uncompressed file to avoid byte-range/encoding mismatches.
func (h *staticHandler) servePrecompressed(w http.ResponseWriter, r *http.Request, rel string) bool {
	if !h.precompressed || r.Header.Get("Range") != "" {
		return false
	}
	enc, ext := h.precompressedPick(r)
	if enc == "" {
		return false
	}
	sidecar := rel + ext
	if h.isHidden(sidecar) {
		return false
	}
	si, err := h.root.Stat(sidecar)
	if err != nil || si.IsDir() {
		return false
	}
	sf, err := h.root.Open(sidecar)
	if err != nil {
		return false
	}
	defer sf.Close()

	hdr := w.Header()
	// Set Content-Type from the original resource so the compressed bytes are
	// not content-sniffed. Fall back to octet-stream for unknown extensions.
	ctype := mime.TypeByExtension(path.Ext(rel))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	hdr.Set("Content-Type", ctype)
	hdr.Set("Content-Encoding", enc)
	hdr.Add("Vary", "Accept-Encoding")
	hdr.Set("ETag", fmt.Sprintf(`"%x-%x"`, si.ModTime().UnixNano(), si.Size()))
	if h.cacheControl != "" {
		hdr.Set("Cache-Control", h.cacheControl)
	}
	http.ServeContent(w, r, rel, si.ModTime(), sf)
	return true
}

// precompressedPick returns the preferred sidecar coding (br before gzip) that
// is both enabled and accepted by the client, with its file extension.
func (h *staticHandler) precompressedPick(r *http.Request) (enc, ext string) {
	ae := r.Header.Get("Accept-Encoding")
	if ae == "" {
		return "", ""
	}
	if h.encoderEnabled("br") && acceptsToken(ae, "br") {
		return "br", ".br"
	}
	if h.encoderEnabled("gzip") && acceptsToken(ae, "gzip") {
		return "gzip", ".gz"
	}
	return "", ""
}

func (h *staticHandler) encoderEnabled(name string) bool {
	for _, e := range h.precompressEncoders {
		if e == name {
			return true
		}
	}
	return false
}

// acceptsToken reports whether an Accept-Encoding header includes token with a
// non-zero q-value.
func acceptsToken(acceptEncoding, token string) bool {
	for _, part := range strings.Split(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		qv := "1"
		if i := strings.IndexByte(part, ';'); i >= 0 {
			name = strings.TrimSpace(part[:i])
			if j := strings.Index(part[i+1:], "q="); j >= 0 {
				qv = strings.TrimSpace(part[i+1+j+2:])
			}
		}
		if strings.EqualFold(name, token) {
			f, err := strconv.ParseFloat(qv, 64)
			return err != nil || f > 0
		}
	}
	return false
}

func (h *staticHandler) listDir(w http.ResponseWriter, r *http.Request, rel string) {
	f, err := h.root.Open(rel)
	if err != nil {
		h.errPages.Render(w, r, http.StatusNotFound)
		return
	}
	defer f.Close()

	entries, err := f.ReadDir(-1)
	if err != nil {
		h.errPages.Render(w, r, http.StatusInternalServerError)
		return
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !h.allowHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)

	base := path.Clean("/" + r.URL.Path)
	if base != "/" {
		base += "/"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html><head><title>Index of %s</title></head><body>\n", html.EscapeString(base))
	fmt.Fprintf(&b, "<h1>Index of %s</h1>\n<ul>\n", html.EscapeString(base))
	for _, name := range names {
		href := base + name
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a></li>\n", html.EscapeString(href), html.EscapeString(name))
	}
	b.WriteString("</ul>\n</body></html>\n")
	_, _ = w.Write([]byte(b.String()))
}

// isHidden reports whether any path segment is a dotfile, unless hidden files
// are explicitly allowed.
func (h *staticHandler) isHidden(rel string) bool {
	if h.allowHidden {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if len(seg) > 1 && seg[0] == '.' && seg != ".." {
			return true
		}
	}
	return false
}

// expandURI substitutes the $uri variable in a try_files template.
func expandURI(tmpl, uri string) string {
	tmpl = strings.ReplaceAll(tmpl, "$uri/", uri+"/")
	tmpl = strings.ReplaceAll(tmpl, "$uri", uri)
	return tmpl
}

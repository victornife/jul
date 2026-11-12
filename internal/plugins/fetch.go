// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// errFetchBlocked marks a fetch rejected by a guard (host not allow-listed, or
// resolving to a non-routable/private address). The host function maps it to the
// guest's "blocked" return code, distinct from a transport error.
var errFetchBlocked = errors.New("fetch blocked by guard")

// ipResolver resolves a host to IP addresses; net.Resolver satisfies it. The
// seam lets tests inject addresses to exercise the SSRF guard, including a
// rebinding record that resolves to a private address.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// fetchBodyCloseGrace bounds how long a fetch waits, after its response was
// read and its context cancelled, for the transport to close every reader of
// the charged request-body copy. A reader still open then is revoked.
var fetchBodyCloseGrace = 2 * time.Second

// doFetch performs a guarded outbound HTTP request for a plugin holding the
// fetch capability. It enforces the allow-list, an SSRF address guard, a
// per-call timeout, and a response-size cap, and refuses redirects to hosts that
// fall outside the same guards.
//
// body is the charged copy of the guest's request body (nil for none); doFetch
// owns it and refunds it before returning, once no reader it handed to the
// transport can reach it: every reader has closed, or the rest are revoked.
// The response is read into a buffer charged to inv's lease before it is
// allocated; inv retains it for fetch_read until the call ends.
func (p *plugin) doFetch(parent context.Context, inv *invocation, method, rawURL string, body *chargedBuf) (int, []byte, error) {
	var fb *fetchBody
	if body != nil && len(body.b) > 0 {
		fb = &fetchBody{data: body.b, idle: make(chan struct{})}
	}
	defer func() {
		if fb != nil && !fb.waitClosed(fetchBodyCloseGrace, parent.Done()) {
			fb.revoke()
			if p.log != nil {
				p.log.Warn("plugin: fetch request body still held by the transport after the call; its readers were revoked",
					"name", p.name)
			}
		}
		body.release()
	}()
	// Clear any previous fetch response so a failed call does not leave stale
	// data visible to fetch_read / last_fetch_len, and refund it before the
	// next response is charged.
	inv.releaseFetch()

	if method == "" {
		method = "GET"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return 0, nil, errFetchBlocked
	}
	if !hostAllowed(p.allowedHosts, u.Hostname()) {
		return 0, nil, errFetchBlocked
	}

	// Backward compat: tests that construct bare plugin structs may not set
	// p.client; use a temporary one in that rare case. Production always sets
	// p.client in compilePlugin.
	client := p.client
	if client == nil {
		client = newFetchClient(p)
	}

	ctx, cancel := context.WithTimeout(parent, p.fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	if fb != nil {
		req.Body = fb.open()
		req.ContentLength = int64(len(fb.data))
		req.GetBody = func() (io.ReadCloser, error) { return fb.reopen() }
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		if errors.Is(err, errFetchBlocked) {
			return 0, nil, errFetchBlocked
		}
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, truncated, charge, err := p.readFetchResponse(inv, resp)
	cancel()
	if err != nil {
		return 0, nil, err
	}
	inv.lastFetch, inv.lastFetchTruncated, inv.fetchCharge = data, truncated, charge
	return resp.StatusCode, data, nil
}

// readFetchResponse reads at most max_fetch_response+1 bytes into one buffer
// charged before it is allocated: the declared length plus the detection
// byte when it is within the cap, else the whole cap plus the detection
// byte. A declared length never caps the read, and the buffer never grows.
func (p *plugin) readFetchResponse(inv *invocation, resp *http.Response) ([]byte, bool, byteCharge, error) {
	ceiling := p.maxFetchResp + 1
	size := ceiling
	if cl := resp.ContentLength; cl >= 0 && cl < int64(p.maxFetchResp) {
		size = int(cl) + 1
	}
	cb, err := newChargedBuf(inv.chargeFn(), size)
	if err != nil {
		inv.failCharge(err)
		return nil, false, nil, err
	}
	buf := cb.b[:size]
	// Only a clean EOF completes the body: io.ReadFull would conflate the
	// transport's own io.ErrUnexpectedEOF (a response cut short of its
	// declared length) with a short read.
	lr := io.LimitReader(resp.Body, int64(ceiling))
	n := 0
	for n < size && err == nil {
		var k int
		k, err = lr.Read(buf[n:])
		n += k
	}
	if err == io.EOF {
		err = nil
	}
	if err == nil && n == size && size < ceiling {
		// More bytes than the declared length: a framing violation the
		// transport did not catch. Refuse rather than grow.
		err = errors.New("fetch response longer than its declared length")
	}
	if err != nil {
		cb.release()
		return nil, false, nil, err
	}
	data := buf[:n]
	truncated := n > p.maxFetchResp
	if truncated {
		data = data[:p.maxFetchResp]
	}
	return data, truncated, cb.c, nil
}

// fetchBody serves a charged request-body copy to the fetch transport. Every
// reader it hands out (the body and any GetBody replays) is tracked until
// closed. The transport may close a request body asynchronously after
// RoundTrip returns; a reader it still holds when the call ends is revoked,
// so the copy is unreachable from it before the copy is refunded.
type fetchBody struct {
	data    []byte
	mu      sync.Mutex
	readers map[*fetchBodyReader]struct{}
	sealed  bool
	idle    chan struct{}
}

func (fb *fetchBody) open() io.ReadCloser {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.openLocked()
}

func (fb *fetchBody) openLocked() *fetchBodyReader {
	r := &fetchBodyReader{fb: fb, r: bytes.NewReader(fb.data)}
	if fb.readers == nil {
		fb.readers = make(map[*fetchBodyReader]struct{})
	}
	fb.readers[r] = struct{}{}
	return r
}

// reopen serves a GetBody replay. Sealing and opening are one step, so a
// replay can never start from a revoked, emptied copy.
func (fb *fetchBody) reopen() (io.ReadCloser, error) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.sealed {
		return nil, errors.New("fetch request body no longer available")
	}
	return fb.openLocked(), nil
}

func (fb *fetchBody) closed(r *fetchBodyReader) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if _, ok := fb.readers[r]; !ok {
		return
	}
	delete(fb.readers, r)
	if len(fb.readers) == 0 && fb.sealed {
		close(fb.idle)
	}
}

// waitClosed seals the body against new readers and waits up to grace, or
// until done, for every reader to close. It reports whether they all did.
func (fb *fetchBody) waitClosed(grace time.Duration, done <-chan struct{}) bool {
	fb.mu.Lock()
	if !fb.sealed {
		fb.sealed = true
		if len(fb.readers) == 0 {
			close(fb.idle)
		}
	}
	fb.mu.Unlock()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-fb.idle:
		return true
	case <-t.C:
	case <-done:
	}
	select {
	case <-fb.idle:
		return true
	default:
		return false
	}
}

// revoke detaches the copy from every reader still open. A Read in progress
// finishes its copy first; later reads fail. Afterwards no reader, and no
// GetBody replay, can reach the copy, so it can be refunded while the
// transport still holds the readers.
func (fb *fetchBody) revoke() {
	fb.mu.Lock()
	fb.sealed, fb.data = true, nil
	open := make([]*fetchBodyReader, 0, len(fb.readers))
	for r := range fb.readers {
		open = append(open, r)
	}
	fb.mu.Unlock()
	for _, r := range open {
		r.mu.Lock()
		r.r, r.revoked = nil, true
		r.mu.Unlock()
	}
}

type fetchBodyReader struct {
	fb      *fetchBody
	mu      sync.Mutex
	r       *bytes.Reader
	closed  bool
	revoked bool
}

func (r *fetchBodyReader) Read(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.closed:
		return 0, errors.New("fetch request body closed")
	case r.revoked:
		return 0, errors.New("fetch request body revoked after the call")
	}
	return r.r.Read(b)
}

func (r *fetchBodyReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed, r.r = true, nil
	r.mu.Unlock()
	r.fb.closed(r)
	return nil
}

// dialer matches net.Dialer's DialContext method so dialValidatedIPs can be
// unit-tested with a mock.
type dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// dialValidatedIPs resolves host, validates every IP against the SSRF guard,
// and then tries each validated IP in order until one connects. The original
// Host header and SNI are preserved by the surrounding DialContext because
// it only substitutes the dial target; the HTTP layer keeps the original name.
func dialValidatedIPs(ctx context.Context, d dialer, resolver ipResolver, network, host, port string) (net.Conn, error) {
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if ipBlocked(ip.IP) {
			return nil, errFetchBlocked
		}
	}
	if len(ips) == 0 {
		return nil, errFetchBlocked
	}

	// Try each validated IP in order until one connects.
	var lastErr error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errFetchBlocked
}

// hostAllowed reports whether host matches the allow-list. An entry may be an
// exact host or a leading-dot suffix ("\u002eexample.com") matching subdomains.
func hostAllowed(allowed []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == host {
			return true
		}
		if strings.HasPrefix(a, ".") && strings.HasSuffix(host, a) {
			return true
		}
	}
	return false
}

// ipBlocked reports whether dialing ip risks SSRF: loopback, link-local,
// private, multicast, unspecified, or unique-local addresses are all refused.
func ipBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// IsPrivate covers IPv6 unique-local, but does not cover special-use IPv4
	// ranges that can be routed inside a local network. Reject those before
	// dialing even when an allow-listed name resolves to one of them.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		if v4[0] == 0 || v4[0] >= 240 || (v4[0] == 198 && (v4[1] == 18 || v4[1] == 19)) {
			return true
		}
	}
	return false
}

// fetchDial builds the plugin fetch DialContext: an SSRF-validating dialer
// (resolve, refuse loopback/private/CGNAT, dial a validated IP) composed beneath
// the optional global egress guard, so a fetch must satisfy both. When present,
// the egress guard evaluates the requested host and every resolved IP first and
// refuses a destination outside the [egress] allow-list before the SSRF dialer
// runs; its refusals wrap egress.ErrBlocked so the host maps them to a distinct
// guest code.
func (p *plugin) fetchDial(d dialer, resolver ipResolver) func(ctx context.Context, network, addr string) (net.Conn, error) {
	ssrf := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return dialValidatedIPs(ctx, d, resolver, network, host, port)
	}
	if p.egressWrap != nil {
		return p.egressWrap(ssrf)
	}
	return ssrf
}

// newFetchClient builds a temporary http.Client for doFetch when p.client is
// nil (backwards-compat for tests that construct bare plugin structs).
func newFetchClient(p *plugin) *http.Client {
	dialer := &net.Dialer{Timeout: p.fetchTimeout}
	resolver := p.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &http.Client{
		Timeout:   p.fetchTimeout,
		Transport: &http.Transport{DialContext: p.fetchDial(dialer, resolver)},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !hostAllowed(p.allowedHosts, req.URL.Hostname()) {
				return errFetchBlocked
			}
			return nil
		},
	}
}

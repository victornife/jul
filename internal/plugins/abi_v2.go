// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"golang.org/x/net/http/httpguts"

	"jul/internal/egress"
)

// jul-abi/v2 wire constants. Every numeric value here is frozen by
// testdata/plugins/abi-v2.golden; new values are additive only (ADR 0020 §13).

// handle_request results (same values as v1; others are reserved and fail).
const (
	actionStop     uint32 = 0
	actionContinue uint32 = 1
)

// subscribe_response modes.
const (
	responseModeMetadata uint32 = 0
	responseModeBody     uint32 = 1
)

// handle_response results (others are reserved and fail).
const (
	responseContinue uint32 = 0
	responseReject   uint32 = 1
)

// resp_body_state values.
const (
	bodyAvailable    uint32 = 0
	bodyNotRequested uint32 = 1
	bodyNone         uint32 = 2
	bodyTooLarge     uint32 = 3
	bodyStreaming    uint32 = 4
	bodyEncoded      uint32 = 5
	bodyPartial      uint32 = 6
	bodyUpgraded     uint32 = 7
)

// Return codes of the v2-only host functions.
const (
	codeOK          int32 = 0
	codeNotFound    int32 = -1
	codeInvalid     int32 = -2
	codeForbidden   int32 = -3
	codeUnavailable int32 = -4
	codeTooLarge    int32 = -5
	codeCommitted   int32 = -6
	codeWrongPhase  int32 = -7
	codeUnsupported int32 = -8
)

// Per-invocation bounds.
const (
	maxRequestState     = 4096
	maxAddedHeaderBytes = 64 << 10
)

// bodyStateLabel is the closed metric label for an unavailable body state.
func bodyStateLabel(s uint32) string {
	switch s {
	case bodyNone:
		return "none"
	case bodyTooLarge:
		return "too_large"
	case bodyStreaming:
		return "streaming"
	case bodyEncoded:
		return "encoded"
	case bodyPartial:
		return "partial"
	case bodyUpgraded:
		return "upgraded"
	default:
		return ""
	}
}

var (
	errGuestMemory = errors.New("plugin: host call used an out-of-bounds guest pointer")
	errWrongPhase  = errors.New("plugin: request-phase host call made from handle_response")
	errBadResult   = errors.New("plugin: guest returned a reserved result value")
)

// invocationPhase is which guest export an invocation runs.
type invocationPhase uint8

const (
	phaseRequest invocationPhase = iota
	phaseResponse
)

// responseView is the response a handle_response invocation reads and
// mutates. header is the live response header map; the host owns framing.
type responseView struct {
	origStatus int
	status     int
	statusSet  bool
	header     http.Header
	body       []byte
	bodyState  uint32
	committed  bool
	replaced   bool
}

// forbiddenResponseHeaders are framing/hop-by-hop names a guest can never set
// or delete (ADR 0020 §7). Trailer-prefixed names are not valid tokens and are
// rejected as invalid before this check.
var forbiddenResponseHeaders = map[string]bool{
	"Connection":        true,
	"Keep-Alive":        true,
	"Proxy-Connection":  true,
	"Transfer-Encoding": true,
	"Te":                true,
	"Trailer":           true,
	"Upgrade":           true,
	"Content-Length":    true,
	"Content-Encoding":  true,
	"Content-Range":     true,
}

func forbiddenResponseHeader(canonical string) bool {
	return forbiddenResponseHeaders[canonical]
}

func (inv *invocation) fail(err error) {
	if inv.err == nil {
		inv.err = err
	}
}

// v2read copies guest memory; an out-of-bounds range fails the invocation.
func v2read(inv *invocation, m api.Module, ptr, n uint32) ([]byte, bool) {
	b, ok := m.Memory().Read(ptr, n)
	if !ok {
		inv.fail(errGuestMemory)
		return nil, false
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, true
}

func v2str(inv *invocation, m api.Module, ptr, n uint32) (string, bool) {
	b, ok := m.Memory().Read(ptr, n)
	if !ok {
		inv.fail(errGuestMemory)
		return "", false
	}
	return string(b), true
}

// v2write is the caller-allocates convention with strict bounds: it copies
// data only when it fits and always returns the full length.
func v2write(inv *invocation, m api.Module, buf, limit uint32, data []byte) uint32 {
	n := uint32(len(data))
	if n != 0 && n <= limit && !m.Memory().Write(buf, data) {
		inv.fail(errGuestMemory)
	}
	return n
}

// registerJulV2HostModule instantiates the jul-abi/v2 host module ("jul-abi/v2").
// The request-phase functions keep their v1 names, signatures and semantics
// (ADR 0020 §2) with strict guest-memory bounds and phase checks; the response
// functions operate on the invocation's responseView.
func registerJulV2HostModule(ctx context.Context, r wazero.Runtime, p *plugin) error {
	kvNamespace := p.name + "\x00"
	b := r.NewHostModuleBuilder(hostModuleV2)
	exp := func(name string, fn any) {
		b.NewFunctionBuilder().WithFunc(fn).Export(name)
	}
	// requestOnly fails an invocation that calls a request-mutating function
	// from handle_response.
	requestOnly := func(inv *invocation) bool {
		if inv.phase != phaseRequest {
			inv.fail(errWrongPhase)
			return false
		}
		return true
	}
	// mutableResponse is the shared gate of the response mutators.
	mutableResponse := func(inv *invocation) int32 {
		if inv.phase != phaseResponse {
			return codeWrongPhase
		}
		if inv.resp.committed {
			return codeCommitted
		}
		return codeOK
	}
	headerArgs := func(inv *invocation, m api.Module, namePtr, nameLen uint32) (string, int32) {
		name, ok := v2str(inv, m, namePtr, nameLen)
		if !ok {
			return "", codeInvalid
		}
		if !httpguts.ValidHeaderFieldName(name) {
			return "", codeInvalid
		}
		name = http.CanonicalHeaderKey(name)
		if forbiddenResponseHeader(name) {
			return "", codeForbidden
		}
		return name, codeOK
	}

	// ---- request-phase surface (v1 names and semantics) --------------------

	exp("log", func(ctx context.Context, m api.Module, level, ptr, n uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		msg, ok := v2str(inv, m, ptr, n)
		if !ok {
			return
		}
		switch level {
		case 0:
			inv.log.Debug("plugin", "name", p.name, "msg", msg)
		case 2:
			inv.log.Warn("plugin", "name", p.name, "msg", msg)
		case 3:
			inv.log.Error("plugin", "name", p.name, "msg", msg)
		default:
			inv.log.Info("plugin", "name", p.name, "msg", msg)
		}
	})

	exp("get_method", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return v2write(inv, m, buf, limit, []byte(inv.r.Method))
	})

	exp("get_uri", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return v2write(inv, m, buf, limit, []byte(inv.r.URL.RequestURI()))
	})

	exp("set_uri", func(ctx context.Context, m api.Module, ptr, n uint32) {
		inv := invocationFrom(ctx)
		if inv == nil || !requestOnly(inv) {
			return
		}
		raw, ok := v2str(inv, m, ptr, n)
		if !ok {
			return
		}
		u, err := url.ParseRequestURI(raw)
		if err != nil {
			return
		}
		inv.r.URL.Path = u.Path
		inv.r.URL.RawQuery = u.RawQuery
		inv.r.RequestURI = raw
	})

	exp("get_request_header", func(ctx context.Context, m api.Module, namePtr, nameLen, buf, limit uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return -1
		}
		name, ok := v2str(inv, m, namePtr, nameLen)
		if !ok || len(inv.r.Header.Values(name)) == 0 {
			return -1
		}
		return int32(v2write(inv, m, buf, limit, []byte(inv.r.Header.Get(name))))
	})

	exp("set_request_header", func(ctx context.Context, m api.Module, namePtr, nameLen, valPtr, valLen uint32) {
		inv := invocationFrom(ctx)
		if inv == nil || !requestOnly(inv) {
			return
		}
		name, ok1 := v2str(inv, m, namePtr, nameLen)
		val, ok2 := v2str(inv, m, valPtr, valLen)
		if ok1 && ok2 {
			inv.r.Header.Set(name, val)
		}
	})

	exp("set_response_header", func(ctx context.Context, m api.Module, namePtr, nameLen, valPtr, valLen uint32) {
		inv := invocationFrom(ctx)
		if inv == nil || !requestOnly(inv) {
			return
		}
		name, ok1 := v2str(inv, m, namePtr, nameLen)
		val, ok2 := v2str(inv, m, valPtr, valLen)
		if ok1 && ok2 {
			inv.w.Header().Set(name, val)
		}
	})

	exp("read_request_body", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil || !requestOnly(inv) {
			return 0
		}
		if !inv.bodyBuffered {
			inv.bodyBuffered = true
			if inv.r.Body != nil {
				data, _ := io.ReadAll(io.LimitReader(inv.r.Body, int64(inv.maxReqBody)+1))
				if len(data) > inv.maxReqBody {
					inv.fail(errBodyTooLarge)
					return 0
				}
				inv.body = data
				inv.r.Body = io.NopCloser(bytes.NewReader(data))
				inv.r.ContentLength = int64(len(data))
			}
		}
		return v2write(inv, m, buf, limit, inv.body)
	})

	exp("write_response_body", func(ctx context.Context, m api.Module, ptr, n uint32) {
		inv := invocationFrom(ctx)
		if inv == nil || !requestOnly(inv) {
			return
		}
		if len(inv.respBody)+int(n) > inv.maxRespBody {
			inv.fail(errRespTooLarge)
			return
		}
		if data, ok := v2read(inv, m, ptr, n); ok {
			inv.respBody = append(inv.respBody, data...)
		}
	})

	exp("set_response_status", func(ctx context.Context, m api.Module, code uint32) {
		inv := invocationFrom(ctx)
		if inv == nil || !requestOnly(inv) {
			return
		}
		inv.status = int(code)
	})

	exp("get_config", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return writeInto(m, buf, limit, p.configJSON)
		}
		return v2write(inv, m, buf, limit, p.configJSON)
	})

	exp("kv_get", func(ctx context.Context, m api.Module, keyPtr, keyLen, buf, limit uint32) int32 {
		if !p.capKV {
			return -2
		}
		inv := invocationFrom(ctx)
		if inv == nil {
			return -1
		}
		key, ok := v2str(inv, m, keyPtr, keyLen)
		if !ok {
			return -1
		}
		v, found := p.kv.Get(kvNamespace + key)
		if !found {
			return -1
		}
		return int32(v2write(inv, m, buf, limit, v))
	})

	exp("kv_set", func(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) int32 {
		if !p.capKV {
			return -2
		}
		inv := invocationFrom(ctx)
		if inv == nil {
			return -1
		}
		key, ok1 := v2str(inv, m, keyPtr, keyLen)
		val, ok2 := v2read(inv, m, valPtr, valLen)
		if !ok1 || !ok2 {
			return -1
		}
		if !p.kvSet(kvNamespace+key, val) {
			return -3
		}
		return 0
	})

	exp("fetch", func(ctx context.Context, m api.Module, methodPtr, methodLen, urlPtr, urlLen, bodyPtr, bodyLen, buf, limit uint32) int32 {
		if !p.capFetch {
			return -2
		}
		inv := invocationFrom(ctx)
		if inv == nil {
			return -4
		}
		method, ok1 := v2str(inv, m, methodPtr, methodLen)
		rawURL, ok2 := v2str(inv, m, urlPtr, urlLen)
		body, ok3 := v2read(inv, m, bodyPtr, bodyLen)
		if !ok1 || !ok2 || !ok3 {
			return -4
		}
		status, respBody, err := p.doFetch(ctx, method, rawURL, body)
		if err != nil {
			inv.log.Warn("plugin: fetch denied", "name", p.name, "url", rawURL, "err", err)
			switch {
			case errors.Is(err, egress.ErrBlocked):
				return -5
			case errors.Is(err, errFetchBlocked):
				return -3
			default:
				return -4
			}
		}
		v2write(inv, m, buf, limit, respBody)
		return int32(status)
	})

	exp("last_fetch_len", func(ctx context.Context, m api.Module) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return uint32(len(inv.lastFetch))
	})

	exp("fetch_read", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return v2write(inv, m, buf, limit, inv.lastFetch)
	})

	exp("last_fetch_truncated", func(ctx context.Context, m api.Module) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil || !inv.lastFetchTruncated {
			return 0
		}
		return 1
	})

	// ---- phase plumbing ---------------------------------------------------

	exp("subscribe_response", func(ctx context.Context, m api.Module, mode uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseRequest {
			return codeWrongPhase
		}
		if p.isHandler || !p.hasResponse {
			return codeUnsupported
		}
		if mode != responseModeMetadata && mode != responseModeBody {
			return codeInvalid
		}
		inv.sub = int32(mode)
		return codeOK
	})

	exp("set_request_state", func(ctx context.Context, m api.Module, ptr, n uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseRequest {
			return codeWrongPhase
		}
		if n > maxRequestState {
			return codeTooLarge
		}
		data, ok := v2read(inv, m, ptr, n)
		if !ok {
			return codeInvalid
		}
		inv.state = data
		return codeOK
	})

	exp("get_request_state", func(ctx context.Context, m api.Module, buf, limit uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return codeWrongPhase
		}
		return int32(v2write(inv, m, buf, limit, inv.state))
	})

	// ---- response phase ---------------------------------------------------

	exp("resp_status", func(ctx context.Context, m api.Module) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseResponse {
			return codeWrongPhase
		}
		return int32(inv.resp.status)
	})

	exp("resp_set_status", func(ctx context.Context, m api.Module, code uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return codeWrongPhase
		}
		if rc := mutableResponse(inv); rc != codeOK {
			return rc
		}
		if inv.resp.origStatus == http.StatusNoContent || inv.resp.origStatus == http.StatusNotModified {
			return codeForbidden
		}
		if code < 200 || code > 599 || code == http.StatusNoContent || code == http.StatusResetContent || code == http.StatusNotModified {
			return codeInvalid
		}
		inv.resp.status = int(code)
		inv.resp.statusSet = true
		return codeOK
	})

	exp("resp_get_header", func(ctx context.Context, m api.Module, namePtr, nameLen, index, buf, limit uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseResponse {
			return codeWrongPhase
		}
		name, ok := v2str(inv, m, namePtr, nameLen)
		if !ok || !httpguts.ValidHeaderFieldName(name) {
			return codeInvalid
		}
		vals := inv.resp.header.Values(name)
		if uint64(index) >= uint64(len(vals)) {
			return codeNotFound
		}
		return int32(v2write(inv, m, buf, limit, []byte(vals[index])))
	})

	exp("resp_header_names", func(ctx context.Context, m api.Module, buf, limit uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseResponse {
			return codeWrongPhase
		}
		names := make([]string, 0, len(inv.resp.header))
		for k := range inv.resp.header {
			names = append(names, k)
		}
		sort.Strings(names)
		return int32(v2write(inv, m, buf, limit, []byte(strings.Join(names, "\n"))))
	})

	setOrAdd := func(add bool) func(ctx context.Context, m api.Module, namePtr, nameLen, valPtr, valLen uint32) int32 {
		return func(ctx context.Context, m api.Module, namePtr, nameLen, valPtr, valLen uint32) int32 {
			inv := invocationFrom(ctx)
			if inv == nil {
				return codeWrongPhase
			}
			if rc := mutableResponse(inv); rc != codeOK {
				return rc
			}
			name, rc := headerArgs(inv, m, namePtr, nameLen)
			if rc != codeOK {
				return rc
			}
			val, ok := v2str(inv, m, valPtr, valLen)
			if !ok || !httpguts.ValidHeaderFieldValue(val) {
				return codeInvalid
			}
			if inv.addedHeaderBytes+len(name)+len(val) > maxAddedHeaderBytes {
				return codeTooLarge
			}
			inv.addedHeaderBytes += len(name) + len(val)
			if add {
				inv.resp.header.Add(name, val)
			} else {
				inv.resp.header.Set(name, val)
			}
			return codeOK
		}
	}
	exp("resp_set_header", setOrAdd(false))
	exp("resp_add_header", setOrAdd(true))

	exp("resp_del_header", func(ctx context.Context, m api.Module, namePtr, nameLen uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return codeWrongPhase
		}
		if rc := mutableResponse(inv); rc != codeOK {
			return rc
		}
		name, rc := headerArgs(inv, m, namePtr, nameLen)
		if rc != codeOK {
			return rc
		}
		inv.resp.header.Del(name)
		return codeOK
	})

	exp("resp_body_state", func(ctx context.Context, m api.Module) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseResponse {
			return codeWrongPhase
		}
		return int32(inv.resp.bodyState)
	})

	exp("resp_body_read", func(ctx context.Context, m api.Module, buf, limit uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil || inv.phase != phaseResponse {
			return codeWrongPhase
		}
		if inv.resp.bodyState != bodyAvailable {
			return codeUnavailable
		}
		return int32(v2write(inv, m, buf, limit, inv.resp.body))
	})

	exp("resp_body_replace", func(ctx context.Context, m api.Module, ptr, n uint32) int32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return codeWrongPhase
		}
		if rc := mutableResponse(inv); rc != codeOK {
			return rc
		}
		if inv.resp.bodyState != bodyAvailable {
			return codeUnavailable
		}
		if uint64(n) > uint64(inv.maxRespBody) {
			return codeTooLarge
		}
		data, ok := v2read(inv, m, ptr, n)
		if !ok {
			return codeInvalid
		}
		inv.resp.body = data
		inv.resp.replaced = true
		return codeOK
	})

	_, err := b.Instantiate(ctx)
	return err
}

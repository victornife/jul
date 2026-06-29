//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// maxRequestBodyBuffer caps how much of a request body the host buffers so a
// guest can read it (and the next handler can re-read it). Bodies larger than
// this default ceiling fail the guest call; per-plugin max_request_body overrides.
const maxRequestBodyBuffer = 1 << 20 // 1 MiB

// maxResponseBodyBuffer caps the response body a guest may accumulate.
const maxResponseBodyBuffer = 8 << 20 // 8 MiB

// errBodyTooLarge and errRespTooLarge fail an invocation when the guest's view
// of the request body would be truncated, or its response would overflow, rather
// than silently serving partial data.
var (
	errBodyTooLarge = errors.New("plugin: request body exceeds max_request_body")
	errRespTooLarge = errors.New("plugin: response body exceeds max_response_body")
)

// invocation is the per-request state the host functions read from the call
// context. One is created per HTTP request and never shared across goroutines.
type invocation struct {
	r   *http.Request
	w   http.ResponseWriter
	log *slog.Logger

	// bodyBuffered records whether the request body has been read into body.
	bodyBuffered bool
	body         []byte

	// status and respBody accumulate the response the guest wants to send. They
	// are flushed to w only after handle_request returns, so a trapped guest
	// never leaves a half-written response.
	status   int
	respBody []byte

	// maxReqBody / maxRespBody bound how much request body is buffered and how
	// much response body is accumulated. err records the first host-side failure
	// (oversize body, response overflow) so invoke turns it into a 500 instead of
	// serving truncated data.
	maxReqBody  int
	maxRespBody int
	err         error

	// lastFetch holds the most recent retained/capped fetch response so
	// last_fetch_len and fetch_read implement the caller-allocates grow-and-retry
	// convention: a guest can size its buffer to the retained length and re-read
	// without a second outbound call.
	lastFetch []byte

	// lastFetchTruncated reports whether the most recent fetch response exceeded
	// max_fetch_response and was truncated.
	lastFetchTruncated bool
}

type invCtxKey struct{}

func withInvocation(ctx context.Context, inv *invocation) context.Context {
	return context.WithValue(ctx, invCtxKey{}, inv)
}

func invocationFrom(ctx context.Context) *invocation {
	inv, _ := ctx.Value(invCtxKey{}).(*invocation)
	return inv
}

// flush writes the guest's accumulated status and body to the real
// ResponseWriter. Response headers set via set_response_header were written to
// w.Header() during the call and are committed by WriteHeader here.
func (inv *invocation) flush() {
	status := inv.status
	if status == 0 {
		status = http.StatusOK
	}
	// A guest may only set a real HTTP status; an out-of-range value is rejected
	// rather than passed to WriteHeader (which would panic).
	if status < 100 || status > 599 {
		inv.log.Warn("plugin: invalid response status replaced with 500", "status", status)
		status = http.StatusInternalServerError
	}
	inv.w.WriteHeader(status)
	if len(inv.respBody) > 0 {
		_, _ = inv.w.Write(inv.respBody)
	}
}

// readMem returns a copy of count bytes of guest memory at ptr.
func readMem(m api.Module, ptr, count uint32) ([]byte, bool) {
	b, ok := m.Memory().Read(ptr, count)
	if !ok {
		return nil, false
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, true
}

func readStr(m api.Module, ptr, count uint32) string {
	b, ok := m.Memory().Read(ptr, count)
	if !ok {
		return ""
	}
	return string(b)
}

// writeInto implements the caller-allocates host->guest convention: it copies
// data into the guest buffer [buf, buf+limit) only when it fits, and always
// returns the full length so a guest that supplied too small a buffer can grow
// it and call again.
func writeInto(m api.Module, buf, limit uint32, data []byte) uint32 {
	n := uint32(len(data))
	if n != 0 && n <= limit {
		m.Memory().Write(buf, data)
	}
	return n
}

// registerJulHostModule instantiates the jul-abi/v1 host module ("jul") on the
// runtime, with all host functions closing over the plugin's capabilities,
// config, and the shared KV store. Per-request data is read from the call
// context via invocationFrom.
func registerJulHostModule(ctx context.Context, r wazero.Runtime, p *plugin) error {
	kvNamespace := p.name + "\x00"

	b := r.NewHostModuleBuilder("jul")

	exp := func(name string, fn any) {
		b.NewFunctionBuilder().WithFunc(fn).Export(name)
	}

	exp("log", func(ctx context.Context, m api.Module, level, ptr, n uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		msg := readStr(m, ptr, n)
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
		return writeInto(m, buf, limit, []byte(inv.r.Method))
	})

	exp("get_uri", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return writeInto(m, buf, limit, []byte(inv.r.URL.RequestURI()))
	})

	exp("set_uri", func(ctx context.Context, m api.Module, ptr, n uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		raw := readStr(m, ptr, n)
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
		name := readStr(m, namePtr, nameLen)
		if len(inv.r.Header.Values(name)) == 0 {
			return -1
		}
		return int32(writeInto(m, buf, limit, []byte(inv.r.Header.Get(name))))
	})

	exp("set_request_header", func(ctx context.Context, m api.Module, namePtr, nameLen, valPtr, valLen uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		inv.r.Header.Set(readStr(m, namePtr, nameLen), readStr(m, valPtr, valLen))
	})

	exp("set_response_header", func(ctx context.Context, m api.Module, namePtr, nameLen, valPtr, valLen uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		inv.w.Header().Set(readStr(m, namePtr, nameLen), readStr(m, valPtr, valLen))
	})

	exp("read_request_body", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		if !inv.bodyBuffered {
			inv.bodyBuffered = true
			if inv.r.Body != nil {
				// Read one byte past the cap to detect overflow; an oversize body
				// fails the call rather than handing the guest a truncated view.
				data, _ := io.ReadAll(io.LimitReader(inv.r.Body, int64(inv.maxReqBody)+1))
				if len(data) > inv.maxReqBody {
					inv.err = errBodyTooLarge
					return 0
				}
				inv.body = data
				// Restore the body so the next handler can read it in full.
				inv.r.Body = io.NopCloser(bytes.NewReader(data))
				inv.r.ContentLength = int64(len(data))
			}
		}
		return writeInto(m, buf, limit, inv.body)
	})

	exp("write_response_body", func(ctx context.Context, m api.Module, ptr, n uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		if len(inv.respBody)+int(n) > inv.maxRespBody {
			inv.err = errRespTooLarge
			return
		}
		data, ok := readMem(m, ptr, n)
		if !ok {
			return
		}
		inv.respBody = append(inv.respBody, data...)
	})

	exp("set_response_status", func(ctx context.Context, m api.Module, code uint32) {
		inv := invocationFrom(ctx)
		if inv == nil {
			return
		}
		inv.status = int(code)
	})

	exp("get_config", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		return writeInto(m, buf, limit, p.configJSON)
	})

	exp("kv_get", func(ctx context.Context, m api.Module, keyPtr, keyLen, buf, limit uint32) int32 {
		if !p.capKV {
			return -2
		}
		key := kvNamespace + readStr(m, keyPtr, keyLen)
		v, ok := p.kv.Get(key)
		if !ok {
			return -1
		}
		return int32(writeInto(m, buf, limit, v))
	})

	exp("kv_set", func(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) int32 {
		if !p.capKV {
			return -2
		}
		key := kvNamespace + readStr(m, keyPtr, keyLen)
		val, ok := readMem(m, valPtr, valLen)
		if !ok {
			return -1
		}
		if !p.kvSet(key, val) {
			return -3 // quota exceeded
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
		method := readStr(m, methodPtr, methodLen)
		rawURL := readStr(m, urlPtr, urlLen)
		body, _ := readMem(m, bodyPtr, bodyLen)
		status, respBody, err := p.doFetch(ctx, method, rawURL, body)
		if err != nil {
			inv.log.Warn("plugin: fetch denied", "name", p.name, "url", rawURL, "err", err)
			if errors.Is(err, errFetchBlocked) {
				return -3
			}
			return -4
		}
		writeInto(m, buf, limit, respBody)
		return int32(status)
	})

	// last_fetch_len reports the length of the most recent fetch response so a
	// guest can size a buffer before fetch_read.
	exp("last_fetch_len", func(ctx context.Context, m api.Module) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return uint32(len(inv.lastFetch))
	})

	// fetch_read copies the most recent fetch response into the guest buffer,
	// returning the full length (caller-allocates) so a short buffer can grow and
	// retry without another outbound call.
	exp("fetch_read", func(ctx context.Context, m api.Module, buf, limit uint32) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		return writeInto(m, buf, limit, inv.lastFetch)
	})

	// last_fetch_truncated reports whether the most recent fetch response
	// exceeded max_fetch_response and was truncated.
	exp("last_fetch_truncated", func(ctx context.Context, m api.Module) uint32 {
		inv := invocationFrom(ctx)
		if inv == nil {
			return 0
		}
		if inv.lastFetchTruncated {
			return 1
		}
		return 0
	})

	_, err := b.Instantiate(ctx)
	return err
}

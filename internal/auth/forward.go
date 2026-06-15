package auth

import (
	"context"
	"net/http"
	"time"
)

// forwardAuth delegates the authentication decision to an external service. The
// configured URL receives a subrequest carrying the original request's method,
// path, and a curated set of forwarded headers. A 2xx response authorizes the
// request and selected response headers are copied onto the upstream request;
// any other status is propagated back to the client.
type forwardAuth struct {
	url     string
	headers []string // response headers to copy on success
	client  *http.Client
}

func newForwardAuth(url string, responseHeaders []string, client *http.Client) *forwardAuth {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			// Do not follow redirects: a 3xx from the auth service is a
			// decision, not a hop to chase.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &forwardAuth{url: url, headers: responseHeaders, client: client}
}

// forwardResult carries the outcome of a forward-auth subrequest.
type forwardResult struct {
	ok         bool
	statusCode int
	body       []byte
	header     http.Header
	// copyHeaders are the AuthResponseHeaders to apply to the upstream request.
	copyHeaders http.Header
}

// decide performs the forward-auth subrequest for r.
func (f *forwardAuth) decide(ctx context.Context, r *http.Request) (forwardResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return forwardResult{}, err
	}
	// Convey the original request context to the auth service. X-Forwarded-*
	// describe the original request; the auth service authenticates against it.
	req.Header.Set("X-Forwarded-Method", r.Method)
	req.Header.Set("X-Forwarded-Uri", r.URL.RequestURI())
	if host := r.Host; host != "" {
		req.Header.Set("X-Forwarded-Host", host)
	}
	copyForwardHeaders(req.Header, r.Header)

	resp, err := f.client.Do(req)
	if err != nil {
		return forwardResult{}, err
	}
	defer resp.Body.Close()
	body, _ := readLimited(resp.Body, 64<<10)

	res := forwardResult{
		statusCode: resp.StatusCode,
		body:       body,
		header:     resp.Header.Clone(),
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.ok = true
		res.copyHeaders = make(http.Header)
		for _, name := range f.headers {
			if v := resp.Header.Values(name); len(v) > 0 {
				for _, vv := range v {
					res.copyHeaders.Add(name, vv)
				}
			}
		}
	}
	return res, nil
}

// copyForwardHeaders copies identity-relevant request headers to the subrequest
// while dropping hop-by-hop headers that must not be forwarded.
func copyForwardHeaders(dst, src http.Header) {
	for name, vals := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, v := range vals {
			dst.Add(name, v)
		}
	}
}

// hopByHopHeaders are connection-scoped headers that must not be forwarded to
// the auth service (RFC 7230 §6.1).
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Content-Length":      true,
}

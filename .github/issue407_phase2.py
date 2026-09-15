from pathlib import Path


def replace_once(path, old, new):
    p = Path(path)
    s = p.read_text()
    n = s.count(old)
    if n != 1:
        raise SystemExit(f"{path}: expected one match, found {n}")
    p.write_text(s.replace(old, new, 1))


def append_once(path, marker, text):
    p = Path(path)
    s = p.read_text()
    if marker in s:
        return
    p.write_text(s.rstrip() + "\n\n" + text.strip() + "\n")

# Importer: named NGINX upstreams already preserve unix:/... addresses. Direct
# NGINX Unix proxy_pass cannot be represented by Jul's deliberately narrower
# public grammar, so omit that location and emit actionable provenance instead
# of generating a broken Jul target.
replace_once(
    "internal/migrate/nginx/translate.go",
    '''\tloc := config.LocationConfig{Match: match}
\troot := serverRoot
\tindex := serverIndex

\tfor _, c := range children(d) {''',
    '''\tloc := config.LocationConfig{Match: match}
\troot := serverRoot
\tindex := serverIndex

\t// Jul exposes HTTP-over-Unix through a named upstream only. NGINX also
\t// accepts direct Unix proxy_pass spellings; translating one verbatim would
\t// produce a Jul configuration that looks valid to the importer but is
\t// intentionally unsupported by the runtime. Fail this location closed and
\t// leave an actionable, source-located finding instead.
\tfor _, c := range children(d) {
\t\tif c.GetName() != "proxy_pass" {
\t\t\tcontinue
\t\t}
\t\tcp := paramValues(c)
\t\tif len(cp) > 0 && isDirectUnixProxyPass(cp[0]) {
\t\t\tt.report.skip(c, "direct Unix proxy_pass is not representable; create a named [[upstreams]] entry with servers = [\\"unix:/path/to/socket.sock\\"] and proxy_pass = \\"http://<upstream-name>\\"")
\t\t\treturn config.LocationConfig{}, false
\t\t}
\t}

\tfor _, c := range children(d) {''',
)
replace_once(
    "internal/migrate/nginx/translate.go",
    '''func translateProxyPass(v string, rep *Report, line int) string {
\tv = strings.TrimSpace(v)''',
    '''func isDirectUnixProxyPass(v string) bool {
\tv = strings.ToLower(strings.TrimSpace(v))
\treturn strings.HasPrefix(v, "http://unix:") || strings.HasPrefix(v, "https://unix:")
}

func translateProxyPass(v string, rep *Report, line int) string {
\tv = strings.TrimSpace(v)''',
)

Path("internal/migrate/nginx/unix_http_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"

	"jul/internal/config"
)

func TestTranslateNamedUnixHTTPUpstream(t *testing.T) {
	cfg, rep := translate(t, `
http {
  upstream app {
    server unix:/tmp/jul-import-app.sock;
  }
  server {
    listen 8080;
    location / {
      proxy_pass http://app;
    }
  }
}`)
	if len(rep.Skipped) != 0 {
		t.Fatalf("unexpected skipped directives: %+v", rep.Skipped)
	}
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 1 {
		t.Fatalf("upstreams = %+v", cfg.Upstreams)
	}
	if got := cfg.Upstreams[0].Servers[0].Address; got != "unix:/tmp/jul-import-app.sock" {
		t.Fatalf("unix address = %q", got)
	}
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 || cfg.Servers[0].Locations[0].ProxyPass != "http://app" {
		t.Fatalf("server/location = %+v", cfg.Servers)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("imported named Unix config should validate: %v", err)
	}
}

func TestTranslateDirectUnixProxyPassIsActionableFinding(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 8080;
    location / {
      proxy_pass http://unix:/tmp/jul-direct.sock;
    }
  }
}`)
	if len(rep.Skipped) == 0 {
		t.Fatal("direct Unix proxy_pass should be reported for manual mapping")
	}
	found := false
	for _, f := range rep.Skipped {
		if f.Name == "proxy_pass" && strings.Contains(f.Reason, "named [[upstreams]]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %+v", rep.Skipped)
	}
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 0 {
		t.Fatalf("direct Unix location must not be emitted as malformed Jul config: %+v", cfg.Servers)
	}
}
''')

# More explicit transport invariants and Unix protocol parity.
p = Path("internal/handler/proxy_unix_test.go")
s = p.read_text()
if "TestUnixHTTPPoolKeyIsolation" not in s:
    s += r'''

func TestUnixHTTPPoolKeyIsolation(t *testing.T) {
	a := upstream.BackendIdentity{Scheme: "http", Network: upstream.NetworkUnix, Address: "/tmp/a.sock"}
	b := upstream.BackendIdentity{Scheme: "http", Network: upstream.NetworkUnix, Address: "/tmp/b.sock"}
	ka, kb := unixHTTPPoolKey(a), unixHTTPPoolKey(b)
	if ka == kb {
		t.Fatalf("distinct sockets share pool key %q", ka)
	}
	if ka != unixHTTPPoolKey(a) {
		t.Fatal("pool key is not stable")
	}
	if strings.Contains(ka, a.Address) || strings.Contains(kb, b.Address) {
		t.Fatalf("pool key leaks filesystem path: %q / %q", ka, kb)
	}
}

func TestProxyUnixHTTPRetryUnixToUnix(t *testing.T) {
	live := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "unix-b")
	}))
	missing := filepath.Join(t.TempDir(), "missing.sock")
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", MaxFails: 1, Servers: []config.UpstreamServer{{Address: "unix:" + missing, Weight: 1}, {Address: "unix:" + live, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "unix-b" {
		t.Fatalf("Unix->Unix retry = %d %q", rec.Code, rec.Body.String())
	}
}

func TestProxyUnixHTTPServerSentEventsStreaming(t *testing.T) {
	releaseSecond := make(chan struct{})
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		fl.Flush()
		<-releaseSecond
		_, _ = io.WriteString(w, "data: second\n\n")
		fl.Flush()
	}))
	front := httptest.NewServer(unixProxy(t, "events", path, config.LocationConfig{}))
	defer front.Close()
	resp, err := http.Get(front.URL + "/events")
	if err != nil {
		t.Fatalf("GET SSE through Unix proxy: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if got := readSSEDataWithin(t, br, 5*time.Second); got != "first" {
		t.Fatalf("first SSE event = %q", got)
	}
	close(releaseSecond)
	if got := readSSEDataWithin(t, br, 5*time.Second); got != "second" {
		t.Fatalf("second SSE event = %q", got)
	}
}

func TestProxyUnixHTTPWebSocketPassthrough(t *testing.T) {
	path := startUnixHTTPBackend(t, websocket.Handler(func(ws *websocket.Conn) {
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return
		}
		_ = websocket.Message.Send(ws, msg)
	}))
	front := httptest.NewServer(unixProxy(t, "ws", path, config.LocationConfig{}))
	defer front.Close()
	wsURL := "ws" + strings.TrimPrefix(front.URL, "http")
	ws, err := websocket.Dial(wsURL, "", front.URL)
	if err != nil {
		t.Fatalf("WebSocket dial through Unix proxy: %v", err)
	}
	defer ws.Close()
	want := []byte("unix-websocket")
	if err := websocket.Message.Send(ws, want); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := websocket.Message.Receive(ws, &got); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}
'''
    # Extend imports required by the added tests.
    s = s.replace('"context"\n\t"io"', '"bufio"\n\t"context"\n\t"io"')
    s = s.replace('"sync"\n\t"testing"', '"sync"\n\t"testing"\n\t"time"')
    s = s.replace('"jul/internal/config"\n)', '"jul/internal/config"\n\t"jul/internal/upstream"\n\n\t"golang.org/x/net/websocket"\n)')
    p.write_text(s)

# Correct a misleading comment discovered while freezing Host semantics.
p = Path("internal/handler/proxy_unix.go")
s = p.read_text()
s = s.replace(
    "// current named-upstream Host contract (incoming Host unless explicitly\n// overridden) remains unchanged.",
    "// Unix HTTP authority contract (incoming Host unless explicitly overridden)\n// is kept separate from the synthetic connection-pool identity. TCP behavior is\n// left unchanged.",
)
p.write_text(s)

# Operator documentation.
replace_once(
    "README.md",
    "| **Reverse proxy** | `proxy_pass` to a concrete URL or a named upstream; per-location connect/read/send timeouts; custom upstream headers with variable expansion |",
    "| **Reverse proxy** | `proxy_pass` to a concrete URL or a named upstream; named upstreams may use `unix:/path.sock` backends for plaintext HTTP; per-location connect/read/send timeouts; custom upstream headers with variable expansion |",
)

append_once("docs/upstreams.md", "## HTTP over Unix-domain sockets (#407)", r'''
## HTTP over Unix-domain sockets (#407)

HTTP reverse proxying may use Unix-domain-socket backends through a **named**
upstream. The socket spelling is the same canonical backend grammar used by the
rest of the upstream layer:

```toml
[[upstreams]]
name = "local-app"
servers = ["unix:/run/local-app.sock"]

[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://local-app"
```

The route stays on the normal HTTP proxy, balancing, admission, retry, circuit,
connection-accounting and generation-retirement path. A selected Unix backend
uses `network=unix` only for dialing; the filesystem path is never the HTTP
Host or a metric label. Each Unix backend has a distinct opaque connection-pool
identity, so keep-alive and `max_connections_per_backend` cannot cross sockets.
Plain HTTP pools may mix TCP and Unix members and retries may cross between them.

For Unix HTTP, the incoming Host is preserved unless `[headers].Host` explicitly
overrides it. Direct Jul spellings such as `proxy_pass = "http://unix:/run/app.sock"`
are rejected: put the socket in `[[upstreams]].servers` and reference the pool.
TLS-over-Unix is not part of this contract, so an HTTPS route or `backend_tls`
policy with a Unix member is rejected before traffic. Active `health_check.type =
"http"` is also rejected for Unix; use the existing `"tcp"` health type, which
means a connect/liveness probe and dials the backend's configured `unix` network.
Socket existence is not checked at configuration-load time.
''')

append_once("docs/health.md", "## Unix-domain-socket HTTP backends (#407)", r'''
## Unix-domain-socket HTTP backends (#407)

A Unix backend (`unix:/path.sock`) may serve a named plaintext HTTP upstream.
The active-health contract remains intentionally narrow: `type = "http"` is
rejected for a pool containing a Unix member, while `type = "tcp"` means a
connect/liveness probe and therefore performs `DialContext(..., "unix", path)`
for that member. The word `tcp` is the existing public health-check type name;
it does not force the wire network to TCP. TLS-over-Unix and `backend_tls` on a
Unix member are unsupported and rejected during configuration validation.
''')

append_once("docs/core-http.md", "## Unix-socket HTTP upstreams (#407)", r'''
## Unix-socket HTTP upstreams (#407)

A normal HTTP `proxy_pass = "http://<named-upstream>"` may select static
`unix:/path.sock` members. Unix HTTP uses the same request-body, streaming/SSE,
101 upgrade/WebSocket, timeout, retry, admission and circuit machinery as TCP.
The socket path is dial identity only: HTTP Host follows the Unix contract in
[upstreams.md](upstreams.md#http-over-unix-domain-sockets-407), and connection
reuse is isolated with an internal per-backend opaque key. The first tranche is
HTTP/1.1 over plaintext Unix sockets; it does not claim h2c, HTTP/2-over-Unix or
TLS-over-Unix support.
''')

append_once("docs/configuration.md", "## HTTP proxying to Unix sockets (#407)", r'''
## HTTP proxying to Unix sockets (#407)

Use a named upstream for HTTP over a Unix-domain socket:
`servers = ["unix:/run/app.sock"]` plus `proxy_pass = "http://<name>"`.
Direct Unix syntax in `proxy_pass` is deliberately not part of the Jul grammar.
Unix HTTP is plaintext-only, cannot use `backend_tls`, and cannot use an HTTP
active-health probe; `health_check.type = "tcp"` provides connect/liveness
probing over the backend's configured Unix network. See [upstreams.md](upstreams.md#http-over-unix-domain-sockets-407).
''')

append_once("docs/known-limitations.md", "## Unix HTTP residual boundaries (#407)", r'''
## Unix HTTP residual boundaries (#407)

HTTP-over-Unix is supported through named upstreams, but the deliberately small
first contract has three boundaries: no direct Unix spelling in `proxy_pass`, no
TLS-over-Unix/`backend_tls`, and no HTTP active-health probe over Unix (use the
connect/liveness `tcp` health type). HTTP/2-over-Unix/h2c is not advertised.
These are explicit product boundaries rather than hidden runtime failures.
''')

append_once("docs/nginx-importer.md", "## Unix HTTP upstream migration (#407)", r'''
## Unix HTTP upstream migration (#407)

A named NGINX upstream containing `server unix:/path.sock;` is preserved as a
Jul named upstream and can be referenced by `proxy_pass http://name;`. NGINX's
separate direct-Unix `proxy_pass` grammar is not copied into Jul: the importer
emits a source-located manual finding and omits that location rather than
producing malformed Jul configuration. Map it to a deterministic operator-chosen
`[[upstreams]]` name with `servers = ["unix:/path.sock"]`, then reference
`proxy_pass = "http://name"`.
''')

append_once("docs/nginx-migration-corpus.md", "### Unix HTTP upstream scenario (#407)", r'''
### Unix HTTP upstream scenario (#407)

The migration corpus now treats a named NGINX upstream containing a Unix socket
and an HTTP location referencing that upstream as a supported #407 scenario.
Direct NGINX Unix `proxy_pass` remains a blocking/manual mapping because Jul's
public contract intentionally requires the socket to live in a named upstream.
''')

append_once("docs/specs/core-gateway-completeness.md", "## Unix HTTP completeness follow-up (#407)", r'''
## Unix HTTP completeness follow-up (#407)

The residual HTTP/Unix backend-network gap is closed by #407: a normal named HTTP
upstream can select `unix:/path.sock` members without a second proxy or resilience
stack. HTTP authority and Unix dial identity are separate, connection pools are
isolated per backend, and unsupported TLS/HTTP-health/direct-target combinations
fail during configuration/import rather than at live request time.
''')

replace_once(
    "CHANGELOG.md",
    "## [Unreleased]\n",
    "## [Unreleased]\n\n- **#407 — HTTP reverse proxy over Unix-domain-socket upstreams.** Named HTTP upstreams may now contain `unix:/path.sock` backends while retaining the existing balancing, admission, retry, circuit, connection-accounting and handler-generation retirement path. Unix dial identity is carried per attempt and isolated from HTTP authority with an opaque per-backend connection-pool key; incoming Host is preserved unless explicitly overridden, raw socket paths do not become metric/trace authority labels or client error bodies, and mixed plaintext TCP/Unix pools are supported. Direct Jul Unix `proxy_pass`, TLS/`backend_tls` over Unix and HTTP active-health probes over Unix are rejected explicitly; connect/liveness health uses the backend network. The NGINX importer preserves named Unix upstreams and reports direct-Unix proxy targets for manual named-upstream mapping.\n",
)

# Dedicated issue-owned quality gate, following the repository's current full tag set.
Path(".github/workflows/issue407-quality.yml").write_text(r'''name: CGC-FOLLOWUP #407 Unix HTTP proxy quality

on:
  workflow_dispatch:
  pull_request:
    paths:
      - 'internal/handler/**'
      - 'internal/upstream/**'
      - 'internal/config/**'
      - 'internal/migrate/nginx/**'
      - 'docs/**'
      - 'README.md'
      - 'CHANGELOG.md'
      - '.github/workflows/issue407-quality.yml'
  push:
    branches: [main, issue-407-unix-http-proxy]
    paths:
      - 'internal/handler/**'
      - 'internal/upstream/**'
      - 'internal/config/**'
      - 'internal/migrate/nginx/**'
      - 'docs/**'
      - 'README.md'
      - 'CHANGELOG.md'
      - '.github/workflows/issue407-quality.yml'

permissions:
  contents: read

env:
  FULL_TAGS: "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
  COVERPKG: "./internal/handler,./internal/upstream,./internal/config,./internal/migrate/nginx"

jobs:
  unix-http-quality:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          fetch-depth: 0
          ref: ${{ github.event.pull_request.head.sha || github.sha }}
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with:
          go-version-file: go.mod
          cache: true
      - name: Resolve comparison base
        shell: bash
        run: |
          git fetch --no-tags origin main:refs/remotes/origin/main
          if [ "${{ github.event_name }}" = "push" ] && [ "${{ github.ref }}" = "refs/heads/main" ]; then
            base="${{ github.event.before }}"
            if [ -z "$base" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then base="$(git rev-parse HEAD^)"; fi
          else
            base="origin/main"
          fi
          git cat-file -e "${base}^{commit}"
          echo "BASE_SHA=$base" >> "$GITHUB_ENV"
      - name: Formatting
        shell: bash
        run: |
          changed="$(git diff --name-only "$BASE_SHA"...HEAD -- '*.go')"
          if [ -n "$changed" ]; then test -z "$(gofmt -d $changed)"; fi
      - name: Targeted lean and real Unix E2E
        run: |
          go test -count=1 ./internal/handler ./internal/upstream ./internal/config
          go test -count=1 ./internal/handler -run 'TestProxyUnixHTTP|TestUnixHTTPPoolKeyIsolation'
      - name: Importer Unix contract
        run: go test -count=1 -tags importer ./internal/migrate/nginx/... -run 'TestTranslate.*Unix|TestTranslateNamedUnixHTTPUpstream|TestTranslateDirectUnixProxyPassIsActionableFinding'
      - name: Full configured-neighbor matrix
        run: go test -count=1 -tags "${FULL_TAGS}" ./internal/handler ./internal/upstream ./internal/config ./internal/migrate/nginx/...
      - name: Race
        run: go test -race -count=1 -p 2 -tags "${FULL_TAGS}" ./internal/handler ./internal/upstream ./internal/config ./internal/migrate/nginx/...
      - name: Added Go production statements >= 90%
        shell: bash
        run: |
          go test -count=1 -covermode=atomic -coverpkg="${COVERPKG}" -coverprofile=/tmp/issue407-lean.cover ./internal/handler ./internal/upstream ./internal/config
          go test -count=1 -tags importer -covermode=atomic -coverpkg="${COVERPKG}" -coverprofile=/tmp/issue407-importer.cover ./internal/migrate/nginx/... ./internal/handler ./internal/upstream ./internal/config
          git diff --unified=0 "$BASE_SHA"...HEAD -- '*.go' > /tmp/issue407.diff
          python3 - <<'PY'
          from pathlib import Path
          import re
          roots = ('internal/handler/', 'internal/upstream/', 'internal/config/', 'internal/migrate/nginx/')
          added = {}; current = None
          for line in Path('/tmp/issue407.diff').read_text().splitlines():
              if line.startswith('+++ b/'):
                  current = line[6:]
                  if current.endswith('_test.go') or not current.startswith(roots): current = None
                  else: added.setdefault(current, set())
                  continue
              if current is None or not line.startswith('@@'): continue
              m = re.search(r'\+(\d+)(?:,(\d+))?', line)
              if not m: continue
              start = int(m.group(1)); count = int(m.group(2) or '1')
              added[current].update(range(start, start + count))
          executions = {}
          for profile in ('/tmp/issue407-lean.cover', '/tmp/issue407-importer.cover'):
              for line in Path(profile).read_text().splitlines()[1:]:
                  span, stmts, count = line.rsplit(' ', 2)
                  path, coords = span.split(':', 1)
                  if path.startswith('jul/'): path = path[4:]
                  if path not in added or not added[path]: continue
                  m = re.match(r'(\d+)\.\d+,(\d+)\.\d+', coords)
                  if not m: continue
                  lo, hi = int(m.group(1)), int(m.group(2))
                  if not any(lo <= n <= hi for n in added[path]): continue
                  key = (path, coords, int(stmts))
                  executions[key] = executions.get(key, 0) + int(count)
          total = sum(k[2] for k in executions)
          covered = sum(k[2] for k, n in executions.items() if n > 0)
          if total == 0: raise SystemExit('no #407 production statements found')
          pct = covered * 100.0 / total
          print(f'#407 added Go production statements: {covered}/{total} = {pct:.2f}%')
          if pct < 90.0:
              for (path, coords, stmts), n in sorted(executions.items()):
                  if n == 0: print(f'UNCOVERED {path}:{coords} ({stmts})')
              raise SystemExit(f'#407 added Go production coverage {pct:.2f}% < 90.0%')
          PY
''')

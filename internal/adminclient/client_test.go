// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/adminapi"
)

func TestParseEndpoint(t *testing.T) {
	t.Parallel()
	valid := []string{"https://example.com", "https://example.com/", "http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"}
	for _, raw := range valid {
		raw := raw
		t.Run("valid_"+strings.NewReplacer(":", "_", "/", "_").Replace(raw), func(t *testing.T) {
			if _, err := ParseEndpoint(raw); err != nil {
				t.Fatalf("ParseEndpoint(%q): %v", raw, err)
			}
		})
	}
	invalid := []string{"", "example.com", "ftp://example.com", "http://example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com?q=x", "https://example.com/#x"}
	for _, raw := range invalid {
		raw := raw
		t.Run("invalid_"+strings.NewReplacer(":", "_", "/", "_").Replace(raw), func(t *testing.T) {
			if _, err := ParseEndpoint(raw); err == nil {
				t.Fatalf("ParseEndpoint(%q) succeeded", raw)
			}
			if strings.Contains(strings.ToLower(errString(ParseEndpoint(raw))), "secret") {
				t.Fatal("endpoint error leaked URL password")
			}
		})
	}
}

func errString(_ *url.URL, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestIdempotencyKeyGrammar(t *testing.T) {
	t.Parallel()
	key, err := NewIdempotencyKey()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidIdempotencyKey(key) {
		t.Fatalf("generated invalid key %q", key)
	}
	for _, s := range []string{"short", strings.Repeat("a", 129), "bad key!", "åbcdefgh"} {
		if ValidIdempotencyKey(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"12345678", "abc_DEF-123"} {
		if !ValidIdempotencyKey(s) {
			t.Fatalf("rejected %q", s)
		}
	}
}

func TestErrorExitClosedSet(t *testing.T) {
	t.Parallel()
	seen := make(map[adminapi.Code]bool)
	for _, code := range adminapi.Codes() {
		exit, ok := ErrorExit(code)
		if !ok {
			t.Fatalf("stable API code %q lacks CLI mapping", code)
		}
		if exit < 1 || exit > 9 {
			t.Fatalf("code %q -> exit %d", code, exit)
		}
		seen[code] = true
	}
	if len(seen) != len(adminapi.Codes()) {
		t.Fatalf("mapped %d codes, API has %d", len(seen), len(adminapi.Codes()))
	}
	if _, ok := ErrorExit(adminapi.Code("future_code")); ok {
		t.Fatal("future code silently classified")
	}
}

func TestOutcomeExitMatrix(t *testing.T) {
	t.Parallel()
	degraded := []adminapi.Degradation{{Kind: "baseline_error"}}
	cases := []struct {
		outcome    string
		restored   bool
		restoreErr string
		degraded   []adminapi.Degradation
		want       int
		terminal   bool
	}{
		{"applied_live", false, "", nil, 0, true}, {"applied_live", false, "", degraded, 4, true},
		{"applied_degraded", false, "", nil, 4, true}, {"staged", false, "", nil, 3, true}, {"staged", false, "", degraded, 4, true},
		{"owned_not_serving", false, "", nil, 3, true}, {"owned_not_serving", false, "", degraded, 4, true},
		{"not_applied", true, "", nil, 1, true}, {"not_applied", false, "disk", nil, 5, true},
		{"saved_not_live", false, "", nil, 9, false}, {"", false, "", nil, 9, false}, {"future", false, "", nil, 9, false},
	}
	for _, tc := range cases {
		got, terminal := OutcomeExit(tc.outcome, tc.restored, tc.restoreErr, tc.degraded)
		if got != tc.want || terminal != tc.terminal {
			t.Fatalf("%q -> (%d,%t), want (%d,%t)", tc.outcome, got, terminal, tc.want, tc.terminal)
		}
	}
}

func TestResolveConnectionPrecedenceAndTokenSafety(t *testing.T) {
	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles.json")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := `{"profiles":{"prod":{"endpoint":"https://profile.example","token_file":"` + strings.ReplaceAll(token, "\\", "\\\\") + `","timeout":"7s"}}}`
	if err := os.WriteFile(profiles, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"JUL_ENDPOINT": "https://env.example", "JUL_TOKEN": "env-token"}
	cfg, warnings, err := ResolveConnection(ResolveOptions{Endpoint: "https://flag.example", ProfileName: "prod", ProfilesFile: profiles, Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	if cfg.Endpoint != "https://flag.example" || cfg.Token != "file-token" || cfg.Timeout != 7*time.Second {
		t.Fatalf("cfg=%+v", cfg)
	}

	cfg, _, err = ResolveConnection(ResolveOptions{Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://env.example" || cfg.Token != "env-token" {
		t.Fatalf("env cfg=%+v", cfg)
	}

	cfg, _, err = ResolveConnection(ResolveOptions{Endpoint: "https://stdin.example", TokenFile: "-", Stdin: strings.NewReader("stdin-token\n"), Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "stdin-token" {
		t.Fatal("stdin token not used")
	}
}

func TestResolveConnectionFailuresAndPermissions(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profiles.json")
	if _, _, err := ResolveConnection(ResolveOptions{Endpoint: "", Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	if _, _, err := ResolveConnection(ResolveOptions{ProfileName: "missing", ProfilesFile: profile, Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.WriteFile(profile, []byte(`{"profiles":{"bad":{"timeout":"nope"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveConnection(ResolveOptions{ProfileName: "bad", ProfilesFile: profile, Endpoint: "https://x.example", Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("bad duration accepted")
	}
	if err := os.WriteFile(profile, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveConnection(ResolveOptions{ProfileName: "x", ProfilesFile: profile, Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("unknown profile JSON accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(profile, []byte(`{"profiles":{"x":{"endpoint":"https://x.example"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(profile, 0o644); err != nil {
			t.Fatal(err)
		}
		_, warnings, err := ResolveConnection(ResolveOptions{ProfileName: "x", ProfilesFile: profile, Getenv: func(string) string { return "" }})
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) == 0 {
			t.Fatal("unsafe profile permissions not warned")
		}
	}
}

func TestTokenFileFailures(t *testing.T) {
	if _, _, err := readToken("-", strings.NewReader("")); err == nil {
		t.Fatal("empty stdin token accepted")
	}
	if _, _, err := readToken(filepath.Join(t.TempDir(), "missing"), strings.NewReader("")); err == nil {
		t.Fatal("missing token accepted")
	}
	f := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readToken(f, strings.NewReader("")); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestNewTLSConfigurationFailures(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(ca, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Endpoint: "https://example.com", CAFile: ca}); err == nil {
		t.Fatal("bad CA accepted")
	}
	if _, err := New(Config{Endpoint: "https://example.com", ClientCertFile: "only-cert"}); err == nil {
		t.Fatal("half mTLS accepted")
	}
	if _, err := New(Config{Endpoint: "https://example.com", ClientCertFile: ca, ClientKeyFile: ca}); err == nil {
		t.Fatal("bad client pair accepted")
	}
}

func TestHTTPSCustomCAAndBearer(t *testing.T) {
	var gotAuth string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(adminapi.StatusResponse{APIVersion: "v1", BootID: "boot"})
	}))
	defer ts.Close()
	cert := ts.Certificate()
	if cert == nil {
		t.Fatal("missing server cert")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Endpoint: ts.URL, Token: "sentinel-token", CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.BootID != "boot" {
		t.Fatalf("status=%+v", v)
	}
	if gotAuth != "Bearer sentinel-token" {
		t.Fatalf("auth=%q", gotAuth)
	}
}

func TestReadOperationsAndExactPlanBytes(t *testing.T) {
	candidate := []byte("# exact\n[global]\nworkers = 1\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/plan", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !bytes.Equal(b, candidate) {
			t.Errorf("body changed: %q", b)
		}
		if r.URL.Query().Get("base_version") != "base" {
			t.Errorf("query=%v", r.URL.Query())
		}
		if r.Header.Get("Content-Type") != "application/toml" {
			t.Errorf("content-type=%q", r.Header.Get("Content-Type"))
		}
		io.WriteString(w, `{"ok":true,"base_version":"base"}`)
	})
	mux.HandleFunc("/api/v1/config/history/7/diff", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"base_version":"b"}`) })
	mux.HandleFunc("/api/v1/config/export", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","redacted":true}`)
	})
	mux.HandleFunc("/api/v1/config/history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("cursor") != "opaque" {
			t.Errorf("query=%v", r.URL.Query())
		}
		io.WriteString(w, `{"api_version":"v1","entries":[],"limit":10}`)
	})
	mux.HandleFunc("/api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","config_schema_version":1,"endpoints":[],"exit_codes":[]}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c, err := New(Config{Endpoint: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Plan(context.Background(), candidate, "base"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HistoryDiff(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Export(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.History(context.Background(), 10, "opaque"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAPIErrorMalformedErrorRedirectAndOversize(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("unused")
		_ = mode
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{Code: adminapi.CodeUnauthenticated, Message: "no", RequestID: "req"}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c, _ := New(Config{Endpoint: ts.URL})
	_, _, err := c.Status(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Envelope.Error.Code != adminapi.CodeUnauthenticated {
		t.Fatalf("err=%T %v", err, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); io.WriteString(w, "not-json") }))
	defer bad.Close()
	c, _ = New(Config{Endpoint: bad.URL})
	_, _, err = c.Status(context.Background())
	var te *TransportError
	if !errors.As(err, &te) || te.Phase != "decode" {
		t.Fatalf("err=%T %v", err, err)
	}
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com", http.StatusFound)
	}))
	defer redir.Close()
	c, _ = New(Config{Endpoint: redir.URL})
	_, _, err = c.Status(context.Background())
	if !errors.As(err, &te) || te.Phase != "redirect" {
		t.Fatalf("redirect err=%v", err)
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer large.Close()
	c, _ = New(Config{Endpoint: large.URL})
	_, _, err = c.Status(context.Background())
	if !errors.As(err, &te) || te.Phase != "response" {
		t.Fatalf("large err=%v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestPreparedMutationValidation(t *testing.T) {
	c := &Client{}
	if _, err := c.PrepareApply([]byte("x"), "", "hot", "12345678", false); err == nil {
		t.Fatal("missing base accepted")
	}
	if _, err := c.PrepareApply([]byte("x"), "b", "wrong", "12345678", false); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := c.PrepareApply([]byte("x"), "b", "hot", "bad", false); err == nil {
		t.Fatal("bad key accepted")
	}
	p, err := c.PrepareApply([]byte("x"), "b", "stage_restart", "12345678", true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Query.Get("confirm_admin") != "true" || p.Query.Get("mode") != "stage_restart" {
		t.Fatalf("query=%v", p.Query)
	}
	if _, err := c.PrepareRollback("", "b", "12345678", false); err == nil {
		t.Fatal("missing history accepted")
	}
	if _, err := c.PrepareRollback("7", "b", "bad", false); err == nil {
		t.Fatal("bad rollback key accepted")
	}
	r, err := c.PrepareRollback("7", "b", "12345678", true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Query.Get("confirm_admin") != "true" {
		t.Fatalf("query=%v", r.Query)
	}
	if _, err := c.PrepareAdopt(AdoptRequest{}, "12345678"); err == nil {
		t.Fatal("missing adopt base accepted")
	}
	if _, err := c.PrepareAdopt(AdoptRequest{BaseVersion: "b"}, "bad"); err == nil {
		t.Fatal("bad adopt key accepted")
	}
}

func TestMutationRetryReusesExactBytesAndChecksBoot(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	prepared, _ := c.PrepareApply([]byte("same exact bytes\n"), "base", "hot", "stable-key-1", false)
	var mu sync.Mutex
	var attempts int
	var bodies [][]byte
	var queries []string
	var keys []string
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/status" {
			return jsonResp(200, `{"api_version":"v1","boot_id":"boot-a"}`), nil
		}
		mu.Lock()
		defer mu.Unlock()
		attempts++
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		queries = append(queries, r.URL.RawQuery)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if attempts == 1 {
			return nil, errors.New("connection reset")
		}
		return jsonResp(200, `{"api_version":"v1","state":"terminal","terminal":true,"ok":true,"outcome":"applied_live","degraded":[],"boot_id":"boot-a"}`), nil
	})}
	v, _, err := c.Mutate(context.Background(), prepared, "boot-a")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Terminal {
		t.Fatal("not terminal")
	}
	if attempts != 2 || !bytes.Equal(bodies[0], bodies[1]) || queries[0] != queries[1] || keys[0] != keys[1] {
		t.Fatalf("retry mutated request: attempts=%d bodies=%q query=%v keys=%v", attempts, bodies, queries, keys)
	}
}

func TestMutationDoesNotRetryAcrossBootChange(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	p, _ := c.PrepareApply([]byte("x"), "b", "hot", "stable-key-2", false)
	posts := 0
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/status" {
			return jsonResp(200, `{"api_version":"v1","boot_id":"new"}`), nil
		}
		posts++
		return nil, errors.New("drop")
	})}
	_, _, err := c.Mutate(context.Background(), p, "old")
	var boot *BootChangedError
	if !errors.As(err, &boot) {
		t.Fatalf("err=%T %v", err, err)
	}
	if posts != 1 {
		t.Fatalf("posts=%d", posts)
	}
}

func TestPollNonTerminalThenTerminalAndBootChange(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	c.pollEvery = time.Millisecond
	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return jsonResp(202, `{"api_version":"v1","apply_id":"a","state":"finalizing","terminal":false,"outcome":"saved_not_live","degraded":[],"boot_id":"boot"}`), nil
		}
		return jsonResp(200, `{"api_version":"v1","apply_id":"a","state":"terminal","terminal":true,"outcome":"staged","degraded":[],"boot_id":"boot"}`), nil
	})}
	v, _, err := c.Poll(context.Background(), "a", "boot", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Terminal || v.Outcome != "staged" || calls != 3 {
		t.Fatalf("v=%+v calls=%d", v, calls)
	}
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(202, `{"api_version":"v1","apply_id":"a","state":"pending","terminal":false,"degraded":[],"boot_id":"new"}`), nil
	})}
	_, _, err = c.Poll(context.Background(), "a", "old", time.Second)
	var boot *BootChangedError
	if !errors.As(err, &boot) {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestPollDeadlineAndCancellation(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	c.pollEvery = time.Millisecond
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(202, `{"api_version":"v1","apply_id":"a","state":"pending","terminal":false,"degraded":[],"boot_id":"boot"}`), nil
	})}
	_, _, err := c.Poll(context.Background(), "a", "boot", 3*time.Millisecond)
	var te *TransportError
	if !errors.As(err, &te) || te.Phase != "poll" {
		t.Fatalf("err=%T %v", err, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = c.Poll(ctx, "a", "boot", time.Second)
	if !errors.As(err, &te) {
		t.Fatalf("cancel err=%T %v", err, err)
	}
}

func TestAdoptPreviewWireBody(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"ok":true,"observed_digest":"d","base_version":"b"}`)
	}))
	defer ts.Close()
	c, _ := New(Config{Endpoint: ts.URL})
	_, err := c.AdoptPreview(context.Background(), AdoptRequest{BaseVersion: "b", Mode: "hot"})
	if err != nil {
		t.Fatal(err)
	}
	if got["base_version"] != "b" || got["mode"] != "hot" {
		t.Fatalf("body=%v", got)
	}
}

func TestOperationsDefensiveCopy(t *testing.T) {
	a := Operations()
	b := Operations()
	a[0].ID = "mutated"
	if reflect.DeepEqual(a, b) {
		t.Fatal("Operations returned shared slice")
	}
}

func TestX509SystemPoolSanityForCoverage(t *testing.T) {
	if _, err := x509.SystemCertPool(); err != nil {
		t.Logf("system pool unavailable in this environment: %v", err)
	}
}

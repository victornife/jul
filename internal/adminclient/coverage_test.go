// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
)

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }
func (errorReader) Close() error             { return nil }

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("stdin boom") }

func TestSmallErrorAndOperationBranches(t *testing.T) {
	if got := (*APIError)(nil).Error(); got != "" {
		t.Fatalf("nil APIError=%q", got)
	}
	if got := (&TransportError{}).Error(); got != "transport failure" {
		t.Fatalf("transport=%q", got)
	}
	if got := (*TransportError)(nil).Error(); got != "transport failure" {
		t.Fatalf("nil transport=%q", got)
	}
	root := errors.New("root")
	if !errors.Is(&TransportError{Phase: "x", Err: root}, root) {
		t.Fatal("unwrap failed")
	}
	if got := (&BootChangedError{}).Error(); !strings.Contains(got, "boot identity changed") {
		t.Fatalf("boot=%q", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown operation did not panic")
		}
	}()
	_ = operation("not-real")
}

func TestAdditionalEndpointAndNewFailures(t *testing.T) {
	if _, err := ParseEndpoint("https://%zz"); err == nil {
		t.Fatal("malformed URL accepted")
	}
	if _, err := New(Config{Endpoint: "https://example.com", CAFile: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing CA accepted")
	}
	c, err := New(Config{Endpoint: "http://localhost:1234", Timeout: -1})
	if err != nil {
		t.Fatal(err)
	}
	if c.timeout != DefaultTimeout {
		t.Fatalf("timeout=%s", c.timeout)
	}
}

func TestResponseReadAndTypedDecodeFailures(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: errorReader{}}, nil
	})}
	_, _, err := c.Status(context.Background())
	var te *TransportError
	if !errors.As(err, &te) || te.Phase != "response" {
		t.Fatalf("read err=%T %v", err, err)
	}

	for name, call := range map[string]func() error{
		"status":       func() error { _, _, e := c.Status(context.Background()); return e },
		"caps":         func() error { _, _, e := c.Capabilities(context.Background()); return e },
		"history":      func() error { _, _, e := c.History(context.Background(), 0, ""); return e },
		"apply-result": func() error { _, _, e := c.ApplyResult(context.Background(), "a"); return e },
	} {
		t.Run(name, func(t *testing.T) {
			c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResp(200, "not-json"), nil })}
			if err := call(); err == nil {
				t.Fatal("malformed success decoded")
			}
		})
	}
}

func TestOptionalQueryAndGeneratedMutationKeys(t *testing.T) {
	var rawQueries []string
	ts := http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rawQueries = append(rawQueries, r.URL.RawQuery)
		return jsonResp(200, `{"api_version":"v1","entries":[],"limit":50}`), nil
	})}
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	c.http = &ts
	if _, _, err := c.History(context.Background(), 0, ""); err != nil {
		t.Fatal(err)
	}
	if len(rawQueries) != 1 || rawQueries[0] != "" {
		t.Fatalf("query=%v", rawQueries)
	}
	if p, err := c.PrepareApply([]byte("x"), "b", "hot", "", false); err != nil || !ValidIdempotencyKey(p.IdempotencyKey) {
		t.Fatalf("apply key=%q err=%v", p.IdempotencyKey, err)
	}
	if p, err := c.PrepareRollback("h", "b", "", false); err != nil || !ValidIdempotencyKey(p.IdempotencyKey) {
		t.Fatalf("rollback key=%q err=%v", p.IdempotencyKey, err)
	}
	if p, err := c.PrepareAdopt(AdoptRequest{BaseVersion: "b"}, ""); err != nil || !ValidIdempotencyKey(p.IdempotencyKey) {
		t.Fatalf("adopt key=%q err=%v", p.IdempotencyKey, err)
	}
	v := url.Values{"x": {"a", "b"}}
	copy := cloneValues(v)
	copy["x"][0] = "changed"
	if v["x"][0] != "a" {
		t.Fatal("clone shares storage")
	}
}

func TestMutateNonTransportAndStatusFailure(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	p, _ := c.PrepareApply([]byte("x"), "b", "hot", "stable-key-z", false)
	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path == "/api/v1/config/apply" {
			return jsonResp(409, `{"error":{"code":"stale_base_version","message":"stale","request_id":"r"}}`), nil
		}
		return jsonResp(500, "bad"), nil
	})}
	_, _, err := c.Mutate(context.Background(), p, "boot")
	var ae *APIError
	if !errors.As(err, &ae) || calls != 1 {
		t.Fatalf("err=%T calls=%d", err, calls)
	}

	calls = 0
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("down") })}
	_, _, err = c.Mutate(context.Background(), p, "boot")
	var te *TransportError
	if !errors.As(err, &te) || calls != 2 {
		t.Fatalf("status failure err=%T calls=%d", err, calls)
	}
}

func TestPollDefaultTimeoutAndImmediateErrors(t *testing.T) {
	c, _ := New(Config{Endpoint: "http://127.0.0.1:1"})
	c.pollTimeout = 10 * time.Millisecond
	c.pollEvery = time.Millisecond
	c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(200, `{"api_version":"v1","apply_id":"a","terminal":true,"outcome":"applied_live","degraded":[],"boot_id":"boot"}`), nil
	})}
	v, _, err := c.Poll(context.Background(), "a", "boot", 0)
	if err != nil || !v.Terminal {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(404, `{"error":{"code":"not_found","message":"gone","request_id":"r"}}`), nil
	})}
	if _, _, err := c.Poll(context.Background(), "a", "boot", time.Second); err == nil {
		t.Fatal("poll API error ignored")
	}
}

func TestProfileDefaultsAndRemainingErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p, err := DefaultProfilesFile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(p), "/jul/profiles.json") {
		t.Fatalf("path=%q", p)
	}
	if got := firstNonEmpty(" ", " x ", "y"); got != "x" {
		t.Fatalf("first=%q", got)
	}
	profile := filepath.Join(dir, "p.json")
	if err := os.WriteFile(profile, []byte(`{"profiles":{"one":{"endpoint":"https://example.com"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProfile(profile, "missing"); err == nil {
		t.Fatal("missing profile accepted")
	}
	if _, _, err := readToken("-", failReader{}); err == nil {
		t.Fatal("stdin read failure ignored")
	}
	if got := permissionWarnings("x", nil); got != nil {
		t.Fatalf("nil info warning=%v", got)
	}

	// Exercise default getenv/stdin path without relying on inherited secrets.
	t.Setenv("JUL_ENDPOINT", "https://env-default.example")
	t.Setenv("JUL_TOKEN", "")
	cfg, _, err := ResolveConnection(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://env-default.example" {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestAPIErrorErrorText(t *testing.T) {
	e := &APIError{Envelope: adminapi.Envelope{Error: adminapi.Body{Code: adminapi.CodeForbidden, Message: "denied"}}}
	if e.Error() != "forbidden: denied" {
		t.Fatalf("err=%q", e.Error())
	}
}

//go:build acme

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/acme"

	"jul/internal/config"
)

func TestNewACMEManagerNilWhenNoACME(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{Listen: ":80"}}}
	mgr, err := NewACMEManager(cfg.Servers, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		t.Error("expected nil manager when no block enables acme")
	}
}

func TestNewACMEManagerBuildsManager(t *testing.T) {
	mgr, err := NewACMEManager(acmeServerCfg().Servers, nil)
	if err != nil {
		t.Fatalf("NewACMEManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected a manager when a block enables acme")
	}
	if !ACMECompiled {
		t.Error("ACMECompiled must be true with the acme tag")
	}
}

func TestDirectoryURL(t *testing.T) {
	cases := map[string]string{
		"letsencrypt":         acme.LetsEncryptURL,
		"letsencrypt-staging": letsEncryptStagingURL,
		"":                    letsEncryptStagingURL,
		"https://ca.test/dir": "https://ca.test/dir",
	}
	for in, want := range cases {
		if got := directoryURL(in); got != want {
			t.Errorf("directoryURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestACMEChallengeHandlerRouting(t *testing.T) {
	mgr, err := NewACMEManager(acmeServerCfg().Servers, nil)
	if err != nil {
		t.Fatal(err)
	}
	fallbackHit := false
	h := mgr.ChallengeHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fallbackHit = true
	}))

	// A challenge path is handled by autocert itself (unknown token -> 404),
	// never reaching the fallback.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/missing", nil))
	if fallbackHit {
		t.Error("challenge request must not reach the fallback")
	}

	// A normal request is delegated to the fallback.
	fallbackHit = false
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !fallbackHit {
		t.Error("non-challenge request must reach the fallback")
	}
}

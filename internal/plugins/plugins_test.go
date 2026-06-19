//go:build wasmplugins

package plugins

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

const testdataDir = "../../testdata/plugins/"

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func buildSet(t *testing.T, m *Manager, cfg map[string]config.PluginConfig) *Set {
	t.Helper()
	s, err := m.Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func pcfg(name string, opts ...func(*config.PluginConfig)) config.PluginConfig {
	pc := config.PluginConfig{
		Path:        testdataDir + name + ".wasm",
		Type:        "middleware",
		MemoryLimit: config.Size(32 << 20),
		Timeout:     config.Duration(2 * time.Second),
	}
	for _, o := range opts {
		o(&pc)
	}
	return pc
}

func okNext() (http.Handler, *bool) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "next")
	})
	return h, &called
}

func TestMiddlewareInjectsResponseHeader(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"hi": pcfg("header-inject")})

	next, called := okNext()
	h := s.Middleware("hi")(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !*called {
		t.Fatal("next handler was not called (plugin should Continue)")
	}
	if got := rec.Header().Get("X-Plugin"); got != "header-inject" {
		t.Fatalf("X-Plugin = %q, want header-inject", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestMiddlewareBlocksRequest(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"rb": pcfg("request-block")})

	next, called := okNext()
	h := s.Middleware("rb")(next)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Block", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if *called {
		t.Fatal("next handler was called but the plugin should have blocked the request")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "blocked") {
		t.Fatalf("body = %q, want it to contain \"blocked\"", rec.Body.String())
	}
}

func TestMiddlewarePassesThrough(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"rb": pcfg("request-block")})

	next, called := okNext()
	h := s.Middleware("rb")(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil)) // no X-Block

	if !*called {
		t.Fatal("next handler was not called for a non-blocked request")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandlerPluginBlocks(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"rb": pcfg("request-block", func(pc *config.PluginConfig) { pc.Type = "handler" }),
	})

	h := s.Handler("rb")
	if h == nil {
		t.Fatal("Handler returned nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Block", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPanicIsContained(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"p": pcfg("testguest-panic")})

	next, called := okNext()
	h := s.Middleware("p")(next)

	rec := httptest.NewRecorder()
	// Must not panic the host; should yield a 500 and leave next uncalled.
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if *called {
		t.Fatal("next handler was called despite the guest panicking")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestTimeoutIsContained(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"loop": pcfg("testguest-loop", func(pc *config.PluginConfig) {
			pc.Timeout = config.Duration(150 * time.Millisecond)
		}),
	})

	next, called := okNext()
	h := s.Middleware("loop")(next)

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		done <- rec.Code
	}()

	select {
	case code := <-done:
		if *called {
			t.Fatal("next handler was called despite the guest timing out")
		}
		if code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runaway guest was not interrupted within 5s")
	}
}

func TestKVCounterWithCapability(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"kv": pcfg("kv-counter", func(pc *config.PluginConfig) { pc.KV = true }),
	})

	for want := 1; want <= 3; want++ {
		next, _ := okNext()
		h := s.Middleware("kv")(next)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("X-Count"); got != itoa(want) {
			t.Fatalf("request %d: X-Count = %q, want %d", want, got, want)
		}
	}
}

func TestKVDeniedWithoutCapability(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"kv": pcfg("kv-counter"), // KV capability NOT granted
	})

	// Without the capability, every KVGet is denied, so the guest always starts
	// from zero and reports 1.
	for i := 0; i < 3; i++ {
		next, _ := okNext()
		h := s.Middleware("kv")(next)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("X-Count"); got != "1" {
			t.Fatalf("X-Count = %q, want 1 (KV denied, no persistence)", got)
		}
	}
}

func TestReloadReusesManager(t *testing.T) {
	m := testManager(t)
	cfg := map[string]config.PluginConfig{"hi": pcfg("header-inject")}

	// First generation.
	s1, err := m.Build(cfg)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	// Second generation on the same manager (shared compilation cache).
	s2, err := m.Build(cfg)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	// Close the first generation; the second must keep working.
	_ = s1.Close()
	t.Cleanup(func() { _ = s2.Close() })

	next, called := okNext()
	rec := httptest.NewRecorder()
	s2.Middleware("hi")(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !*called || rec.Header().Get("X-Plugin") != "header-inject" {
		t.Fatal("second generation did not function after first was closed")
	}
}

func TestBuildRejectsMissingModule(t *testing.T) {
	m := testManager(t)
	_, err := m.Build(map[string]config.PluginConfig{
		"bad": {Path: testdataDir + "does-not-exist.wasm", Type: "middleware"},
	})
	if err == nil {
		t.Fatal("expected an error building a plugin with a missing module file")
	}
}

// itoa is a tiny strconv.Itoa to avoid importing strconv just for the test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

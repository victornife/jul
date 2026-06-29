package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestActionOfDeny(t *testing.T) {
	loc := config.LocationConfig{Deny: true}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionDeny {
		t.Fatalf("action = %q, want deny", action)
	}
}

func TestActionOfRedirect(t *testing.T) {
	loc := config.LocationConfig{Redirect: "/new"}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionRedirect {
		t.Fatalf("action = %q, want redirect", action)
	}
}

func TestActionOfReturn(t *testing.T) {
	loc := config.LocationConfig{Return: 418}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionReturn {
		t.Fatalf("action = %q, want return", action)
	}
}

func TestActionOfProxy(t *testing.T) {
	loc := config.LocationConfig{ProxyPass: "http://backend"}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionProxy {
		t.Fatalf("action = %q, want proxy", action)
	}
}

func TestActionOfFastCGI(t *testing.T) {
	loc := config.LocationConfig{FastCGIPass: "unix:/var/run/php.sock"}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionFastCGI {
		t.Fatalf("action = %q, want fastcgi", action)
	}
}

func TestActionOfUWSGI(t *testing.T) {
	loc := config.LocationConfig{UWSGIPass: "unix:/var/run/uwsgi.sock"}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionFastCGI {
		t.Fatalf("action = %q, want fastcgi (uwsgi maps to fastcgi action)", action)
	}
}

func TestActionOfStatic(t *testing.T) {
	loc := config.LocationConfig{Root: "/var/www"}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionStatic {
		t.Fatalf("action = %q, want static", action)
	}
}

func TestActionOfPlugin(t *testing.T) {
	loc := config.LocationConfig{Plugin: "myplugin"}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionPlugin {
		t.Fatalf("action = %q, want plugin", action)
	}
}

func TestActionOfGRPCTranscode(t *testing.T) {
	loc := config.LocationConfig{GRPCTranscode: &config.GRPCTranscodeConfig{}}
	action, err := actionOf(loc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != ActionGRPCTranscode {
		t.Fatalf("action = %q, want grpc_transcode", action)
	}
}

func TestActionOfNoAction(t *testing.T) {
	loc := config.LocationConfig{
		Match: config.MatchConfig{Path: "/nothing"},
	}
	_, err := actionOf(loc)
	if err == nil {
		t.Fatal("expected error for location with no action")
	}
}

func TestDenyHandler(t *testing.T) {
	h := denyHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRedirectHandlerRedirect(t *testing.T) {
	loc := config.LocationConfig{Redirect: "/new", Return: 0}
	h := redirectHandler(loc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/old", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if locHdr := rec.Header().Get("Location"); locHdr != "/new" {
		t.Fatalf("location = %q, want /new", locHdr)
	}
}

func TestRedirectHandlerReturnOnly(t *testing.T) {
	loc := config.LocationConfig{Return: 204, Redirect: ""}
	h := redirectHandler(loc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestRedirectHandlerDefaultCode(t *testing.T) {
	loc := config.LocationConfig{Redirect: "/new", Return: 0}
	h := redirectHandler(loc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
}

func TestRedirectHandlerZeroMeans200(t *testing.T) {
	loc := config.LocationConfig{Return: 0, Redirect: ""}
	h := redirectHandler(loc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestBuiltinBuilders(t *testing.T) {
	builders := builtinBuilders()
	if len(builders) != 3 {
		t.Fatalf("expected 3 builtin builders, got %d", len(builders))
	}
	for _, name := range []string{ActionDeny, ActionRedirect, ActionReturn} {
		if _, ok := builders[name]; !ok {
			t.Fatalf("missing builtin builder for %s", name)
		}
	}
}

func TestRedirectToHTTPS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/path?query=1", nil)
	req.Host = "example.com:8080"
	rec := httptest.NewRecorder()

	redirectToHTTPS(rec, req, http.StatusMovedPermanently)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/path?query=1" {
		t.Fatalf("location = %q, want https://example.com/path?query=1", loc)
	}
}

func TestRedirectToHTTPSCoercesInvalidCode(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()

	redirectToHTTPS(rec, req, http.StatusFound) // not 301 or 308

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301 (coerced)", rec.Code)
	}
}

func TestRedirectToHTTPSIPv6Host(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "[::1]:8080"
	rec := httptest.NewRecorder()

	redirectToHTTPS(rec, req, http.StatusPermanentRedirect)

	if loc := rec.Header().Get("Location"); loc != "https://::1/" {
		t.Fatalf("location = %q, want https://::1/", loc)
	}
}

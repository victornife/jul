//go:build waf

package waf

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
)

// newOKHandler is the protected action: it returns 200 and a fixed body so a
// test can tell a request that reached the action from one the WAF blocked.
func newOKHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "reached-action")
	})
}

// eventRecorder captures WAF events for assertions.
type eventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (e *eventRecorder) onEvent(action, rule string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, action+":"+rule)
}

func (e *eventRecorder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

// buildAndServe builds a firewall from cfg (applying defaults as the loader
// would), wraps an OK handler, and serves req against it, returning the recorder.
func buildAndServe(t *testing.T, cfg config.WAFConfig, req *http.Request) (*httptest.ResponseRecorder, *eventRecorder) {
	t.Helper()
	applyTestDefaults(&cfg)
	rec := &eventRecorder{}
	fw, err := New(cfg, Options{Hooks: Hooks{OnEvent: rec.onEvent}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := fw.Middleware()(newOKHandler())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, rec
}

// applyTestDefaults mirrors the loader defaults so tests construct WAFConfig
// values directly without going through the TOML parser.
func applyTestDefaults(c *config.WAFConfig) {
	if c.Mode == "" {
		c.Mode = "block"
	}
	if c.BlockStatus == 0 {
		c.BlockStatus = 403
	}
	if c.RequestBodyLimit == 0 {
		c.RequestBodyLimit = config.Size(128 << 10)
	}
}

func TestFirewallBlocksInlineRule(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		InlineRules: `SecRule REQUEST_URI "@contains /forbidden" "id:100,phase:1,deny,status:403,log,msg:'blocked path'"`,
	}
	req := httptest.NewRequest(http.MethodGet, "/forbidden/page", nil)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "reached-action") {
		t.Error("request reached the action but should have been blocked")
	}
	if rec.count() == 0 {
		t.Error("expected at least one WAF event to be recorded")
	}
}

func TestFirewallAllowsCleanRequest(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		InlineRules: `SecRule REQUEST_URI "@contains /forbidden" "id:100,phase:1,deny,status:403"`,
	}
	req := httptest.NewRequest(http.MethodGet, "/allowed/page", nil)
	rr, _ := buildAndServe(t, cfg, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "reached-action") {
		t.Error("clean request did not reach the action")
	}
}

func TestFirewallDetectModeRecordsButAllows(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "detect",
		InlineRules: `SecRule REQUEST_URI "@contains /forbidden" "id:100,phase:1,deny,status:403,log,msg:'would block'"`,
	}
	req := httptest.NewRequest(http.MethodGet, "/forbidden/page", nil)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (detect mode must not block)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "reached-action") {
		t.Error("detect-mode request did not reach the action")
	}
	if rec.count() == 0 {
		t.Error("expected the matched rule to be recorded in detect mode")
	}
}

func TestFirewallRequestBodyLimitRejectsOversizedBody(t *testing.T) {
	// A rule inspects the request body; the body limit caps how much is read.
	cfg := config.WAFConfig{
		Enabled:          true,
		Mode:             "block",
		RequestBodyLimit: config.Size(16),
		InlineRules: strings.Join([]string{
			`SecRequestBodyAccess On`,
			`SecRequestBodyLimitAction ProcessPartial`,
			`SecRule REQUEST_BODY "@contains attack" "id:101,phase:2,deny,status:403,msg:'body match'"`,
		}, "\n"),
	}
	// The marker sits beyond the 16-byte limit, so a ProcessPartial body is not
	// fully inspected — the request is allowed. This asserts the limit is wired.
	body := strings.NewReader(strings.Repeat("A", 32) + "attack")
	req := httptest.NewRequest(http.MethodPost, "/submit", body)
	req.Header.Set("Content-Type", "text/plain")
	rr, _ := buildAndServe(t, cfg, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (marker beyond body limit must not match)", rr.Code)
	}
}

func TestFirewallCRSBlocksSQLi(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:    true,
		Mode:       "block",
		CRSEnabled: true,
	}
	// A classic SQL-injection probe in a query parameter. CRS anomaly scoring
	// crosses the inbound threshold and rule 949110 blocks the request.
	req := httptest.NewRequest(http.MethodGet, "/?id=1%27%20OR%20%271%27%3D%271", nil)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code == http.StatusOK {
		t.Errorf("status = %d, want a block (SQLi should be rejected by CRS)", rr.Code)
	}
	if rec.count() == 0 {
		t.Error("expected CRS rule events to be recorded for the SQLi probe")
	}
}

func TestFirewallCRSAllowsCleanRequest(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:    true,
		Mode:       "block",
		CRSEnabled: true,
	}
	req := httptest.NewRequest(http.MethodGet, "/products?page=2&sort=name", nil)
	rr, _ := buildAndServe(t, cfg, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (a clean request must pass CRS)", rr.Code)
	}
}

func TestNewRejectsBadDirectives(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		InlineRules: `this is not valid seclang @@@`,
	}
	applyTestDefaults(&cfg)
	if _, err := New(cfg, Options{}); err == nil {
		t.Error("expected an error compiling invalid SecLang directives")
	}
}

func TestFirewallBlockStatusCustom(t *testing.T) {
	// A deny rule without an explicit status: SecDefaultAction (which now
	// comes before user rules) supplies the status, so block_status=451 is
	// applied instead of Coraza's hardcoded 403 fallback.
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 451,
		InlineRules: `SecRule REQUEST_URI "@contains /forbidden" "id:100,phase:1,deny,log,msg:'blocked path'"`,
	}
	req := httptest.NewRequest(http.MethodGet, "/forbidden/page", nil)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code != 451 {
		t.Errorf("status = %d, want 451", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "reached-action") {
		t.Error("request reached the action but should have been blocked")
	}
	if rec.count() == 0 {
		t.Error("expected at least one WAF event to be recorded")
	}
}

func TestFirewallDoesNotRewriteDownstream403(t *testing.T) {
	// Regression: a clean request that reaches the action and gets a genuine
	// 403 from the backend must NOT be rewritten to the WAF block_status.
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 451,
		InlineRules: `SecRule REQUEST_URI "@contains /forbidden" "id:100,phase:1,deny,status:403,log,msg:'blocked path'"`,
	}
	applyTestDefaults(&cfg)
	fw, err := New(cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The action returns 403 — this is a legitimate backend response.
	action := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "backend says no", http.StatusForbidden)
	})
	h := fw.Middleware()(action)

	req := httptest.NewRequest(http.MethodGet, "/allowed/page", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("downstream 403 rewritten to %d; must remain 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "backend says no") {
		t.Error("clean request should reach the action")
	}
}

// indexOrAbsent reports the byte offset of sub in s, or -1 when absent.
func indexOrAbsent(s, sub string) int { return strings.Index(s, sub) }

// TestBuildDirectivesOrder is a golden test pinning the deterministic assembly
// order of the SecLang program: SecDefaultAction → CRS → directives_files →
// inline_rules → enforcement-mode line last. Order matters because later
// directives override earlier engine state, so a regression that reordered
// these would silently change blocking behaviour.
func TestBuildDirectivesOrder(t *testing.T) {
	t.Run("crs disabled: default-action first, mode last", func(t *testing.T) {
		cfg := config.WAFConfig{
			Mode:            "detect",
			BlockStatus:     451,
			DirectivesFiles: []string{"/etc/jul/extra.conf"},
			InlineRules:     `SecRule REQUEST_URI "@contains /x" "id:100,phase:1,deny"`,
		}
		out, err := buildDirectives(cfg)
		if err != nil {
			t.Fatalf("buildDirectives: %v", err)
		}

		defAction := indexOrAbsent(out, "SecDefaultAction \"phase:1,deny,status:451,log\"")
		dirFile := indexOrAbsent(out, "Include /etc/jul/extra.conf")
		inline := indexOrAbsent(out, `SecRule REQUEST_URI "@contains /x"`)
		mode := indexOrAbsent(out, "SecRuleEngine DetectionOnly")

		for name, idx := range map[string]int{
			"SecDefaultAction": defAction, "directives_file": dirFile,
			"inline_rules": inline, "mode": mode,
		} {
			if idx < 0 {
				t.Fatalf("expected %s in output, got:\n%s", name, out)
			}
		}
		if defAction >= dirFile || dirFile >= inline || inline >= mode {
			t.Errorf("order violated (defAction=%d dirFile=%d inline=%d mode=%d):\n%s",
				defAction, dirFile, inline, mode, out)
		}
		// CRS off ⇒ our SecDefaultAction is emitted; CRS includes are not.
		if strings.Contains(out, "@owasp_crs") {
			t.Error("CRS includes must be absent when crs_enabled is false")
		}
	})

	t.Run("crs enabled: no self default-action, CRS before user rules", func(t *testing.T) {
		cfg := config.WAFConfig{
			Mode:            "block",
			CRSEnabled:      true,
			DirectivesFiles: []string{"/etc/jul/extra.conf"},
			InlineRules:     `SecRule REQUEST_URI "@contains /x" "id:100,phase:1,deny"`,
		}
		out, err := buildDirectives(cfg)
		if err != nil {
			t.Fatalf("buildDirectives: %v", err)
		}

		crs := indexOrAbsent(out, "Include @owasp_crs/*.conf")
		dirFile := indexOrAbsent(out, "Include /etc/jul/extra.conf")
		inline := indexOrAbsent(out, `SecRule REQUEST_URI "@contains /x"`)
		mode := indexOrAbsent(out, "SecRuleEngine On")

		for name, idx := range map[string]int{
			"CRS include": crs, "directives_file": dirFile,
			"inline_rules": inline, "mode": mode,
		} {
			if idx < 0 {
				t.Fatalf("expected %s in output, got:\n%s", name, out)
			}
		}
		if crs >= dirFile || dirFile >= inline || inline >= mode {
			t.Errorf("order violated (crs=%d dirFile=%d inline=%d mode=%d):\n%s",
				crs, dirFile, inline, mode, out)
		}
		// CRS on ⇒ we must NOT emit our own SecDefaultAction (Coraza rejects dups).
		if strings.Contains(out, "SecDefaultAction") {
			t.Error("must not emit SecDefaultAction when CRS is enabled")
		}
	})
}

func TestFirewallClose(t *testing.T) {
	fw, err := New(config.WAFConfig{Enabled: true, Mode: "detect"}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestFirewallCRSBlocksPathTraversal(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:    true,
		Mode:       "block",
		CRSEnabled: true,
	}
	// A local-file-inclusion probe with path traversal. CRS rule 930100
	// (Path Traversal Attack) should match and push anomaly score above
	// threshold, causing inbound blocking.
	req := httptest.NewRequest(http.MethodGet, "/?file=../../../etc/passwd", nil)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code == http.StatusOK {
		t.Errorf("status = %d, want a block (path traversal should be rejected by CRS)", rr.Code)
	}
	if rec.count() == 0 {
		t.Error("expected CRS rule events to be recorded for the LFI probe")
	}
}

func TestFirewallCRSBlocksXSS(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:    true,
		Mode:       "block",
		CRSEnabled: true,
	}
	// A reflected XSS payload in the User-Agent header. CRS rule 941100
	// (XSS Attack Detected) or 941101 should match and push anomaly score
	// above the threshold, causing rule 949110 to block.
	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	req.Header.Set("User-Agent", `<script>alert('XSS')</script>`)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code == http.StatusOK {
		t.Errorf("status = %d, want a block (XSS should be rejected by CRS)", rr.Code)
	}
	if rec.count() == 0 {
		t.Error("expected CRS rule events to be recorded for the XSS probe")
	}
}

func TestFirewallCRSDetectModeAllows(t *testing.T) {
	cfg := config.WAFConfig{
		Enabled:    true,
		Mode:       "detect",
		CRSEnabled: true,
	}
	// A SQLi probe that CRS would block in enforcement mode.
	req := httptest.NewRequest(http.MethodGet, "/?id=1%27%20OR%20%271%27%3D%271", nil)
	rr, rec := buildAndServe(t, cfg, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (detect mode must allow the request)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "reached-action") {
		t.Error("detect-mode request should still reach the action")
	}
	if rec.count() == 0 {
		t.Error("expected CRS rule events to be recorded in detect mode")
	}
}

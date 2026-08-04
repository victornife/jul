// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corazawaf/coraza/v3"
	corazahttp "github.com/corazawaf/coraza/v3/http"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
	"jul/internal/redact"
)

const privacyRule = `SecRule ARGS:token "@streq s3cr3t" "id:910001,phase:1,deny,status:403,log,msg:'token=%{ARGS.token}'"`

// TestCorazaMatchedRuleContainsFullRequestTargetAndExpandedMessage pins the
// behavior of the exact Coraza dependency linked by Jul.IA. It is the evidence
// for sanitizing at the integration boundary: URI contains the raw query and
// Message contains macro-expanded request data.
func TestCorazaMatchedRuleContainsFullRequestTargetAndExpandedMessage(t *testing.T) {
	var match types.MatchedRule
	w, err := coraza.NewWAF(coraza.NewWAFConfig().
		WithDirectives("SecRuleEngine On\n" + privacyRule + "\n").
		WithErrorCallback(func(mr types.MatchedRule) { match = mr }))
	if err != nil {
		t.Fatalf("coraza.NewWAF: %v", err)
	}

	h := corazahttp.WrapHandler(w, newOKHandler())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/login?token=s3cr3t&api_key=also-secret", nil))

	if match == nil {
		t.Fatal("expected Coraza error callback to receive a matched rule")
	}
	if got := match.URI(); !strings.Contains(got, "?token=s3cr3t") {
		t.Fatalf("MatchedRule.URI() = %q; expected the full request target including query", got)
	}
	if got := match.Message(); !strings.Contains(got, "s3cr3t") {
		t.Fatalf("MatchedRule.Message() = %q; expected macro-expanded request data", got)
	}
}

func TestWAFStructuredLogIsPathOnlyBoundedAndSecretSafe(t *testing.T) {
	previous := redact.Global()
	redact.Install(redact.NewState([]string{"tenant-secret"}, 4))
	t.Cleanup(func() { redact.Install(previous) })

	for _, mode := range []string{"block", "detect"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&out, nil))
			cfg := config.WAFConfig{
				Enabled:     true,
				Mode:        mode,
				InlineRules: privacyRule,
			}
			applyTestDefaults(&cfg)
			fw, err := New(context.Background(), cfg, Options{Logger: logger})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			h := fw.Middleware()(newOKHandler())
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
				"/accounts/tenant-secret?token=s3cr3t&api_key=also-secret&code=oauth-code", nil))

			if mode == "block" && rr.Code != http.StatusForbidden {
				t.Fatalf("block status = %d, want 403", rr.Code)
			}
			if mode == "detect" && rr.Code != http.StatusOK {
				t.Fatalf("detect status = %d, want 200", rr.Code)
			}

			logLine := out.String()
			for _, forbidden := range []string{
				"s3cr3t", "also-secret", "oauth-code", "tenant-secret",
				`"uri":`, `"message":`, "?token=", "api_key=", "code=",
			} {
				if strings.Contains(logLine, forbidden) {
					t.Errorf("structured WAF log contains forbidden value %q: %s", forbidden, logLine)
				}
			}
			for _, required := range []string{
				`"msg":"waf rule matched"`,
				`"rule_id":"910001"`,
				`"path":"/accounts/***"`,
				`"query_omitted":true`,
				`"phase":1`,
				`"mode":"` + mode + `"`,
			} {
				if !strings.Contains(logLine, required) {
					t.Errorf("structured WAF log missing %q: %s", required, logLine)
				}
			}
		})
	}
}

func TestWAFStructuredLogBoundsAttackerControlledPath(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "detect",
		InlineRules: privacyRule,
	}
	applyTestDefaults(&cfg)
	fw, err := New(context.Background(), cfg, Options{Logger: logger})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	longSegment := strings.Repeat("a", 4096)
	h := fw.Middleware()(newOKHandler())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/"+longSegment+"?token=s3cr3t", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	logLine := out.String()
	if strings.Contains(logLine, longSegment) {
		t.Fatalf("structured WAF log retained unbounded path: %d bytes", len(logLine))
	}
	if strings.Contains(logLine, "s3cr3t") {
		t.Fatalf("structured WAF log retained query secret: %s", logLine)
	}
	if len(logLine) > 1024 {
		t.Fatalf("structured WAF log = %d bytes, expected a bounded event", len(logLine))
	}
}

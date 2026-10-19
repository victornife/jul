// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
	"jul/internal/waf"
)

func TestAudit653CompiledWAFIncludeChangeMustRetire(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.conf")
	main := filepath.Join(dir, "main.conf")
	// Relative to main.conf: Coraza's SecLang Include parser treats a Windows
	// drive-letter path as relative and joins it onto the including file's directory.
	_ = os.WriteFile(main, []byte("Include child.conf\n"), 0o600)
	_ = os.WriteFile(child, []byte(`SecRule REQUEST_METHOD "@streq GET" "id:1001,phase:1,pass,nolog"`), 0o600)
	cfg := audit653Config()
	cfg.WAF = config.WAFConfig{Enabled: true, Mode: "block", DirectivesFiles: []string{main}}
	b, err := waf.New(context.Background(), cfg.WAF, waf.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	beforeFiles := captureLoadedAccessFiles(cfg)
	_ = os.WriteFile(child, []byte(`SecRule REQUEST_METHOD "@streq GET" "id:1001,phase:1,deny,status:403,nolog"`), 0o600)
	a, err := waf.New(context.Background(), cfg.WAF, waf.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	code := func(f *waf.Firewall) int {
		rr := httptest.NewRecorder()
		f.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rr, httptest.NewRequest("GET", "http://example.com/blocked", nil))
		return rr.Code
	}
	if code(b) != 200 || code(a) != 403 || !a.SourceChanged(b) {
		t.Fatalf("fixture did not change compiled admission: before=%d after=%d changed=%t", code(b), code(a), a.SourceChanged(b))
	}
	trs := ComputePublishEntryTransitions(cfg, cfg, beforeFiles, cfg, cfg, captureLoadedAccessFiles(cfg), nil, nil)
	if len(trs) != 1 || !trs[0].Retire {
		t.Fatalf("compiled WAF changed 200->403, loaded source identity changed, but retirement skipped: %+v", trs)
	}
}

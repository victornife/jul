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

func TestReaudit653WAFLoadedDataFilePathWithSpacesMustRetire(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "blocked methods.txt")
	main := filepath.Join(dir, "main.conf")
	if err := os.WriteFile(main, []byte(`SecRule REMOTE_ADDR "@ipMatchFromFile blocked methods.txt" "id:1001,phase:1,deny,status:403,nolog"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("198.51.100.0/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := audit653Config()
	cfg.WAF = config.WAFConfig{Enabled: true, Mode: "block", DirectivesFiles: []string{main}}
	before, err := waf.New(context.Background(), cfg.WAF, waf.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	beforeFiles := captureLoadedAccessFiles(cfg)
	if err := os.WriteFile(child, []byte("192.0.2.0/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := waf.New(context.Background(), cfg.WAF, waf.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	code := func(f *waf.Firewall) int {
		rec := httptest.NewRecorder()
		f.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rec, httptest.NewRequest("GET", "http://example.com/", nil))
		return rec.Code
	}
	if code(before) != 200 || code(after) != 403 || !before.SourceChanged(after) {
		t.Fatalf("invalid fixture before=%d after=%d changed=%t", code(before), code(after), before.SourceChanged(after))
	}
	// 1. Direct captureLoadedAccessFiles comparison detects the data-file change with space in path.
	transitions := ComputePublishEntryTransitions(cfg, cfg, beforeFiles, cfg, cfg, captureLoadedAccessFiles(cfg), nil, nil)
	if len(transitions) != 1 || !transitions[0].Retire {
		t.Fatalf("valid @ipMatchFromFile with space in its filename changed loaded policy but retirement skipped: %+v", transitions)
	}
	// 2. Even if the file on disk is reverted between Prepare (waf.New) and Publish,
	//    carrying the prepared engine's SourceDigest via WithCompiledWAF / applyWAFDigests still retires!
	_ = os.WriteFile(child, []byte("198.51.100.0/24\n"), 0o600)
	afterPrepared := captureLoadedAccessFiles(cfg).WithCompiledWAF(after, map[[2]int]*waf.Firewall{{0, 0}: after})
	afterPrepared.applyWAFDigests(after.SourceDigest(), map[[2]int]string{{0, 0}: after.SourceDigest()})
	transitionsPrepared := ComputePublishEntryTransitions(cfg, cfg, beforeFiles, cfg, cfg, afterPrepared, nil, nil)
	if len(transitionsPrepared) != 1 || !transitionsPrepared[0].Retire {
		t.Fatalf("prepared WAF SourceDigest must govern retirement even if disk file changed after Prepare: %+v", transitionsPrepared)
	}
}

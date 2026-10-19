// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/waf"
)

// TestWAFSharingModeBlockVsDetectOversizedBody pins the SD-14 / spec §9.5 WAF
// mode rules against the real Coraza engine over HTTP:
//  1. A request body exceeding request_body_limit in "block" mode is rejected
//     with 413 Request Entity Too Large and never reaches the upstream action.
//  2. The same oversized request body in "detect" mode is NOT rejected and the
//     complete body is delivered to the upstream action (200 OK).
//  3. An unreadable request body in "detect" mode fails before the action is
//     invoked (the AD-08 read-before-action transport boundary).
func TestWAFSharingModeBlockVsDetectOversizedBody(t *testing.T) {
	const limitBytes = 64
	oversizedPayload := bytes.Repeat([]byte("A"), limitBytes*4)

	buildWAFHandler := func(t *testing.T, mode string, next http.Handler) http.Handler {
		t.Helper()
		fw, err := waf.New(context.Background(), config.WAFConfig{
			Enabled:          true,
			Mode:             mode,
			BlockStatus:      http.StatusForbidden,
			RequestBodyLimit: config.Size(limitBytes),
			InlineRules:      `SecRule ARGS:attack "@streq 1" "id:1001,phase:2,deny,status:403"`,
		}, waf.Options{})
		if err != nil {
			t.Fatalf("waf.New(%s): %v", mode, err)
		}
		return fw.Middleware()(next)
	}

	t.Run("block mode rejects oversized body with 413 before action", func(t *testing.T) {
		var actionReached atomic.Bool
		srv := httptest.NewServer(buildWAFHandler(t, "block", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actionReached.Store(true)
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		})))
		defer srv.Close()

		resp, err := http.Post(srv.URL+"/upload", "text/plain", bytes.NewReader(oversizedPayload))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", resp.StatusCode)
		}
		if actionReached.Load() {
			t.Fatal("upstream action must not be reached when block mode rejects an oversized body")
		}
	})

	t.Run("detect mode delivers full oversized body to action with 200", func(t *testing.T) {
		var receivedBytes atomic.Int64
		srv := httptest.NewServer(buildWAFHandler(t, "detect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			receivedBytes.Store(int64(len(data)))
			w.WriteHeader(http.StatusOK)
		})))
		defer srv.Close()

		resp, err := http.Post(srv.URL+"/upload?attack=1", "text/plain", bytes.NewReader(oversizedPayload))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got := receivedBytes.Load(); got != int64(len(oversizedPayload)) {
			t.Fatalf("action received %d bytes, want full %d bytes", got, len(oversizedPayload))
		}
	})

	t.Run("detect mode reads body before action and aborts before action on unreadable body (AD-08)", func(t *testing.T) {
		var disabledReached atomic.Bool
		disabledHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			disabledReached.Store(true)
			_, _ = io.WriteString(w, "action-ran")
		})
		reqDisabled := httptest.NewRequest(http.MethodPost, "http://example.com/upload", &errReader{})
		reqDisabled.Header.Set("Content-Type", "text/plain")
		reqDisabled.ContentLength = 32
		rrDisabled := httptest.NewRecorder()
		disabledHandler.ServeHTTP(rrDisabled, reqDisabled)
		if !disabledReached.Load() || rrDisabled.Body.String() != "action-ran" {
			t.Fatalf("expected disabled handler that ignores body to run, got reached=%v body=%q", disabledReached.Load(), rrDisabled.Body.String())
		}

		var detectReached atomic.Bool
		detectHandler := buildWAFHandler(t, "detect", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			detectReached.Store(true)
			_, _ = io.WriteString(w, "action-ran")
		}))
		reqDetect := httptest.NewRequest(http.MethodPost, "http://example.com/upload", &errReader{})
		reqDetect.Header.Set("Content-Type", "text/plain")
		reqDetect.ContentLength = 32
		rrDetect := httptest.NewRecorder()
		detectHandler.ServeHTTP(rrDetect, reqDetect)
		if detectReached.Load() || rrDetect.Body.String() == "action-ran" {
			t.Fatalf("expected detect mode to read body before action and abort on unreadable body, got reached=%v body=%q", detectReached.Load(), rrDetect.Body.String())
		}
	})

	t.Run("ctl:ruleEngine=On in detect mode switches blocking on", func(t *testing.T) {
		fw, err := waf.New(context.Background(), config.WAFConfig{
			Enabled:          true,
			Mode:             "detect",
			BlockStatus:      http.StatusForbidden,
			RequestBodyLimit: config.Size(1024),
			InlineRules: strings.Join([]string{
				`SecRule REQUEST_HEADERS:X-Force-Block "@streq 1" "id:2001,phase:1,pass,ctl:ruleEngine=On"`,
				`SecRule ARGS:attack "@streq 1" "id:2002,phase:2,deny,status:403"`,
			}, "\n"),
		}, waf.Options{})
		if err != nil {
			t.Fatalf("waf.New: %v", err)
		}
		srv := httptest.NewServer(fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/?attack=1", nil)
		req.Header.Set("X-Force-Block", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 when ctl:ruleEngine=On triggers in detect mode", resp.StatusCode)
		}
	})
}

type errReader struct{}

func (*errReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated transport body read error")
}

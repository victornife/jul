// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/waf"
)

func TestInspectionStatusWithLiveWAFTraffic(t *testing.T) {
	limits := inspection.Limits{
		Mode:                   inspection.Enforce,
		MaxBytes:               1 << 30,
		MaxResponseHeaderBytes: 1 << 20,
		EvaluationSlots:        1,
		BaseChargeBytes:        16 << 10,
	}
	governor, err := inspection.New(limits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	firewall, err := waf.New(context.Background(), config.WAFConfig{
		Enabled: true, Mode: "block", InlineRules: "SecRuleEngine On",
	}, waf.Options{Governor: governor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := firewall.Close(); err != nil {
			t.Errorf("close firewall: %v", err)
		}
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var originCalls atomic.Int32
	traffic := httptest.NewServer(firewall.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if originCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	defer traffic.Close()
	defer releaseOnce.Do(func() { close(release) })
	client := &http.Client{Timeout: 10 * time.Second}
	first := make(chan error, 1)
	go func() {
		resp, err := client.Get(traffic.URL)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			if closeErr := resp.Body.Close(); err == nil {
				err = closeErr
			}
			if err == nil && resp.StatusCode != http.StatusNoContent {
				err = fmt.Errorf("first request status = %d", resp.StatusCode)
			}
		}
		first <- err
	}()
	select {
	case <-entered:
	case err := <-first:
		t.Fatalf("first request ended before reaching origin: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("first request did not reach origin")
	}

	snapshot := governor.Snapshot()
	var held uint64
	for _, amount := range snapshot.Usage {
		held += amount
	}
	if snapshot.ActiveLeases != 1 || held < 2 {
		t.Fatalf("first WAF request did not hold charges: %+v", snapshot)
	}
	limits.MaxBytes = held - 1
	if err := governor.PublishLimitsIfVersion(governor.Snapshot().LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(newTestServer(t, config.AdminConfig{}, Deps{
		InspectionSnapshot: governor.Snapshot,
	}).routes())
	defer admin.Close()
	status := func(path string) *adminapi.InspectionStatus {
		t.Helper()
		resp, err := client.Get(admin.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", path, resp.StatusCode)
		}
		var result struct {
			Inspection *adminapi.InspectionStatus `json:"inspection"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result.Inspection
	}
	waitStatus := func(mode string, leases uint64, draining bool) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			overview, v1 := status("/api/runtime/overview"), status("/api/v1/status")
			matches := func(got *adminapi.InspectionStatus) bool {
				return got != nil && got.Draining == draining &&
					got.Effective.Mode == mode && got.ActiveLeases == leases
			}
			if matches(overview) && matches(v1) {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("overview=%+v v1=%+v, want mode=%s leases=%d draining=%t",
					overview, v1, mode, leases, draining)
			case <-ticker.C:
			}
		}
	}
	waitStatus("enforce", 1, true)
	if got := status("/api/v1/status"); got == nil || got.Effective.MaxBufferedBytes != held-1 || got.Usage.WAFStateBytes == 0 {
		t.Fatalf("status did not report the lowered limit and live WAF charges: %+v", got)
	}
	resp, err := client.Get(traffic.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable ||
		resp.Header.Get("Retry-After") != "1" ||
		strings.TrimSpace(string(body)) != inspection.ReasonOverloaded || originCalls.Load() != 1 {
		t.Fatalf("enforce response = %d %q, origin calls=%d, error=%v", resp.StatusCode, body, originCalls.Load(), err)
	}

	limits.Mode = inspection.Observe
	if err := governor.PublishLimitsIfVersion(governor.Snapshot().LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	resp, err = client.Get(traffic.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil || resp.StatusCode != http.StatusNoContent || originCalls.Load() != 2 {
		t.Fatalf("observe response = %d, origin calls=%d, error=%v", resp.StatusCode, originCalls.Load(), err)
	}
	waitStatus("observe", 1, true)
	releaseOnce.Do(func() { close(release) })
	if err := <-first; err != nil {
		t.Fatalf("first request: %v", err)
	}
	waitStatus("observe", 0, false)
	if got := status("/api/v1/status"); got == nil || got.Usage != (adminapi.InspectionUsage{}) {
		t.Fatalf("status retained charges after release: %+v", got)
	}
}

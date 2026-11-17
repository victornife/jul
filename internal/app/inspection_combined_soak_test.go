// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build soak && waf && wasmplugins

package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

func TestInspectionCombinedSoak(t *testing.T) {
	duration := 30 * time.Second
	if value := os.Getenv("JUL_INSPECTION_SOAK_DURATION"); value != "" {
		var err error
		duration, err = time.ParseDuration(value)
		if err != nil || duration <= 0 {
			t.Fatalf("invalid JUL_INSPECTION_SOAK_DURATION %q", value)
		}
	}

	var origin, blockedOrigin atomic.Uint64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("soak_case") == "blocked" {
			blockedOrigin.Add(1)
		}
		origin.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "soak-ok")
	}))
	defer backend.Close()

	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	if err := writer.WriteField("probe", "ordinary"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("upload", "fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, strings.Repeat("ordinary upload ", 256)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	multipartType := writer.FormDataContentType()
	multipartBytes := multipartBody.Bytes()

	factory, cleanup := minimalFactory(t)
	defer cleanup()
	factory.Inspection = factory.RT.Inspection
	cfg := config.ProxyTarget(backend.URL, freePort(t))
	budget := config.Size(1 << 30)
	cfg.Global.Inspection.MaxBufferedBytes = &budget
	plugin := v2Plugin("upper")
	plugin.MemoryLimit = config.Size(16 << 20)
	plugin.MaxInvocations = 16
	plugin.MaxInstances = 4
	cfg.Plugins = map[string]config.PluginConfig{"p": plugin}
	cfg.Servers[0].Locations[0].Plugins = []string{"p"}
	cfg.WAF = config.WAFConfig{
		Enabled: true,
		Mode:    "block",
		InlineRules: `SecRequestBodyAccess On
SecRule ARGS:probe "@streq block-me" "id:9701,phase:2,deny,status:403,log"`,
	}
	initial, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective),
		factory.Log, factory.Prepare, config.NewTOMLSource(""), func(context.Context, *config.Config) error { return nil })
	srv.Inspection = factory.RT.Inspection
	srv.InspectionRequirement = factory.InspectionRequirement
	srv.PluginModulesUnchanged = factory.PluginModulesUnchanged
	srv.PluginModuleChanges = factory.PluginModuleChanges
	srv.WAFSourceDigests = factory.WAFSourceDigests
	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	reloads := make(chan server.ReloadRequest)
	go func() { serverDone <- srv.Run(ctx, reloads, initial.Redaction) }()
	serverStopped := false
	defer func() {
		cancel()
		if !serverStopped {
			select {
			case err := <-serverDone:
				if err != nil {
					t.Errorf("server shutdown: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Error("server shutdown timed out")
			}
		}
	}()
	select {
	case <-ready:
	case err := <-serverDone:
		serverStopped = true
		t.Fatalf("server startup: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("server startup timed out")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	url := "http://" + cfg.Servers[0].Listen + "/"
	var good, rejected [4]atomic.Uint64
	failures := make(chan error, 1)
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWorkers := func() { stopOnce.Do(func() { close(stop) }) }
	var workers sync.WaitGroup
	defer func() {
		stopWorkers()
		workers.Wait()
	}()
	start := time.Now()
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			ticker := time.NewTicker(125 * time.Millisecond)
			defer ticker.Stop()
			for seq := worker; ; seq++ {
				select {
				case <-stop:
					return
				default:
				}
				kind := seq % 4
				var body io.Reader
				method, contentType := http.MethodGet, ""
				switch kind {
				case 1:
					method, contentType = http.MethodPost, "application/x-www-form-urlencoded"
					body = strings.NewReader("probe=ordinary")
				case 2:
					method, contentType = http.MethodPost, multipartType
					body = bytes.NewReader(multipartBytes)
				case 3:
					method, contentType = http.MethodPost, "application/x-www-form-urlencoded"
					body = strings.NewReader("probe=block-me")
				}
				requestURL := url
				if kind == 3 {
					requestURL += "?soak_case=blocked"
				}
				req, err := http.NewRequest(method, requestURL, body)
				if err == nil {
					req.Header.Set("X-Mode", "body")
					if contentType != "" {
						req.Header.Set("Content-Type", contentType)
					}
					var resp *http.Response
					resp, err = client.Do(req)
					if err == nil {
						var response []byte
						response, err = io.ReadAll(resp.Body)
						closeErr := resp.Body.Close()
						if err == nil {
							err = closeErr
						}
						if err == nil {
							switch {
							case kind == 3 && resp.StatusCode == http.StatusForbidden:
								rejected[kind].Add(1)
							case kind != 3 && resp.StatusCode == http.StatusOK && string(response) == "SOAK-OK":
								good[kind].Add(1)
							default:
								err = fmt.Errorf("kind %d: unexpected status %d and body %q", kind, resp.StatusCode, response)
							}
						}
					}
				}
				if err != nil {
					select {
					case failures <- err:
					default:
					}
					return
				}
				select {
				case <-stop:
					return
				case <-ticker.C:
				}
			}
		}(worker)
	}

	interval := min(duration/10, 30*time.Second)
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	var reloadCount int
	var lastGood, maxGenerationGood uint64
	totalGood := func() uint64 { return good[0].Load() + good[1].Load() + good[2].Load() }
loop:
	for {
		select {
		case err := <-failures:
			stopWorkers()
			workers.Wait()
			t.Fatalf("traffic: %v", err)
		case err := <-serverDone:
			serverStopped = true
			stopWorkers()
			workers.Wait()
			t.Fatalf("server stopped during soak: %v", err)
		case <-timer.C:
			break loop
		case <-ticker.C:
			if count := totalGood() - lastGood; count > maxGenerationGood {
				maxGenerationGood = count
			}
			next, err := cfg.Clone()
			if err != nil {
				t.Fatal(err)
			}
			next.WAF.InlineRules = strings.Replace(cfg.WAF.InlineRules, "id:9701", fmt.Sprintf("id:%d", 9702+reloadCount%2), 1)
			candidate, err := config.NewCandidate(next)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan server.ReloadResult, 1)
			select {
			case reloads <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: candidate, Result: result}:
			case <-time.After(30 * time.Second):
				t.Fatal("reload submission timed out")
			}
			select {
			case rr := <-result:
				if rr.Outcome != server.ReloadAppliedLive {
					t.Fatalf("reload: %+v", rr)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("reload timed out")
			}
			reloadCount++
			lastGood = totalGood()
			snapshot := factory.RT.Inspection.Snapshot()
			guest := snapshot.GuestCapacity
			if guest.Instances[inspection.GuestFailedRetained] != 0 ||
				guest.ReservedBytes > snapshot.Limits.MaxGuestCapacityBytes {
				t.Fatalf("guest capacity during reload: %+v", guest)
			}
		}
	}
	stopWorkers()
	workers.Wait()
	activeDuration := time.Since(start)
	select {
	case err := <-failures:
		t.Fatalf("traffic: %v", err)
	default:
	}
	if reloadCount == 0 || good[0].Load() == 0 || good[1].Load() == 0 || good[2].Load() == 0 || rejected[3].Load() == 0 {
		t.Fatalf("incomplete workload: reloads=%d good=%d/%d/%d rejected=%d", reloadCount,
			good[0].Load(), good[1].Load(), good[2].Load(), rejected[3].Load())
	}
	if blockedOrigin.Load() != 0 {
		t.Fatalf("%d blocked requests reached the origin", blockedOrigin.Load())
	}
	goodTotal := totalGood()
	if count := goodTotal - lastGood; count > maxGenerationGood {
		maxGenerationGood = count
	}
	if duration >= time.Minute && maxGenerationGood <= uint64(plugin.MaxInvocations*plugin.MaxInstances) {
		t.Fatalf("no generation served enough requests to require recycling: maximum=%d", maxGenerationGood)
	}
	if got := origin.Load(); got != goodTotal {
		t.Fatalf("origin requests=%d, successful requests=%d", got, goodTotal)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		snapshot := factory.RT.Inspection.Snapshot()
		guest := snapshot.GuestCapacity
		if snapshot.ActiveLeases == 0 && snapshot.ActiveEvaluations == 0 &&
			snapshot.WaitingEvaluations == 0 && snapshot.Usage == ([len(snapshot.Usage)]uint64{}) &&
			guest.Instances[inspection.GuestStarting] == 0 && guest.Instances[inspection.GuestBusy] == 0 &&
			guest.Instances[inspection.GuestClosing] == 0 && guest.Instances[inspection.GuestFailedRetained] == 0 &&
			guest.Instances[inspection.GuestIdle] <= 4 && guest.ReservedBytes == guest.Instances[inspection.GuestIdle]*16<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed activity failed to drain to pool baseline: %+v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-serverDone:
		serverStopped = true
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server shutdown timed out")
	}
	factory.GenRes.CloseLive()
	if err := factory.PluginMgr.Close(); err != nil {
		t.Fatalf("plugin shutdown: %v", err)
	}
	if snapshot := factory.RT.Inspection.Snapshot(); snapshot.ActiveLeases != 0 ||
		snapshot.Usage != ([len(snapshot.Usage)]uint64{}) ||
		snapshot.GuestCapacity != (inspection.GuestCapacitySnapshot{}) {
		t.Fatalf("managed activity remained after shutdown: %+v", snapshot)
	}
	t.Logf("duration=%s reloads=%d max_generation_good=%d good=%d (get=%d form=%d multipart=%d) goodput=%.2f/s rejected=%d origin=%d blocked_origin=%d",
		activeDuration, reloadCount, maxGenerationGood, goodTotal, good[0].Load(), good[1].Load(), good[2].Load(),
		float64(goodTotal)/activeDuration.Seconds(), rejected[3].Load(), origin.Load(), blockedOrigin.Load())
}

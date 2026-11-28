// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/buildcaps"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
	"jul/internal/upstream"
)

const (
	quotaSaturationCallers     = 4096
	quotaSaturationCallerBurst = 10
	quotaSaturationFillWorkers = 32
	quotaSaturationWorkers     = 8
	quotaSaturationBaseline    = 200
	quotaSaturationSamples     = 500
	quotaSaturationP99Limit    = 5 * time.Millisecond
)

func TestQuotaProtectedHTTPAdmissionWithSaturatedPublicCallerStore(t *testing.T) {
	if os.Getenv("JUL_QUOTA_LATENCY_QUALIFICATION") != "1" {
		t.Skip("set JUL_QUOTA_LATENCY_QUALIFICATION=1 to run the predeclared real-server latency workload")
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	listen := freePort(t)
	certPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "tls", "localhost.crt"))
	if err != nil {
		t.Fatalf("resolve TLS certificate path: %v", err)
	}
	keyPath, err := filepath.Abs(filepath.Join("..", "..", "testdata", "tls", "localhost.key"))
	if err != nil {
		t.Fatalf("resolve TLS key path: %v", err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read TLS certificate: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("add TLS certificate to client root pool")
	}
	cfg := config.ProxyTarget(backend.URL, listen)
	cfg.Global.LogLevel = "error"
	cfg.Observability.AccessLog.Enabled = config.Bool(false)
	cfg.Servers[0].TLS = &config.TLSConfig{Enabled: true, Cert: certPath, Key: keyPath}
	if buildcaps.Compiled().HTTP3 {
		cfg.Servers[0].HTTP3 = &config.HTTP3Config{Enabled: true}
	}
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"latency": {
			Mode:       "enforce",
			Rate:       1_000_000_000,
			Burst:      1_000_000_000,
			MaxCallers: quotaSaturationCallers,
			Classes: map[string]config.QuotaClassConfig{
				"public": {
					Shared:    config.QuotaLimitConfig{Rate: 999_000_000, Burst: 999_000_000},
					PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: quotaSaturationCallerBurst},
				},
				"protected": {
					Shared:    config.QuotaLimitConfig{Rate: 10_000, Burst: 10_000},
					PerCaller: config.QuotaLimitConfig{Rate: 10_000, Burst: 10_000},
				},
			},
			Pools: map[string]config.QuotaPoolConfig{
				"protected": {Class: "protected", CIDRs: []string{"203.0.113.0/24"}},
			},
		},
	}
	cfg.Servers[0].ClientAddress = &config.ClientAddressConfig{
		TrustedProxies:   []string{"127.0.0.1/32"},
		ForwardedHeaders: []string{"x-forwarded-for"},
	}
	cfg.Servers[0].Locations[0].QuotaPolicy = "latency"

	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatalf("prepare quota latency config: %v", err)
	}
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal quota latency config: %v", err)
	}
	sourcePath := filepath.Join(t.TempDir(), "jul.toml")
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatalf("write quota latency config: %v", err)
	}

	factory, cleanup := minimalFactory(t)
	factory.QuotaRuntime = newQuotaRuntime()
	handlerFactory := server.HandlerFactory(func(ctx context.Context, candidate *config.Config) (
		map[string]http.Handler,
		uint64,
		func() (upstream.SnapshotMap, func()),
		func(),
		error,
	) {
		return factory.Prepare(ctx, candidate)
	})
	liveServer := server.New(
		candidate.Effective,
		candidate.Raw,
		lifecycle.ComputeFingerprint(candidate.Effective),
		factory.Log,
		handlerFactory,
		config.NewTOMLSource(sourcePath),
		func(_ context.Context, candidate *config.Config) error {
			return config.Validate(candidate)
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan server.ReloadRequest, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- liveServer.Run(ctx, reloads, candidate.Redaction)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after cancellation")
		}
		cleanup()
	})

	waitUntil := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitUntil) {
		if _, ok := liveServer.LiveSnapshot().Listeners[listen]; ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := liveServer.LiveSnapshot().Listeners[listen]; !ok {
		t.Fatal("server did not publish a bound listener")
	}

	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		t.Fatalf("split listener address %q: %v", listen, err)
	}
	endpoint := "https://127.0.0.1:" + port
	newHTTP1Client := func() *http.Client {
		return &http.Client{
			Timeout: 2 * time.Second,
			Transport: &http.Transport{
				MaxConnsPerHost: 1,
				TLSClientConfig: &tls.Config{
					RootCAs:    roots,
					ServerName: "localhost",
					NextProtos: []string{"http/1.1"},
				},
			},
		}
	}
	client := newHTTP1Client()
	defer client.CloseIdleConnections()
	h2Client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: roots, ServerName: "localhost"},
			ForceAttemptHTTP2: true,
		},
	}
	defer h2Client.CloseIdleConnections()
	var h3Client *http.Client
	if buildcaps.Compiled().HTTP3 {
		h3Transport := &http3.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"},
		}
		t.Cleanup(func() { _ = h3Transport.Close() })
		h3Client = &http.Client{Transport: h3Transport, Timeout: 2 * time.Second}
	}
	var fillWorkers sync.WaitGroup
	fillErrors := make(chan error, quotaSaturationFillWorkers)
	for worker := 0; worker < quotaSaturationFillWorkers; worker++ {
		fillWorkers.Add(1)
		go func(worker int) {
			defer fillWorkers.Done()
			fillClient := newHTTP1Client()
			defer fillClient.CloseIdleConnections()
			for caller := worker; caller < quotaSaturationCallers; caller += quotaSaturationFillWorkers {
				address := fmt.Sprintf("198.18.%d.%d", caller/256, caller%256)
				for request := 0; request < quotaSaturationCallerBurst; request++ {
					resp, err := quotaSaturationRequestRaw(fillClient, endpoint, address)
					if err != nil {
						select {
						case fillErrors <- err:
						default:
						}
						return
					}
					if resp.StatusCode != http.StatusOK {
						select {
						case fillErrors <- fmt.Errorf("fill public caller %d (%s) request %d: status=%d, want 200", caller, address, request, resp.StatusCode):
						default:
						}
						return
					}
				}
			}
		}(worker)
	}
	fillWorkers.Wait()
	close(fillErrors)
	for err := range fillErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	protectedSample := func(sampleClient *http.Client, count, expectedProtocol int) []time.Duration {
		t.Helper()
		latencies := make([]time.Duration, 0, count)
		for i := 0; i < count; i++ {
			start := time.Now()
			resp := quotaSaturationRequest(t, sampleClient, endpoint, "203.0.113.10")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("protected sample %d: status=%d, want 200", i, resp.StatusCode)
			}
			if resp.ProtoMajor != expectedProtocol {
				t.Fatalf("protected sample %d: protocol=%q, want HTTP/%d", i, resp.Proto, expectedProtocol)
			}
			latencies = append(latencies, time.Since(start))
		}
		return latencies
	}
	baselineStart := time.Now()
	baselineHTTP1 := protectedSample(client, quotaSaturationBaseline, 1)
	baselineHTTP1Elapsed := time.Since(baselineStart)
	baselineStart = time.Now()
	baselineHTTP2 := protectedSample(h2Client, quotaSaturationBaseline, 2)
	baselineHTTP2Elapsed := time.Since(baselineStart)
	var baselineHTTP3 []time.Duration
	var baselineHTTP3Elapsed time.Duration
	if h3Client != nil {
		baselineStart = time.Now()
		baselineHTTP3 = protectedSample(h3Client, quotaSaturationBaseline, 3)
		baselineHTTP3Elapsed = time.Since(baselineStart)
	}

	// Re-drain early buckets that may have fully refilled during setup and
	// baseline sampling. Reverse the fill order so the oldest buckets are
	// charged last, immediately before the measured saturation interval.
	var drainWorkers sync.WaitGroup
	drainErrors := make(chan error, quotaSaturationFillWorkers)
	for worker := 0; worker < quotaSaturationFillWorkers; worker++ {
		drainWorkers.Add(1)
		go func(worker int) {
			defer drainWorkers.Done()
			drainClient := newHTTP1Client()
			defer drainClient.CloseIdleConnections()
			for caller := quotaSaturationCallers - 1 - worker; caller >= 0; caller -= quotaSaturationFillWorkers {
				address := fmt.Sprintf("198.18.%d.%d", caller/256, caller%256)
				resp, err := quotaSaturationRequestRaw(drainClient, endpoint, address)
				if err != nil {
					select {
					case drainErrors <- err:
					default:
					}
					return
				}
				if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusTooManyRequests {
					select {
					case drainErrors <- fmt.Errorf("drain public caller %d (%s): status=%d, want 200 or 429", caller, address, resp.StatusCode):
					default:
					}
					return
				}
			}
		}(worker)
	}
	drainWorkers.Wait()
	close(drainErrors)
	for err := range drainErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	stopLoad := make(chan struct{})
	var workers sync.WaitGroup
	var publicDenials atomic.Uint64
	var publicEvictions atomic.Uint64
	loadErrors := make(chan error, quotaSaturationWorkers)
	for worker := 0; worker < quotaSaturationWorkers; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			loadClient := newHTTP1Client()
			defer loadClient.CloseIdleConnections()
			for sequence := 0; ; sequence++ {
				select {
				case <-stopLoad:
					return
				default:
				}
				caller := worker*8192 + sequence
				address := fmt.Sprintf("198.19.%d.%d", caller/256, caller%256)
				resp, err := quotaSaturationRequestRaw(loadClient, endpoint, address)
				if err != nil {
					select {
					case loadErrors <- err:
					default:
					}
					return
				}
				switch resp.StatusCode {
				case http.StatusOK:
					publicEvictions.Add(1)
				case http.StatusTooManyRequests:
					publicDenials.Add(1)
				default:
					select {
					case loadErrors <- fmt.Errorf("public caller %s returned %d, want 200 after safe eviction or caller_capacity 429", address, resp.StatusCode):
					default:
					}
					return
				}
			}
		}(worker)
	}
	loadedStart := time.Now()
	loadedHTTP1 := protectedSample(client, quotaSaturationSamples, 1)
	loadedHTTP1Elapsed := time.Since(loadedStart)
	loadedStart = time.Now()
	loadedHTTP2 := protectedSample(h2Client, quotaSaturationSamples, 2)
	loadedHTTP2Elapsed := time.Since(loadedStart)
	var loadedHTTP3 []time.Duration
	var loadedHTTP3Elapsed time.Duration
	if h3Client != nil {
		loadedStart = time.Now()
		loadedHTTP3 = protectedSample(h3Client, quotaSaturationSamples, 3)
		loadedHTTP3Elapsed = time.Since(loadedStart)
	}
	close(stopLoad)
	workers.Wait()
	close(loadErrors)
	for err := range loadErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	decisions := factory.QuotaRuntime.decisionSnapshot()["latency"]
	if decisions["enforce:caller_capacity"] == 0 || publicDenials.Load() == 0 {
		t.Fatalf("public store saturation did not record caller_capacity: %v", decisions)
	}
	samples := []struct {
		protocol         string
		baseline         []time.Duration
		baselineElapsed  time.Duration
		saturated        []time.Duration
		saturatedElapsed time.Duration
	}{
		{
			protocol:         "HTTP/1.1 over TLS",
			baseline:         baselineHTTP1,
			baselineElapsed:  baselineHTTP1Elapsed,
			saturated:        loadedHTTP1,
			saturatedElapsed: loadedHTTP1Elapsed,
		},
		{
			protocol:         "HTTP/2 over TLS (ALPN)",
			baseline:         baselineHTTP2,
			baselineElapsed:  baselineHTTP2Elapsed,
			saturated:        loadedHTTP2,
			saturatedElapsed: loadedHTTP2Elapsed,
		},
	}
	if h3Client != nil {
		samples = append(samples, struct {
			protocol         string
			baseline         []time.Duration
			baselineElapsed  time.Duration
			saturated        []time.Duration
			saturatedElapsed time.Duration
		}{
			protocol:         "HTTP/3 over QUIC",
			baseline:         baselineHTTP3,
			baselineElapsed:  baselineHTTP3Elapsed,
			saturated:        loadedHTTP3,
			saturatedElapsed: loadedHTTP3Elapsed,
		})
	}
	for _, sample := range samples {
		baselineP99 := durationPercentile(sample.baseline, 99)
		loadedP99 := durationPercentile(sample.saturated, 99)
		t.Logf("environment=Jul Server.Run real TCP %s; upstream=httptest; max_callers=%d; public_caller_burst=%d; fill_workers=%d; flood_workers=%d; caller_capacity_denials=%d; safe_evictions=%d; baseline_samples=%d; baseline_elapsed=%s; baseline_p50=%s; baseline_p95=%s; baseline_p99=%s; saturated_samples=%d; saturated_elapsed=%s; saturated_p50=%s; saturated_p95=%s; saturated_p99=%s; saturated_throughput=%.0f protected_req/s; target_p99<=%s; target_enforced=%t",
			sample.protocol, quotaSaturationCallers, quotaSaturationCallerBurst, quotaSaturationFillWorkers, quotaSaturationWorkers, publicDenials.Load(), publicEvictions.Load(),
			len(sample.baseline), sample.baselineElapsed, durationPercentile(sample.baseline, 50), durationPercentile(sample.baseline, 95), baselineP99,
			len(sample.saturated), sample.saturatedElapsed, durationPercentile(sample.saturated, 50), durationPercentile(sample.saturated, 95), loadedP99,
			float64(len(sample.saturated))/sample.saturatedElapsed.Seconds(), quotaSaturationP99Limit, quotaSaturationEnforceLatencyTarget)
		if quotaSaturationEnforceLatencyTarget && loadedP99 > quotaSaturationP99Limit {
			t.Fatalf("%s protected-class saturated p99 %s exceeds predeclared target %s", sample.protocol, loadedP99, quotaSaturationP99Limit)
		}
	}
}

func quotaSaturationRequest(t *testing.T, client *http.Client, endpoint, address string) *http.Response {
	t.Helper()
	resp, err := quotaSaturationRequestRaw(client, endpoint, address)
	if err != nil {
		t.Fatalf("request for %s: %v", address, err)
	}
	return resp
}

func quotaSaturationRequestRaw(client *http.Client, endpoint, address string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint+"/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Forwarded-For", address)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return resp, nil
}

func durationPercentile(samples []time.Duration, percentile int) time.Duration {
	ordered := append([]time.Duration(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := (percentile*len(ordered)+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return ordered[index]
}

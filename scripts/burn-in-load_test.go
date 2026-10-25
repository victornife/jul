//go:build ignore

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileCaptureRejectsHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-profile-token" {
					t.Error("missing profile authorization")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, "synthetic forbidden body")
			}))
			defer server.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "heap-T0.out")
			if err := os.WriteFile(path, []byte("previous complete profile"), 0600); err != nil {
				t.Fatal(err)
			}
			err := captureProfile(server.Client(), server.URL, path, "synthetic-profile-token")
			if err == nil || err.Error() != fmt.Sprintf("HTTP %d", status) {
				t.Fatalf("got %v, want HTTP status failure", err)
			}
			assertProfileFiles(t, dir, "heap-T0.out", "previous complete profile")
		})
	}
}

func TestProfileCaptureRejectsIncompleteAndEmptyBodies(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if truncated {
					w.Header().Set("Content-Length", "100")
					fmt.Fprint(w, "partial profile")
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			err := captureProfile(server.Client(), server.URL, filepath.Join(dir, "heap.out"), "secret")
			if err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("accepted incomplete/empty profile: %v", err)
			}
			assertProfileFiles(t, dir, "", "")
		})
	}
}

func TestProfileCaptureTransportTrustAndFilesystemFailures(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "complete profile") }))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "heap.out")
	if err := captureProfile(&http.Client{Timeout: time.Second}, server.URL, path, "secret"); err == nil {
		t.Fatal("untrusted TLS profile accepted")
	}
	assertProfileFiles(t, dir, "", "")
	if err := captureProfile(server.Client(), server.URL, filepath.Join(dir, "missing", "heap.out"), "secret"); err == nil || !strings.Contains(err.Error(), "temporary") {
		t.Fatalf("output creation failure: %v", err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := captureProfile(server.Client(), server.URL, path, "secret"); err == nil || !strings.Contains(err.Error(), "publish") {
		t.Fatalf("publication failure: %v", err)
	}
	assertProfileFiles(t, dir, "heap.out", "")
	if err := captureProfile(server.Client(), ":bad secret URL", path, "secret"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid request diagnostic: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := captureProfile(server.Client(), server.URL, path, "secret"); err != nil {
		t.Fatal(err)
	}
	assertProfileFiles(t, dir, "heap.out", "complete profile")
}

func TestProfileCaptureTimeout(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	dir := t.TempDir()
	err := captureProfile(&http.Client{Timeout: 100 * time.Millisecond}, server.URL, filepath.Join(dir, "heap.out"), "secret")
	<-entered
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("timeout: %v", err)
	}
	assertProfileFiles(t, dir, "", "")
}

func TestSnapshotsRejectRedirectWithoutFollowingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			t.Error("profile redirect followed")
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	dir := t.TempDir()
	if snapPProf(server.URL, dir, "T0", "synthetic-token") {
		t.Fatal("redirected captures reported success")
	}
	assertProfileFiles(t, dir, "", "")
}

func assertProfileFiles(t *testing.T, dir, name, content string) {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if name == "" {
		if len(files) != 0 {
			t.Fatalf("partial files remain: %v", files)
		}
		return
	}
	if len(files) != 1 || files[0].Name() != name {
		t.Fatalf("unexpected output files: %v", files)
	}
	if !files[0].IsDir() {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != content {
			t.Fatalf("profile changed: %q, %v", got, err)
		}
	}
}

func TestLoadProfileCompletionAndPreflight(t *testing.T) {
	for _, mode := range []string{"success", "forbidden", "output failure", "health failure"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/healthz" && mode == "health failure":
					w.WriteHeader(503)
				case strings.HasPrefix(r.URL.Path, "/debug/pprof/"):
					if r.Header.Get("Authorization") != "Bearer synthetic-profile-token" {
						t.Error("wrong profile token")
					}
					if mode == "forbidden" {
						w.WriteHeader(403)
						fmt.Fprint(w, "synthetic forbidden body")
					} else {
						pprof.Handler(strings.TrimPrefix(r.URL.Path, "/debug/pprof/")).ServeHTTP(w, r)
					}
				default:
					fmt.Fprint(w, "OK")
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			if mode == "output failure" {
				dir = filepath.Join(dir, "existing-file")
				if err := os.WriteFile(dir, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args, flags, stdout := os.Args, flag.CommandLine, os.Stdout
			defer func() { os.Args, flag.CommandLine, os.Stdout = args, flags, stdout }()
			flag.CommandLine = flag.NewFlagSet("profile-test", flag.ContinueOnError)
			os.Args = []string{"profile-test", "-duration=100ms", "-workers=1", "-base=" + server.URL, "-tls=" + server.URL, "-health=" + server.URL + "/healthz", "-admin=" + server.URL, "-out=" + dir, "-authUser=", "-adminToken=synthetic-profile-token"}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = writer
			code := runLoad()
			writer.Close()
			os.Stdout = stdout
			output, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			text := string(output)
			if strings.Contains(text, "synthetic-profile-token") || strings.Contains(text, "synthetic forbidden body") {
				t.Fatalf("capture diagnostic leaks synthetic secret/body: %s", text)
			}
			if mode == "success" {
				if code != 0 || !strings.Contains(text, "All four profiles saved") || !strings.Contains(text, "Total requests") {
					t.Fatalf("successful completion: code=%d\n%s", code, text)
				}
				for _, suffix := range []string{"T0", "Tend"} {
					for _, name := range []string{"goroutine", "heap"} {
						body, err := os.ReadFile(filepath.Join(dir, name+"-"+suffix+".out"))
						if err != nil || !strings.Contains(string(body), "profile:") {
							t.Fatalf("missing real %s profile: %v", name, err)
						}
					}
				}
			} else {
				if code != 1 || strings.Contains(text, "All four profiles saved") {
					t.Fatalf("false successful capture: code=%d\n%s", code, text)
				}
				if mode == "forbidden" {
					if strings.Count(text, "FAILED: HTTP 403") != 4 || !strings.Contains(text, "Total requests") {
						t.Fatalf("capture/traffic outcomes not distinct:\n%s", text)
					}
					assertProfileFiles(t, dir, "", "")
				}
			}
		})
	}
}

func TestF1TransportValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := buildF1Transport("https://127.0.0.1:8443", "", 4); err == nil {
		t.Fatal("expected missing -caFile to fail")
	}
	if _, err := buildF1Transport("https://127.0.0.1:8443", filepath.Join(dir, "missing.pem"), 4); err == nil {
		t.Fatal("expected unreadable -caFile to fail")
	}
	badCA := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badCA, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildF1Transport("https://127.0.0.1:8443", badCA, 4); err == nil {
		t.Fatal("expected invalid PEM to fail")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	caPath := filepath.Join(dir, "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildF1Transport(":bad-url", caPath, 4); err == nil {
		t.Fatal("expected invalid URL to fail")
	}
	tr, err := buildF1Transport("https://example.com", caPath, 4)
	if err != nil {
		t.Fatalf("expected valid transport without port: %v", err)
	}
	tr.CloseIdleConnections()
}

func TestF1RouteMixAndOracles(t *testing.T) {
	validHTML := "<!doctype html><html><body>" + f1PortalMarker + "</body></html>"
	validAsset := []byte(f1PortalMarker + strings.Repeat("A", 8192-len(f1PortalMarker)))
	validDashJSON := fmt.Sprintf(`{"marker":%q,"private":%q}`, f1DashboardMarker, f1PrivateLeakMarker)

	for _, tc := range []struct {
		name     string
		mode     string
		phase    string
		wantCode int
	}{
		{name: "healthy_control", mode: "healthy", phase: "healthy", wantCode: 0},
		{name: "withdrawn_control", mode: "withdrawn_ok", phase: "withdrawn", wantCode: 0},
		{name: "outage_control", mode: "outage_ok", phase: "outage", wantCode: 0},
		{name: "transitioning_control", mode: "transition_mix", phase: "transitioning", wantCode: 0},
		{name: "healthy_phase_403_violation", mode: "withdrawn_ok", phase: "healthy", wantCode: 1},
		{name: "withdrawn_phase_200_leak_violation", mode: "healthy", phase: "withdrawn", wantCode: 1},
		{name: "withdrawn_403_private_marker_leak_violation", mode: "withdrawn_leak", phase: "withdrawn", wantCode: 1},
		{name: "recovered_phase_502_violation", mode: "outage_ok", phase: "healthy", wantCode: 1},
		{name: "excluded_leak_violation", mode: "excluded_200", phase: "healthy", wantCode: 1},
		{name: "truncated_asset_violation", mode: "short_asset", phase: "healthy", wantCode: 1},
		{name: "truncated_html_violation", mode: "truncated_html", phase: "healthy", wantCode: 1},
		{name: "held_sse_bad_admission_violation", mode: "bad_sse", phase: "healthy", wantCode: 1},
		{name: "preflight_failure", mode: "preflight_500", phase: "healthy", wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reqIdx int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				idx := atomic.AddInt64(&reqIdx, 1)
				switch r.URL.Path {
				case "/index.html":
					if tc.mode == "preflight_500" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if tc.mode == "truncated_html" && idx > 1 {
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, f1PortalMarker+" truncated before closing html")
						return
					}
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, validHTML)
				case "/assets/bundle.bin":
					w.WriteHeader(http.StatusOK)
					if tc.mode == "short_asset" {
						_, _ = w.Write(validAsset[:1024])
						return
					}
					_, _ = w.Write(validAsset)
				case "/events":
					if tc.mode == "bad_sse" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, "data: "+f1DashboardMarker+"\n\n")
				case "/api/summary":
					auth := r.Header.Get("Authorization")
					if !strings.HasPrefix(auth, "Basic ") || strings.Contains(r.Header.Get("Tailscale-User-Login"), "forged") {
						if tc.mode == "excluded_200" {
							w.WriteHeader(http.StatusUnauthorized)
							fmt.Fprint(w, f1PrivateLeakMarker)
							return
						}
						if idx%2 == 0 {
							w.WriteHeader(http.StatusUnauthorized)
						} else {
							w.WriteHeader(http.StatusForbidden)
						}
						return
					}
					switch tc.mode {
					case "withdrawn_ok":
						w.Header().Set("Cache-Control", "no-store")
						w.WriteHeader(http.StatusForbidden)
					case "withdrawn_leak":
						w.Header().Set("Cache-Control", "no-store")
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, f1PrivateLeakMarker)
					case "outage_ok":
						w.WriteHeader(http.StatusBadGateway)
					case "transition_mix":
						if idx%2 == 0 {
							w.Header().Set("Cache-Control", "no-store")
							w.WriteHeader(http.StatusForbidden)
						} else {
							w.WriteHeader(http.StatusOK)
							fmt.Fprint(w, validDashJSON)
						}
					default:
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, validDashJSON)
					}
				}
			}))
			defer srv.Close()

			dir := t.TempDir()
			caPath := filepath.Join(dir, "ca.pem")
			pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
			if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			phasePath := filepath.Join(dir, "phase.json")
			_ = os.WriteFile(phasePath, []byte(fmt.Sprintf(`{"phase":%q}`, tc.phase)), 0o600)
			evPath := filepath.Join(dir, "events.jsonl")
			sumPath := filepath.Join(dir, "summary.json")
			heldReady := filepath.Join(dir, "held.ready")

			args, flags := os.Args, flag.CommandLine
			defer func() { os.Args, flag.CommandLine = args, flags }()
			flag.CommandLine = flag.NewFlagSet("f1-test", flag.ContinueOnError)
			os.Args = []string{
				"f1-test",
				"-f1",
				"-duration=180ms",
				"-workers=2",
				"-tls=" + srv.URL,
				"-caFile=" + caPath,
				"-portalHost=example.com",
				"-dashHost=example.com",
				"-authUser=soakuser",
				"-authPassword=soakpass",
				"-excludedRatio=25",
				"-heldConns=2",
				"-heldReadyFile=" + heldReady,
				"-probeInterval=35ms",
				"-probeDash",
				"-phaseFile=" + phasePath,
				"-expectedAssetBytes=8192",
				"-pace=4ms",
				"-eventLog=" + evPath,
				"-summaryOut=" + sumPath,
			}
			code := runLoad()
			if code != tc.wantCode {
				t.Fatalf("%s: got code %d, want %d", tc.name, code, tc.wantCode)
			}
			if tc.wantCode == 0 {
				b, err := os.ReadFile(sumPath)
				if err != nil {
					t.Fatalf("missing summary.json: %v", err)
				}
				var s f1Summary
				if err := json.Unmarshal(b, &s); err != nil {
					t.Fatal(err)
				}
				if s.TotalRequests == 0 || s.OracleViolations != 0 || s.HeldOpened != 2 || s.ProbePortalOK == 0 || len(s.ByClass) == 0 {
					t.Fatalf("unexpected summary counts: %+v", s)
				}
			}
		})
	}

	// Error paths: missing CA, invalid eventLog path, invalid summaryOut path, and unreachable preflight
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validHTML)
	}))
	defer srv.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	if got := runF1Mode(f1Options{TLSBase: srv.URL, CAFile: ""}); got != 1 {
		t.Fatalf("expected missing CA to return 1, got %d", got)
	}
	if got := runF1Mode(f1Options{TLSBase: srv.URL, CAFile: caPath, EventLogPath: dir}); got != 1 {
		t.Fatalf("expected directory eventLog path to return 1, got %d", got)
	}
	if got := runF1Mode(f1Options{TLSBase: "https://127.0.0.1:1", CAFile: caPath, PortalHost: "example.com"}); got != 1 {
		t.Fatalf("expected unreachable preflight to return 1, got %d", got)
	}
}

func TestF1SharedH2AndWebSocketRetirement(t *testing.T) {
	var withdrawn atomic.Bool
	sseClose := make(chan struct{})
	wsClose := make(chan struct{})
	assetPayload := []byte(f1PortalMarker + strings.Repeat("P", 8192-len(f1PortalMarker)))

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/bundle.bin":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(assetPayload[:4096])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			time.Sleep(15 * time.Millisecond)
			_, _ = w.Write(assetPayload[4096:])
		case "/index.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<!doctype html><html><body>" + f1PortalMarker + "</body></html>"))
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: " + f1DashboardMarker + "\n\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-sseClose
		case "/ws":
			hj, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, rw, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = rw.Flush()
			_, _ = conn.Write([]byte{0x81, 0x04, 't', 'e', 's', 't'})
			<-wsClose
			_, _ = conn.Write([]byte{0x88, 0x02, 0x03, 0xF0})
			buf := make([]byte, 32)
			_ = conn.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
			_, _ = conn.Read(buf)
		case "/api/summary":
			if withdrawn.Load() {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"marker":"` + f1DashboardMarker + `","private":"` + f1PrivateLeakMarker + `"}`))
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	readyPath := filepath.Join(dir, "ready.txt")
	pubSigPath := filepath.Join(dir, "publish.sig")
	evPath := filepath.Join(dir, "h2-events.jsonl")
	sumPath := filepath.Join(dir, "h2-summary.json")

	go func() {
		for {
			if _, err := os.Stat(readyPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(pubSigPath, []byte("publish\n"), 0o600)
		time.Sleep(15 * time.Millisecond)
		withdrawn.Store(true)
		close(sseClose)
		close(wsClose)
	}()

	args, flags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = args, flags }()
	flag.CommandLine = flag.NewFlagSet("f1-h2-test", flag.ContinueOnError)
	os.Args = []string{
		"f1-h2-test",
		"-f1SharedH2",
		"-duration=5s",
		"-tls=" + srv.URL,
		"-caFile=" + caPath,
		"-portalHost=example.com",
		"-dashHost=example.com",
		"-authUser=soakuser",
		"-authPassword=soakpass",
		"-expectedAssetBytes=8192",
		"-pace=10ms",
		"-readyFile=" + readyPath,
		"-publishSignalFile=" + pubSigPath,
		"-retirementBound=2s",
		"-withWS=true",
		"-wsEchoClose=true",
		"-eventLog=" + evPath,
		"-summaryOut=" + sumPath,
	}
	if code := runLoad(); code != 0 {
		t.Fatalf("expected runF1SharedH2 to succeed with 0, got %d", code)
	}
	b, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatal(err)
	}
	var res f1SharedH2Result
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.DialCount != 1 || res.NegotiatedProtocol != "h2" || !res.PortalStreamCompletedOK || !res.PortalActiveAtRetirement || res.PortalBytesBeforeRetirement != 4096 || res.PortalBytesAfterRetirement != 4096 || !res.RetirementWithinBound || res.PrematureCloseBeforePublish || res.WSCloseCode != 1008 || !res.WSGracefulHandshake {
		t.Fatalf("unexpected shared H2 result: %+v", res)
	}

	// Test HTTP/1.1 keep-alive retirement and revocation mode (-f1H1KeepAlive)
	withdrawn.Store(false)
	h1SSEClose := make(chan struct{})
	sseClose = h1SSEClose
	h1Ready := filepath.Join(dir, "h1.ready")
	h1Pub := filepath.Join(dir, "h1.pub")
	h1Sum := filepath.Join(dir, "h1-summary.json")
	go func() {
		for {
			if _, err := os.Stat(h1Ready); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.WriteFile(h1Pub, []byte("pub\n"), 0o600)
		time.Sleep(15 * time.Millisecond)
		withdrawn.Store(true)
		close(h1SSEClose)
	}()
	if code := runF1SharedH2(f1SharedH2Options{
		TLSBase:                srv.URL,
		CAFile:                 caPath,
		PortalHost:             "example.com",
		DashHost:               "example.com",
		AuthUser:               "soakuser",
		AuthPassword:           "soakpass",
		ReadyFile:              h1Ready,
		PublishSignalFile:      h1Pub,
		RetirementBound:        2 * time.Second,
		ExpectedPostDashStatus: http.StatusForbidden,
		HTTP1KeepAlive:         true,
		SummaryPath:            h1Sum,
		Timeout:                3 * time.Second,
	}); code != 0 {
		t.Fatalf("expected HTTP1KeepAlive run to return 0, got %d", code)
	}

	// Test forced close (wsEchoClose = false) and error branches
	c1, c2 := make(chan struct{}), make(chan struct{})
	close(c1)
	close(c2)
	sseClose = c1
	wsClose = c2
	if code := runF1SharedH2(f1SharedH2Options{
		TLSBase:            srv.URL,
		CAFile:             caPath,
		ClientCert:         "testdata/tls/client.crt",
		ClientKey:          "testdata/tls/client.key",
		PortalHost:         "example.com",
		DashHost:           "example.com",
		AuthUser:           "soakuser",
		AuthPassword:       "soakpass",
		ExpectedAssetBytes: 8192,
		WithWS:             true,
		WSEchoClose:        false,
		Timeout:            3 * time.Second,
	}); code != 0 {
		t.Fatalf("expected wsEchoClose=false run to return 0, got %d", code)
	}
	if code := runF1SharedH2(f1SharedH2Options{
		TLSBase:            srv.URL,
		CAFile:             caPath,
		PortalHost:         "example.com",
		DashHost:           "example.com",
		ExpectedAssetBytes: 8192,
		WithWS:             false,
	}); code != 0 {
		t.Fatalf("expected WithWS=false run to return 0, got %d", code)
	}
	if code := runF1SharedH2(f1SharedH2Options{
		TLSBase:    srv.URL,
		CAFile:     caPath,
		PortalHost: "wrong.invalid",
		DashHost:   "example.com",
	}); code != 1 {
		t.Fatalf("expected wrong ServerName handshake to fail, got %d", code)
	}

	if code := runF1SharedH2(f1SharedH2Options{TLSBase: srv.URL, CAFile: filepath.Join(dir, "missing.pem")}); code != 1 {
		t.Fatalf("expected missing CA to fail, got %d", code)
	}
	badCA := filepath.Join(dir, "bad.pem")
	_ = os.WriteFile(badCA, []byte("invalid"), 0o600)
	if code := runF1SharedH2(f1SharedH2Options{TLSBase: srv.URL, CAFile: badCA}); code != 1 {
		t.Fatalf("expected bad CA to fail, got %d", code)
	}
	if code := runF1SharedH2(f1SharedH2Options{TLSBase: ":bad", CAFile: caPath}); code != 1 {
		t.Fatalf("expected bad URL to fail, got %d", code)
	}
	if code := runF1SharedH2(f1SharedH2Options{TLSBase: "https://127.0.0.1:1", CAFile: caPath, PortalHost: "example.com"}); code != 1 {
		t.Fatalf("expected unreachable dial to fail, got %d", code)
	}

	// Test portal already completed before retirement (only 4096 bytes so 0 bytes after retirement), portal 500, dash SSE 500, ws 403, and post-change 200 failure branches
	for _, failStage := range []string{"portal_already_complete", "portal500", "sse500", "ws403", "post200"} {
		s2 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/assets/bundle.bin":
				if failStage == "portal500" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(strings.Repeat("P", 4096)))
			case "/events":
				if failStage == "sse500" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("data: " + f1DashboardMarker + "\n\n"))
			case "/ws":
				if failStage == "ws403" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			case "/index.html":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(f1PortalMarker))
			case "/api/summary":
				if failStage == "portal_already_complete" {
					w.Header().Set("Cache-Control", "no-store")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusOK)
			}
		}))
		s2.EnableHTTP2 = true
		s2.StartTLS()
		ca2 := filepath.Join(dir, "ca-"+failStage+".pem")
		_ = os.WriteFile(ca2, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s2.Certificate().Raw}), 0o600)
		got := runF1SharedH2(f1SharedH2Options{
			TLSBase:            s2.URL,
			CAFile:             ca2,
			PortalHost:         "example.com",
			DashHost:           "example.com",
			ExpectedAssetBytes: 4096,
			WithWS:             failStage == "ws403",
			Timeout:            200 * time.Millisecond,
		})
		s2.Close()
		if got != 1 {
			t.Fatalf("stage %s: expected code 1, got %d", failStage, got)
		}
	}
}

func TestF1HelpersAndEdgeCases(t *testing.T) {
	dir := t.TempDir()

	// readBoundedF1Body oversize and read error
	if _, err := readBoundedF1Body(strings.NewReader("123456"), 4); err == nil {
		t.Fatal("expected oversize readBoundedF1Body to fail")
	}

	// currentF1Phase plain-text, InitialPhase, AllowOutage, and default healthy
	plainPhase := filepath.Join(dir, "plain.phase")
	_ = os.WriteFile(plainPhase, []byte("withdrawn\n"), 0o600)
	if got := currentF1Phase(f1Options{PhaseFile: plainPhase}); got != "withdrawn" {
		t.Fatalf("expected plain phase withdrawn, got %q", got)
	}
	if got := currentF1Phase(f1Options{InitialPhase: "outage"}); got != "outage" {
		t.Fatalf("expected InitialPhase outage, got %q", got)
	}
	if got := currentF1Phase(f1Options{AllowOutage: true}); got != "outage" {
		t.Fatalf("expected AllowOutage -> outage, got %q", got)
	}
	if got := currentF1Phase(f1Options{}); got != "healthy" {
		t.Fatalf("expected default healthy, got %q", got)
	}

	// evaluateF1ResponseOracle branches: rerr != nil, default expBytes, ExpectedAssetSHA256 mismatch, phaseStart != phaseEnd, unknown class/phase
	hdr := http.Header{"Cache-Control": []string{"no-store"}}
	validDash := []byte(`{"marker":"` + f1DashboardMarker + `","private":"` + f1PrivateLeakMarker + `"}`)
	if evaluateF1ResponseOracle(f1Options{}, "portal_html", "healthy", "healthy", 200, hdr, []byte("x"), io.ErrUnexpectedEOF) {
		t.Fatal("expected rerr != nil to fail oracle")
	}
	if evaluateF1ResponseOracle(f1Options{ExpectedAssetBytes: len(f1PortalMarker), ExpectedAssetSHA256: "deadbeef"}, "portal_asset", "healthy", "healthy", 200, hdr, []byte(f1PortalMarker), nil) {
		t.Fatal("expected SHA256 mismatch to fail oracle")
	}
	if !evaluateF1ResponseOracle(f1Options{}, "dash_auth", "healthy", "withdrawn", 403, hdr, nil, nil) {
		t.Fatal("expected cross-phase healthy->withdrawn 403 to pass oracle")
	}
	if !evaluateF1ResponseOracle(f1Options{}, "dash_auth", "healthy", "withdrawn", 200, hdr, validDash, nil) {
		t.Fatal("expected cross-phase healthy->withdrawn 200 to pass oracle")
	}
	if evaluateF1ResponseOracle(f1Options{}, "dash_auth", "unknown_phase", "unknown_phase", 200, hdr, validDash, nil) {
		t.Fatal("expected unknown phase to fail oracle")
	}
	if evaluateF1ResponseOracle(f1Options{}, "unknown_class", "healthy", "healthy", 200, hdr, validDash, nil) {
		t.Fatal("expected unknown class to fail oracle")
	}

	// computeQuantiles empty
	if minV, p50, p95, p99, maxV, avg := computeQuantiles(nil); minV != 0 || p50 != 0 || p95 != 0 || p99 != 0 || maxV != 0 || avg != 0 {
		t.Fatal("expected zero quantiles on empty slice")
	}

	// f1EventWriter marshal error and writeF1SummaryFile directory error
	evPath := filepath.Join(dir, "err-ev.jsonl")
	ew, err := newF1EventWriter(evPath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ew.Write(map[string]any{"unmarshalable": make(chan int)})
	if err := ew.Close(); err == nil {
		t.Fatal("expected Close error after failed Write")
	}
	if err := writeF1SummaryFile(dir, map[string]int{"a": 1}); err == nil {
		t.Fatal("expected writeF1SummaryFile on directory to fail")
	}

	// evaluatePublishSignalTiming branches: default bound, fallback Stat on existing sigFile, and late WS close
	sigFile := filepath.Join(dir, "existing.sig")
	_ = os.WriteFile(sigFile, []byte("ok\n"), 0o600)
	var pMono atomic.Int64
	var pUTC atomic.Value
	res := f1SharedH2Result{
		DashSSEClosedMonoNS: int64(10 * time.Millisecond),
		WSClosedMonoNS:      int64(5000 * time.Millisecond),
	}
	evaluatePublishSignalTiming(sigFile, &pMono, &pUTC, 0, true, &res)
	if res.RetirementWithinBound {
		t.Fatal("expected late WS close to set RetirementWithinBound=false")
	}

	// runF1Mode with 429 response and SummaryPath write error
	srv429 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.html" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<!doctype html><html><body>" + f1PortalMarker + "</body></html>"))
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv429.Close()
	ca429 := filepath.Join(dir, "ca429.pem")
	_ = os.WriteFile(ca429, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv429.Certificate().Raw}), 0o600)
	if got := runF1Mode(f1Options{
		Duration:    60 * time.Millisecond,
		Workers:     1,
		TLSBase:     srv429.URL,
		CAFile:      ca429,
		PortalHost:  "example.com",
		DashHost:    "example.com",
		SummaryPath: dir,
	}); got != 1 {
		t.Fatalf("expected SummaryPath=dir to return 1, got %d", got)
	}

	// runF1SharedH2 with sse missing marker and HTTP1KeepAlive pre-change 500
	srvBadSSE := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/bundle.bin":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Repeat("P", 4096)))
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: wrong-marker\n\n"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	srvBadSSE.EnableHTTP2 = true
	srvBadSSE.StartTLS()
	defer srvBadSSE.Close()
	caBadSSE := filepath.Join(dir, "ca-bad-sse.pem")
	_ = os.WriteFile(caBadSSE, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvBadSSE.Certificate().Raw}), 0o600)
	if got := runF1SharedH2(f1SharedH2Options{TLSBase: srvBadSSE.URL, CAFile: caBadSSE, PortalHost: "example.com", DashHost: "example.com"}); got != 1 {
		t.Fatalf("expected missing SSE marker to return 1, got %d", got)
	}
	if got := runF1SharedH2(f1SharedH2Options{TLSBase: srvBadSSE.URL, CAFile: caBadSSE, PortalHost: "example.com", DashHost: "example.com", HTTP1KeepAlive: true}); got != 1 {
		t.Fatalf("expected H1 keepalive pre-change 500 to return 1, got %d", got)
	}
	if got := runF1SharedH2(f1SharedH2Options{TLSBase: srvBadSSE.URL, CAFile: caBadSSE, EventLogPath: dir}); got != 1 {
		t.Fatalf("expected EventLogPath=dir to return 1, got %d", got)
	}

	// Test timeout waiting for SSE retirement and ReadyFile=dir
	srvHangSSE := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/bundle.bin":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(f1PortalMarker + strings.Repeat("P", 4096)))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: " + f1DashboardMarker + "\n\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
		}
	}))
	srvHangSSE.EnableHTTP2 = true
	srvHangSSE.StartTLS()
	defer srvHangSSE.Close()
	caHang := filepath.Join(dir, "ca-hang.pem")
	_ = os.WriteFile(caHang, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvHangSSE.Certificate().Raw}), 0o600)
	if got := runF1SharedH2(f1SharedH2Options{TLSBase: srvHangSSE.URL, CAFile: caHang, PortalHost: "example.com", DashHost: "example.com", ReadyFile: dir, WithWS: false, Timeout: 80 * time.Millisecond}); got != 1 {
		t.Fatalf("expected ReadyFile=dir to return 1, got %d", got)
	}
	if got := runF1SharedH2(f1SharedH2Options{TLSBase: srvHangSSE.URL, CAFile: caHang, PortalHost: "example.com", DashHost: "example.com", WithWS: false, Timeout: 80 * time.Millisecond}); got != 1 {
		t.Fatalf("expected SSE timeout to return 1, got %d", got)
	}

	// runF1H1KeepAlive failure branches (unreachable dial, premature close before publish)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/summary":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(validDash)
		case "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: " + f1DashboardMarker + "\n\n"))
		case "/index.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<!doctype html><html><body>" + f1PortalMarker + "</body></html>"))
		}
	}))
	defer srv.Close()
	caPath := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	if got := runF1SharedH2(f1SharedH2Options{
		TLSBase:           srv.URL,
		CAFile:            caPath,
		PortalHost:        "example.com",
		DashHost:          "example.com",
		PublishSignalFile: filepath.Join(dir, "never-created.sig"),
		HTTP1KeepAlive:    true,
		Timeout:           200 * time.Millisecond,
	}); got != 1 {
		t.Fatalf("expected H1 keepalive with missing publish signal (premature close) to return 1, got %d", got)
	}
}

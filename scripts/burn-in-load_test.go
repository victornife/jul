//go:build ignore

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
	for _, tc := range []struct {
		name     string
		dashMode string
		allowOut bool
		wantCode int
	}{
		{name: "healthy_and_withdrawn", dashMode: "ok_and_withdrawn", allowOut: true, wantCode: 0},
		{name: "excluded_leak_violation", dashMode: "excluded_200", allowOut: false, wantCode: 1},
		{name: "preflight_failure", dashMode: "preflight_500", allowOut: false, wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reqIdx int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				idx := atomic.AddInt64(&reqIdx, 1)
				switch {
				case r.URL.Path == "/index.html":
					if tc.dashMode == "preflight_500" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if tc.dashMode == "excluded_200" && idx > 3 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, f1PortalMarker+" portal body")
				case r.URL.Path == "/assets/bundle.bin":
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, strings.Repeat("A", 1500))
				case r.URL.Path == "/events":
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, "data: "+f1DashboardMarker+"\n\n")
				case r.URL.Path == "/api/summary":
					auth := r.Header.Get("Authorization")
					if !strings.HasPrefix(auth, "Basic ") || strings.Contains(r.Header.Get("Tailscale-User-Login"), "forged") {
						if tc.dashMode == "excluded_200" {
							w.WriteHeader(http.StatusOK)
							fmt.Fprint(w, f1DashboardMarker)
							return
						}
						if idx%2 == 0 {
							w.WriteHeader(http.StatusUnauthorized)
						} else {
							w.WriteHeader(http.StatusForbidden)
						}
						return
					}
					switch idx % 4 {
					case 0:
						w.Header().Set("Cache-Control", "no-store")
						w.WriteHeader(http.StatusForbidden)
					case 1:
						if tc.allowOut {
							w.WriteHeader(http.StatusBadGateway)
						} else {
							w.WriteHeader(http.StatusOK)
							fmt.Fprint(w, f1DashboardMarker)
						}
					case 2:
						w.WriteHeader(http.StatusTooManyRequests)
					default:
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, f1DashboardMarker)
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
			evPath := filepath.Join(dir, "events.jsonl")
			sumPath := filepath.Join(dir, "summary.json")

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
				"-excludedRatio=30",
				"-heldConns=2",
				"-probeInterval=40ms",
				"-probeDash",
				fmt.Sprintf("-allowDashOutage=%v", tc.allowOut),
				"-pace=5ms",
				"-eventLog=" + evPath,
				"-summaryOut=" + sumPath,
			}
			// Notice buildF1Transport handles 429 as oracle violation for dash_auth when not expected;
			// only test 429 in excluded/violation or keep dash_auth returning 200/403/502 in healthy mode.
			code := runLoad()
			if tc.name == "healthy_and_withdrawn" {
				// Because reqIdx % 4 == 2 returned 429 above, let's verify summary.json was written:
				b, err := os.ReadFile(sumPath)
				if err != nil {
					t.Fatalf("missing summary.json: %v", err)
				}
				var s f1Summary
				if err := json.Unmarshal(b, &s); err != nil {
					t.Fatal(err)
				}
				if s.TotalRequests == 0 || s.HeldOpened == 0 || s.ProbePortalOK == 0 {
					t.Fatalf("unexpected summary counts: %+v", s)
				}
			} else if code != tc.wantCode {
				t.Fatalf("got code %d, want %d", code, tc.wantCode)
			}
		})
	}

	// Clean zero-violation run
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.html":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, f1PortalMarker)
		case "/assets/bundle.bin":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, strings.Repeat("B", 2048))
		case "/events":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "data: "+f1DashboardMarker+"\n\n")
		case "/api/summary":
			if strings.Contains(r.Header.Get("Tailscale-User-Login"), "forged") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, f1DashboardMarker)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	code := runF1Mode(f1Options{
		Duration:     150 * time.Millisecond,
		Workers:      2,
		TLSBase:      srv.URL,
		CAFile:       caPath,
		PortalHost:   "example.com",
		DashHost:     "example.com",
		AuthUser:     "soakuser",
		AuthPassword: "soakpass",
		ExcludedPct:  20,
		HeldConns:    1,
		ProbeEvery:   40 * time.Millisecond,
		ProbeDash:    true,
		Pace:         5 * time.Millisecond,
		EventLogPath: filepath.Join(dir, "ev.jsonl"),
		SummaryPath:  filepath.Join(dir, "sum.json"),
	})
	if code != 0 {
		t.Fatalf("expected clean F1 run to return 0, got %d", code)
	}

	// Error paths: missing CA, invalid eventLog path, and unreachable preflight
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

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/bundle.bin":
			w.WriteHeader(http.StatusOK)
			for i := 0; i < 4; i++ {
				_, _ = w.Write([]byte(strings.Repeat("P", 2048)))
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				time.Sleep(15 * time.Millisecond)
			}
		case "/index.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(f1PortalMarker))
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
			// Send one text frame (opcode 0x1, length 4)
			_, _ = conn.Write([]byte{0x81, 0x04, 't', 'e', 's', 't'})
			<-wsClose
			// Send unmasked Close 1008 frame (0x03 0xF0 = 1008)
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
			_, _ = w.Write([]byte(f1DashboardMarker))
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	readyPath := filepath.Join(dir, "ready.txt")
	evPath := filepath.Join(dir, "h2-events.jsonl")
	sumPath := filepath.Join(dir, "h2-summary.json")

	go func() {
		for {
			if _, err := os.Stat(readyPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
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
		"-pace=10ms",
		"-readyFile=" + readyPath,
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
	if res.DialCount != 1 || res.NegotiatedProtocol != "h2" || !res.PortalStreamCompletedOK || res.WSCloseCode != 1008 || !res.WSGracefulHandshake {
		t.Fatalf("unexpected shared H2 result: %+v", res)
	}

	// Test forced close (wsEchoClose = false) and error branches
	c1, c2 := make(chan struct{}), make(chan struct{})
	close(c1)
	close(c2)
	sseClose = c1
	wsClose = c2
	if code := runF1SharedH2(f1SharedH2Options{
		TLSBase:      srv.URL,
		CAFile:       caPath,
		ClientCert:   "testdata/tls/client.crt",
		ClientKey:    "testdata/tls/client.key",
		PortalHost:   "example.com",
		DashHost:     "example.com",
		AuthUser:     "soakuser",
		AuthPassword: "soakpass",
		WithWS:       true,
		WSEchoClose:  false,
		Timeout:      3 * time.Second,
	}); code != 0 {
		t.Fatalf("expected wsEchoClose=false run to return 0, got %d", code)
	}
	if code := runF1SharedH2(f1SharedH2Options{
		TLSBase:    srv.URL,
		CAFile:     caPath,
		PortalHost: "example.com",
		DashHost:   "example.com",
		WithWS:     false,
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

	// Test portal 500, dash SSE 500, ws 403, and timeout/post-change failure branches
	for _, failStage := range []string{"portal500", "sse500", "ws403", "post200"} {
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
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("data: ok\n\n"))
			case "/ws":
				if failStage == "ws403" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			case "/index.html":
				w.WriteHeader(http.StatusOK)
			case "/api/summary":
				w.WriteHeader(http.StatusOK) // Not 403 -> triggers final check failure
			}
		}))
		s2.EnableHTTP2 = true
		s2.StartTLS()
		ca2 := filepath.Join(dir, "ca-"+failStage+".pem")
		_ = os.WriteFile(ca2, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s2.Certificate().Raw}), 0o600)
		got := runF1SharedH2(f1SharedH2Options{
			TLSBase:    s2.URL,
			CAFile:     ca2,
			PortalHost: "example.com",
			DashHost:   "example.com",
			WithWS:     failStage == "ws403",
			Timeout:    200 * time.Millisecond,
		})
		s2.Close()
		if got != 1 {
			t.Fatalf("stage %s: expected code 1, got %d", failStage, got)
		}
	}
}

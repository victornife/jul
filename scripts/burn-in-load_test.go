//go:build ignore

package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strings"
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

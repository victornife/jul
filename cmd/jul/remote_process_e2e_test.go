// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/admin"
	"jul/internal/adminapi"
	"jul/internal/config"
)

func buildJulProcess(t *testing.T) string {
	t.Helper()
	name := "jul-e2e"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build actual jul binary: %v\n%s", err, out)
	}
	return bin
}

func runJulProcess(t *testing.T, bin string, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if err == nil {
		return 0, stdoutBuf.String(), stderrBuf.String()
	}
	var exitErr *exec.ExitError
	if !strings.Contains(fmt.Sprintf("%T", err), "ExitError") {
		t.Fatalf("execute jul: %v", err)
	}
	if !asExitError(err, &exitErr) {
		t.Fatalf("execute jul: %v", err)
	}
	return exitErr.ExitCode(), stdoutBuf.String(), stderrBuf.String()
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func realAdminHandler(t *testing.T, authority, token string) http.Handler {
	t.Helper()
	raw := []byte("")
	cfg := config.AdminConfig{Enabled: true, Token: token}
	s := admin.New(cfg, nil, admin.Deps{
		Product:       "Jul.IA",
		Version:       "e2e",
		ReadConfigRaw: func() ([]byte, error) { return append([]byte(nil), raw...), nil },
		LoadConfig:    func() (*config.Config, error) { return config.Parse(raw) },
		Authority: func() admin.ConfigAuthorityStatus {
			state := "managed_clean"
			if authority == "file_owned" {
				state = "file_owned_clean"
			}
			return admin.ConfigAuthorityStatus{Mode: authority, Source: "explicit", ConfigState: state}
		},
		BootID: func() string { return "boot-e2e" },
	})
	if s == nil {
		t.Fatal("real admin server construction returned nil")
	}
	return s.Handler()
}

func TestRemoteCLIActualBinaryAgainstRealAdminRouteStack(t *testing.T) {
	bin := buildJulProcess(t)
	candidate := filepath.Join(t.TempDir(), "candidate.toml")
	if err := os.WriteFile(candidate, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	managed := httptest.NewServer(realAdminHandler(t, "managed", "e2e-secret"))
	defer managed.Close()
	env := []string{"JUL_TOKEN=e2e-secret", "JUL_TOKEN_FILE="}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"status", []string{"status", "--endpoint", managed.URL, "--json"}},
		{"plan", []string{"plan", "--endpoint", managed.URL, "--config", candidate, "--json"}},
		{"diagnostics", []string{"diagnostics", "--endpoint", managed.URL, "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runJulProcess(t, bin, env, tc.args...)
			if code != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, errOut)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("machine output is not one JSON object: %q", out)
			}
			if strings.Contains(out+errOut, "e2e-secret") {
				t.Fatal("credential leaked from actual process")
			}
		})
	}

	fileOwned := httptest.NewServer(realAdminHandler(t, "file_owned", "e2e-secret"))
	defer fileOwned.Close()
	code, out, errOut := runJulProcess(t, bin, env,
		"apply", "--endpoint", fileOwned.URL, "--config", candidate,
		"--base-version", "reviewed", "--idempotency-key", "e2e-file-owned-1", "--json")
	if code != 6 {
		t.Fatalf("file_owned mutation exit=%d want=6 stdout=%s stderr=%s", code, out, errOut)
	}
	if !strings.Contains(out, "config_authority_read_only") {
		t.Fatalf("authority denial envelope missing: %s", out)
	}

	code, out, errOut = runJulProcess(t, bin, []string{"JUL_TOKEN=", "JUL_TOKEN_FILE="},
		"status", "--endpoint", managed.URL, "--json")
	if code != 7 {
		t.Fatalf("unauthenticated exit=%d want=7 stdout=%s stderr=%s", code, out, errOut)
	}
}

func TestRemoteCLIActualProcessExitContractZeroThroughNine(t *testing.T) {
	bin := buildJulProcess(t)

	errorCases := []struct {
		name string
		code adminapi.Code
		want int
	}{
		{"validation", adminapi.CodeValidationFailed, 1},
		{"usage_request", adminapi.CodeInvalidRequest, 2},
		{"conflict", adminapi.CodeStaleBaseVersion, 5},
		{"authority", adminapi.CodeConfigAuthorityRO, 6},
		{"auth", adminapi.CodeForbidden, 7},
		{"rate_limit", adminapi.CodeRateLimited, 8},
		{"server", adminapi.CodeInternalError, 9},
	}
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{Code: tc.code, Message: "bounded", RequestID: "REQ"}})
			}))
			defer ts.Close()
			code, out, errOut := runJulProcess(t, bin, nil, "status", "--endpoint", ts.URL, "--json")
			if code != tc.want {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, tc.want, out, errOut)
			}
			if !json.Valid([]byte(out)) || errOut != "" {
				t.Fatalf("machine contract stdout=%q stderr=%q", out, errOut)
			}
		})
	}

	for _, tc := range []struct {
		name     string
		outcome  string
		degraded bool
		want     int
	}{
		{"live", "applied_live", false, 0},
		{"staged", "staged", false, 3},
		{"degraded", "applied_live", true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "candidate.toml")
			if err := os.WriteFile(candidate, []byte("[global]\nworkers = 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, `{"api_version":"v1","boot_id":"boot","drift":{"detected":false},"pending_restart":{"pending":false,"discard_available":false},"ledger_retention":{"policy":"evict_after_both"}}`)
			})
			mux.HandleFunc("/api/v1/config/apply", func(w http.ResponseWriter, _ *http.Request) {
				degraded := "[]"
				if tc.degraded {
					degraded = `[{"kind":"baseline_error","message":"bounded"}]`
				}
				fmt.Fprintf(w, `{"api_version":"v1","apply_id":"a","state":"terminal","terminal":true,"ok":true,"outcome":%q,"degraded":%s,"boot_id":"boot"}`, tc.outcome, degraded)
			})
			ts := httptest.NewServer(mux)
			defer ts.Close()
			command := "apply"
			if tc.outcome == "staged" {
				command = "stage"
			}
			code, out, errOut := runJulProcess(t, bin, nil, command, "--endpoint", ts.URL,
				"--config", candidate, "--base-version", "base", "--idempotency-key", "process-exit-key-1", "--json")
			if code != tc.want {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, tc.want, out, errOut)
			}
		})
	}
}

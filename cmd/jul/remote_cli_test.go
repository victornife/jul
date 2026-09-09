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
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/adminapi"
)

func writeRemoteCandidate(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "candidate.toml")
	if err := os.WriteFile(p, []byte("[global]\nworkers = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func withRemoteOutput(t *testing.T, fn func(out, errOut *bytes.Buffer)) {
	t.Helper()
	oldOut, oldErr := stdout, stderr
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	stdout, stderr = out, errOut
	defer func() { stdout, stderr = oldOut, oldErr }()
	fn(out, errOut)
}

func statusJSON(authority, state string) string {
	return fmt.Sprintf(`{"api_version":"v1","config_authority":%q,"config_authority_source":"explicit","config_state":%q,"drift":{"detected":false},"pending_restart":{"pending":false,"discard_available":false},"boot_id":"boot","ledger_retention":{"min_terminal_records":512,"min_age_seconds":3600,"policy":"evict_after_both"}}`, authority, state)
}

func TestRemoteDispatchSurface(t *testing.T) {
	for _, cmd := range []string{"plan", "diff", "apply", "stage", "status", "rollback", "export", "diagnostics"} {
		handled, _ := dispatchRemoteSubcommand([]string{cmd, "--endpoint", "http://127.0.0.1:1"})
		if !handled {
			t.Fatalf("%s not handled", cmd)
		}
	}
	if handled, _ := dispatchRemoteSubcommand([]string{"doctor"}); handled {
		t.Fatal("local doctor captured by remote dispatcher")
	}
}

func TestRemotePlanAndDiffJSON(t *testing.T) {
	candidate := writeRemoteCandidate(t)
	expected, _ := os.ReadFile(candidate)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/plan", func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, expected) {
			t.Fatal("candidate bytes changed")
		}
		io.WriteString(w, `{"ok":true,"base_version":"base","valid":true,"validation_errors":[],"lint":[],"diff":{"changes":[]},"lifecycle":{}}`)
	})
	mux.HandleFunc("/api/v1/config/history/h1/diff", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"base_version":"base","changes":[]}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, args := range [][]string{
		{"--endpoint", ts.URL, "--config", candidate, "--json"},
		{"--endpoint", ts.URL, "--history-id", "h1", "--json"},
	} {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			var code int
			if strings.Contains(strings.Join(args, " "), "history-id") {
				code = cmdRemoteDiff(args)
			} else {
				code = cmdRemotePlan(args)
			}
			if code != 0 || errOut.Len() != 0 || !json.Valid(out.Bytes()) {
				t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
			}
		})
	}
}

func TestRemoteStatusAndApplyLookup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/applies/a1", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"api_version":"v1","apply_id":"a1","state":"terminal","terminal":true,"outcome":"applied_live","ok":true,"restored":false,"degraded":[],"boot_id":"boot"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteStatus([]string{"--endpoint", ts.URL}); code != 0 || !strings.Contains(out.String(), "managed_clean") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteStatus([]string{"--endpoint", ts.URL, "--apply-id", "a1", "--json"}); code != 0 || !strings.Contains(out.String(), `"apply_id":"a1"`) {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func mutationServer(t *testing.T, outcome string, degraded bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		deg := "[]"
		if degraded {
			deg = `[{"kind":"baseline_error","message":"bounded"}]`
		}
		fmt.Fprintf(w, `{"api_version":"v1","apply_id":"a1","state":"terminal","terminal":true,"ok":true,"outcome":%q,"restored":false,"degraded":%s,"boot_id":"boot"}`, outcome, deg)
	})
	return httptest.NewServer(mux)
}

func TestApplyStageAndDegradedExitCodes(t *testing.T) {
	candidate := writeRemoteCandidate(t)
	cases := []struct {
		name, outcome string
		cmd           func([]string) int
		want          int
	}{
		{"live", "applied_live", cmdRemoteApply, 0},
		{"stage", "staged", cmdRemoteStage, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := mutationServer(t, tc.outcome, false)
			defer ts.Close()
			withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
				code := tc.cmd([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "base", "--idempotency-key", "stable-key-123", "--json"})
				if code != tc.want || !json.Valid(out.Bytes()) {
					t.Fatalf("code=%d want=%d out=%s err=%s", code, tc.want, out, errOut)
				}
			})
		})
	}
	ts := mutationServer(t, "applied_live", true)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteApply([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "base", "--idempotency-key", "stable-key-456", "--json"})
		if code != 4 {
			t.Fatalf("degraded exit=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func TestApplyPollsSavedNotLiveUntilTerminal(t *testing.T) {
	candidate := writeRemoteCandidate(t)
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/apply", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"api_version":"v1","apply_id":"a1","state":"finalizing","terminal":false,"outcome":"saved_not_live","degraded":[],"boot_id":"boot"}`)
	})
	mux.HandleFunc("/api/v1/config/applies/a1", func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls == 1 {
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"api_version":"v1","apply_id":"a1","state":"finalizing","terminal":false,"outcome":"saved_not_live","degraded":[],"boot_id":"boot"}`)
			return
		}
		io.WriteString(w, `{"api_version":"v1","apply_id":"a1","state":"terminal","terminal":true,"outcome":"applied_live","ok":true,"degraded":[],"boot_id":"boot"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteApply([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "base", "--idempotency-key", "stable-key-789", "--poll-timeout", "2s", "--json"})
		if code != 0 || polls < 2 {
			t.Fatalf("code=%d polls=%d out=%s err=%s", code, polls, out, errOut)
		}
	})
}

func TestRemoteAPIErrorExitClassesAndJSON(t *testing.T) {
	cases := []struct {
		code adminapi.Code
		want int
	}{
		{adminapi.CodeValidationFailed, 1},
		{adminapi.CodeInvalidRequest, 2},
		{adminapi.CodeStaleBaseVersion, 5},
		{adminapi.CodeConfigAuthorityRO, 6},
		{adminapi.CodeForbidden, 7},
		{adminapi.CodeRateLimited, 8},
		{adminapi.CodeInternalError, 9},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{Code: tc.code, Message: "bounded", RequestID: "REQ"}})
			}))
			defer ts.Close()
			withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
				code := cmdRemoteStatus([]string{"--endpoint", ts.URL, "--json"})
				if code != tc.want || errOut.Len() != 0 || !json.Valid(out.Bytes()) {
					t.Fatalf("code=%d want=%d out=%s err=%s", code, tc.want, out, errOut)
				}
			})
		})
	}
}

func TestTokenSentinelNeverAppearsInCLIOutput(t *testing.T) {
	const secret = "SENTINEL_TOKEN_SHOULD_NEVER_APPEAR"
	t.Setenv("JUL_TOKEN", secret)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("token not injected")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{Code: adminapi.CodeUnauthenticated, Message: "credential rejected", RequestID: "REQ"}})
	}))
	defer ts.Close()
	for _, jsonMode := range []bool{false, true} {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			args := []string{"--endpoint", ts.URL}
			if jsonMode {
				args = append(args, "--json")
			}
			_ = cmdRemoteStatus(args)
			if strings.Contains(out.String()+errOut.String(), secret) {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestExportDiagnosticsRollbackAndAdoption(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/export", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"api_version":"v1","config":{"admin":{"token":"[redacted]"}}}`)
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"api_version":"v1","config_schema_version":1,"endpoints":[],"exit_codes":[],"boot_id":"boot","ledger_retention":{"policy":"evict_after_both"}}`)
	})
	mux.HandleFunc("/api/v1/config/history/h1/diff", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"base_version":"base","changes":[]}`)
	})
	mux.HandleFunc("/api/v1/config/rollback", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["id"] != "h1" || body["base_version"] != "base" {
			t.Errorf("rollback body=%v", body)
		}
		io.WriteString(w, `{"api_version":"v1","apply_id":"r1","state":"terminal","terminal":true,"ok":true,"outcome":"applied_live","degraded":[],"boot_id":"boot"}`)
	})
	mux.HandleFunc("/api/v1/config/adopt-external/preview", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"origin":"drift","observed_digest":"deadbeef","base_version":"base","restart_required":false}`)
	})
	mux.HandleFunc("/api/v1/config/adopt-external", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["observed_digest"] != "deadbeef" || body["confirm"] != true {
			t.Errorf("adopt body=%v", body)
		}
		io.WriteString(w, `{"api_version":"v1","apply_id":"ad1","state":"terminal","terminal":true,"ok":true,"outcome":"applied_live","degraded":[],"boot_id":"boot"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	commands := []func() int{
		func() int { return cmdRemoteExport([]string{"--endpoint", ts.URL, "--json"}) },
		func() int { return cmdRemoteDiagnostics([]string{"--endpoint", ts.URL, "--json"}) },
		func() int {
			return cmdRemoteRollback([]string{"--endpoint", ts.URL, "--history-id", "h1", "--idempotency-key", "rollback-key-1", "--json"})
		},
		func() int {
			return cmdRemoteApply([]string{"--endpoint", ts.URL, "--adopt-external", "--base-version", "base", "--idempotency-key", "adoption-key-1", "--json"})
		},
	}
	for _, command := range commands {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			if code := command(); code != 0 || errOut.Len() != 0 || !json.Valid(out.Bytes()) {
				t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
			}
		})
	}
}

func TestRemoteUsageFailuresAreMachineReadable(t *testing.T) {
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteApply([]string{"--endpoint", "http://127.0.0.1:1", "--json"}); code != 2 || !json.Valid(out.Bytes()) {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteStatus([]string{"--endpoint", "http://example.com", "--json"}); code != 2 || !json.Valid(out.Bytes()) {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

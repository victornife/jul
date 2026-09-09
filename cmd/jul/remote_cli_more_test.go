// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/adminclient"
)

func TestRemoteDispatchEmptyAndCommonClientError(t *testing.T) {
	if handled, code := dispatchRemoteSubcommand(nil); handled || code != 0 {
		t.Fatalf("handled=%t code=%d", handled, code)
	}
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteStatus([]string{"--json"}); code != 2 || !json.Valid(out.Bytes()) {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func TestParseRemoteFlagFailureAndPermissionWarning(t *testing.T) {
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		var c remoteCommon
		_, code, ok := parseRemote(fs, []string{"--no-such-flag"}, &c)
		if ok || code != 2 || errOut.Len() == 0 {
			t.Fatalf("ok=%t code=%d err=%s", ok, code, errOut)
		}
	})
	if os.PathSeparator == '/' {
		token := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(token, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			fs := flag.NewFlagSet("x", flag.ContinueOnError)
			var c remoteCommon
			client, code, ok := parseRemote(fs, []string{"--endpoint", "https://example.com", "--token-file", token}, &c)
			if !ok || code != 0 || client == nil || !strings.Contains(errOut.String(), "owner-only permissions") {
				t.Fatalf("ok=%t code=%d err=%s", ok, code, errOut)
			}
		})
	}
}

func TestReadCandidateBranches(t *testing.T) {
	if _, err := readCandidate(""); err == nil {
		t.Fatal("missing config accepted")
	}
	if _, err := readCandidate(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("stdin-candidate")
	_ = w.Close()
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()
	b, err := readCandidate("-")
	if err != nil || string(b) != "stdin-candidate" {
		t.Fatalf("b=%q err=%v", b, err)
	}
}

func TestCommandUsageBranches(t *testing.T) {
	endpoint := "http://127.0.0.1:1"
	cases := []func() int{
		func() int { return cmdRemotePlan([]string{"--endpoint", endpoint, "extra", "--json"}) },
		func() int { return cmdRemotePlan([]string{"--endpoint", endpoint, "--json"}) },
		func() int {
			return cmdRemoteDiff([]string{"--endpoint", endpoint, "--config", "a", "--history-id", "h", "--json"})
		},
		func() int { return cmdRemoteStatus([]string{"--endpoint", endpoint, "extra", "--json"}) },
		func() int { return cmdRemoteApply([]string{"--endpoint", endpoint, "--poll-timeout", "0s", "--json"}) },
		func() int { return cmdRemoteStage([]string{"--endpoint", endpoint, "--adopt-external", "--json"}) },
		func() int {
			return cmdRemoteApply([]string{"--endpoint", endpoint, "--adopt-external", "--config", "x", "--json"})
		},
		func() int { return cmdRemoteApply([]string{"--endpoint", endpoint, "--json"}) },
		func() int { return cmdRemoteRollback([]string{"--endpoint", endpoint, "--json"}) },
		func() int { return cmdRemoteExport([]string{"--endpoint", endpoint, "extra", "--json"}) },
		func() int { return cmdRemoteDiagnostics([]string{"--endpoint", endpoint, "extra", "--json"}) },
	}
	for i, fn := range cases {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			if code := fn(); code != 2 {
				t.Fatalf("case %d code=%d out=%s err=%s", i, code, out, errOut)
			}
		})
	}
}

func TestPlanDiffAndStatusHumanErrorBranches(t *testing.T) {
	candidate := writeRemoteCandidate(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/plan", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("base_version") == "error" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{Code: adminapi.CodeValidationFailed, Message: "bad", RequestID: "R1"}})
			return
		}
		w.Header().Set("X-Request-ID", "REQ")
		io.WriteString(w, `{"ok":true,"base_version":"b","valid":true,"validation_errors":[],"lint":[],"diff":{"changes":[]},"lifecycle":{}}`)
	})
	mux.HandleFunc("/api/v1/config/history/h", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","config_authority":"managed","config_authority_source":"explicit","config_state":"managed_clean","serving_version":"s","persisted_version":"p","drift":{"detected":false},"pending_restart":{"pending":false,"discard_available":false},"boot_id":"boot","last_apply":{"apply_id":"last","state":"terminal","outcome":"applied_live","terminal":true,"ok":true,"degraded":[]},"ledger_retention":{"policy":"evict_after_both"}}`)
	})
	mux.HandleFunc("/api/v1/config/applies/a", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","apply_id":"a","state":"terminal","terminal":true,"outcome":"staged","serving_version":"s","persisted_version":"p","degraded":[{"kind":"baseline_error"}],"boot_id":"boot"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemotePlan([]string{"--endpoint", ts.URL, "--config", candidate, "--verbose"})
		if code != 0 || !strings.Contains(out.String(), "candidate assessed") || !strings.Contains(errOut.String(), "request_id=REQ") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemotePlan([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "error"})
		if code != 1 || !strings.Contains(errOut.String(), "validation_failed") || !strings.Contains(errOut.String(), "request_id: R1") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteStatus([]string{"--endpoint", ts.URL})
		if code != 0 || !strings.Contains(out.String(), "last apply: last") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteStatus([]string{"--endpoint", ts.URL, "--apply-id", "a"})
		if code != 0 || !strings.Contains(out.String(), "degraded: 1") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func TestAdoptionFailureBranches(t *testing.T) {
	if code := func() int {
		var b bytes.Buffer
		oldOut, oldErr := stdout, stderr
		stdout, stderr = &b, &b
		defer func() { stdout, stderr = oldOut, oldErr }()
		c, _ := adminclient.New(adminclient.Config{Endpoint: "http://127.0.0.1:1"})
		return runAdoption(c, remoteCommon{json: true}, "b", "stable-key-1", "bad", time.Second)
	}(); code != 2 {
		t.Fatalf("bad mode=%d", code)
	}

	responses := []string{"not-json", `{"ok":true}`, `{"ok":true,"observed_digest":"d","base_version":"different"}`}
	for i, body := range responses {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			code := cmdRemoteApply([]string{"--endpoint", ts.URL, "--adopt-external", "--base-version", "base", "--idempotency-key", "adopt-key-2", "--json"})
			if code == 0 {
				t.Fatalf("case %d unexpectedly succeeded: %s", i, out)
			}
		})
		ts.Close()
	}
}

func TestRollbackFailureAndHumanPreview(t *testing.T) {
	for i, body := range []string{"not-json", `{"changes":[]}`, `{"base_version":"server-base","changes":[]}`} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/config/history/h/diff", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		ts := httptest.NewServer(mux)
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			args := []string{"--endpoint", ts.URL, "--history-id", "h", "--json"}
			if i == 2 {
				args = append(args, "--base-version", "other")
			}
			if code := cmdRemoteRollback(args); code == 0 {
				t.Fatalf("case %d succeeded", i)
			}
		})
		ts.Close()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/history/h/diff", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"base_version":"base","changes":[]}`)
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/rollback", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","apply_id":"r","state":"terminal","terminal":true,"outcome":"staged","degraded":[],"boot_id":"boot"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteRollback([]string{"--endpoint", ts.URL, "--history-id", "h", "--idempotency-key", "rollback-key-2"})
		if code != 3 || !strings.Contains(out.String(), "rollback preview") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func TestMutationFailureAndWaitBranches(t *testing.T) {
	candidate := writeRemoteCandidate(t)
	// Status failure before submit.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{Code: adminapi.CodeInternalError, Message: "down"}})
	}))
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteApply([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "b", "--idempotency-key", "wait-key-1", "--json"})
		if code != 9 {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	ts.Close()

	// Non-terminal admission without apply_id is malformed.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		io.WriteString(w, `{"api_version":"v1","state":"pending","terminal":false,"degraded":[],"boot_id":"boot"}`)
	})
	ts = httptest.NewServer(mux)
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteApply([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "b", "--idempotency-key", "wait-key-2", "--json"})
		if code != 9 {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
	ts.Close()

	// Poll timeout preserves apply id and human guidance.
	mux = http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/apply", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		io.WriteString(w, `{"api_version":"v1","apply_id":"a-timeout","state":"pending","terminal":false,"degraded":[],"boot_id":"boot"}`)
	})
	mux.HandleFunc("/api/v1/config/applies/a-timeout", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		io.WriteString(w, `{"api_version":"v1","apply_id":"a-timeout","state":"pending","terminal":false,"degraded":[],"boot_id":"boot"}`)
	})
	ts = httptest.NewServer(mux)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteApply([]string{"--endpoint", ts.URL, "--config", candidate, "--base-version", "b", "--idempotency-key", "wait-key-3", "--poll-timeout", "1ms"})
		if code != 8 || !strings.Contains(errOut.String(), "server transaction was not cancelled") || !strings.Contains(errOut.String(), "a-timeout") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func TestExportDiagnosticsHumanAndErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/export", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"redacted":true}`) })
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","config_schema_version":1,"endpoints":[{"method":"GET","path":"/api/v1/status"}],"exit_codes":[],"boot_id":"boot","ledger_retention":{"policy":"evict_after_both"}}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteExport([]string{"--endpoint", ts.URL}); code != 0 || !strings.HasSuffix(out.String(), "\n") {
			t.Fatalf("code=%d out=%q err=%s", code, out.String(), errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteDiagnostics([]string{"--endpoint", ts.URL}); code != 0 || !strings.Contains(out.String(), "reduced scope") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

func TestRenderHelpersCoverAllLocalShapes(t *testing.T) {
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := renderTerminal(remoteCommon{verbose: true}, "stage", "staged", false, "", nil, "a", "k", "boot", nil); code != 3 || !strings.Contains(out.String(), "restart/convergence") || !strings.Contains(errOut.String(), "boot_id=boot") {
			t.Fatalf("stage code=%d out=%s err=%s", code, out, errOut)
		}
		out.Reset()
		errOut.Reset()
		if code := renderTerminal(remoteCommon{}, "apply", "applied_live", false, "", []adminapi.Degradation{{Kind: "baseline_error"}}, "a", "k", "", nil); code != 4 || !strings.Contains(out.String(), "degraded: 1") {
			t.Fatalf("deg code=%d out=%s", code, out)
		}
		out.Reset()
		errOut.Reset()
		if code := renderTerminal(remoteCommon{}, "apply", "future", false, "", nil, "", "", "", nil); code != 9 {
			t.Fatalf("future=%d", code)
		}
		out.Reset()
		errOut.Reset()
		r := adminclient.RawResponse{RequestID: "R", Body: []byte(`{"ok":true}`)}
		if code := renderRawSuccess(remoteCommon{verbose: true}, "plan", r, "summary"); code != 0 || !strings.Contains(out.String(), "summary") || !strings.Contains(errOut.String(), "request_id=R") {
			t.Fatalf("raw code=%d out=%s err=%s", code, out, errOut)
		}
		out.Reset()
		errOut.Reset()
		_ = renderUsageError(false, "x", "bad")
		if !strings.Contains(errOut.String(), "error: bad") {
			t.Fatalf("usage=%s", errOut)
		}
	})

	api := &adminclient.APIError{Envelope: adminapi.Envelope{Error: adminapi.Body{Code: adminapi.CodeForbidden, Message: "denied", RequestID: "REQ"}}}
	boot := &adminclient.BootChangedError{Previous: "a", Current: "b", ApplyID: "id"}
	transport := &adminclient.TransportError{Phase: "connect", Err: errors.New("down")}
	decode := &adminclient.TransportError{Phase: "decode", Err: errors.New("bad")}
	if localErrorExit(api) != 7 || localErrorExit(boot) != 5 || localErrorExit(transport) != 8 || localErrorExit(decode) != 9 || localErrorExit(errors.New("usage")) != 2 {
		t.Fatal("local exit mapping")
	}
	for _, err := range []error{api, boot, transport, errors.New("usage")} {
		if localErrorObject(err, "REQ") == nil {
			t.Fatal("nil local object")
		}
	}
	if emptyDash("") != "-" || emptyDash("x") != "x" {
		t.Fatal("emptyDash")
	}

	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := renderRemoteError(false, "x", api, ""); code != 7 || !strings.Contains(errOut.String(), "request_id: REQ") {
			t.Fatalf("code=%d err=%s", code, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := renderRemoteError(false, "x", errors.New("plain"), ""); code != 2 || !strings.Contains(errOut.String(), "plain") {
			t.Fatalf("code=%d err=%s", code, errOut)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := renderMutationWaitError(true, "apply", transport, "a", "k", "R"); code != 8 || !json.Valid(out.Bytes()) {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := renderMutationWaitError(false, "apply", transport, "a", "k", "R"); code != 8 || !strings.Contains(errOut.String(), "connect") {
			t.Fatalf("code=%d err=%s", code, errOut)
		}
	})
}

func TestInvalidFlagJSONHasNoStderrNoise(t *testing.T) {
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteStatus([]string{"--json", "--definitely-invalid"})
		if code != 2 || errOut.Len() != 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

func TestRemoteExtendedUsage(t *testing.T) {
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		remoteExtendedUsage()
		if !strings.Contains(errOut.String(), "Remote automation") || !strings.Contains(errOut.String(), "successful degraded outcome") {
			t.Fatalf("usage=%s", errOut)
		}
	})
}

func TestExecuteMutationVerboseSuccessDirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, statusJSON("managed", "managed_clean"))
	})
	mux.HandleFunc("/api/v1/config/apply", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"api_version":"v1","apply_id":"a","state":"terminal","terminal":true,"outcome":"applied_live","degraded":[],"boot_id":"boot"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c, _ := adminclient.New(adminclient.Config{Endpoint: ts.URL})
	p, _ := c.PrepareApply([]byte("x"), "b", "hot", "direct-key-1", false)
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := executeMutationWithContext(context.Background(), c, remoteCommon{verbose: true}, "apply", p, time.Second)
		if code != 0 || !strings.Contains(errOut.String(), "phase=submit") {
			t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
		}
	})
}

var _ = fmt.Sprintf

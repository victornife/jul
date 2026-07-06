// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/handler"
)

// capture swaps the package output streams for buffers, runs fn, and returns the
// captured stdout and stderr. It restores the originals afterward.
func capture(t *testing.T, fn func() int) (code int, out, errOut string) {
	t.Helper()
	oldOut, oldErr := stdout, stderr
	var bo, be bytes.Buffer
	stdout, stderr = &bo, &be
	defer func() { stdout, stderr = oldOut, oldErr }()
	code = fn()
	return code, bo.String(), be.String()
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

const validConfig = `[compression]
enabled = true

[[servers]]
listen = ":8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv"
`

const warnConfig = `[[servers]]
listen = ":8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv"
`

const invalidConfig = `[compression]
enabled = true

[[servers]]
listen = ":8080"
  [[servers.locations]]
  root = "/srv"
`

// runtimePreflightConfig is structurally valid but references a missing htpasswd
// file so validateRuntimeConfig fails during the auth dry-run.
const runtimePreflightConfig = `[[servers]]
listen = ":8080"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv"
  auth = { basic = { file = "/nonexistent/htpasswd.txt" } }
`

func TestDispatchSubcommand(t *testing.T) {
	cases := map[string]bool{
		"lint": true, "fmt": true, "run": true, "import": true,
		"-config": false, "serve": false, "": false,
	}
	for arg, wantHandled := range cases {
		var args []string
		if arg != "" {
			args = []string{arg, "-h"}
		}
		// Only check the routing decision, not execution, for recognized verbs by
		// using an unknown flag that makes each handler return quickly.
		if arg == "lint" || arg == "fmt" || arg == "run" || arg == "import" {
			capture(t, func() int {
				handled, _ := dispatchSubcommand(args)
				if !handled {
					t.Errorf("dispatchSubcommand(%q) handled = false, want true", arg)
				}
				return 0
			})
			continue
		}
		handled, _ := dispatchSubcommand(args)
		if handled != wantHandled {
			t.Errorf("dispatchSubcommand(%q) handled = %v, want %v", arg, handled, wantHandled)
		}
	}
}

func TestCmdLintValid(t *testing.T) {
	path := writeTemp(t, validConfig)
	code, out, _ := capture(t, func() int { return cmdLint([]string{"-config", path}) })
	if code != 0 {
		t.Errorf("exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "is valid") {
		t.Errorf("expected 'is valid' in output:\n%s", out)
	}
}

func TestCmdLintInvalid(t *testing.T) {
	path := writeTemp(t, "")
	code, out, _ := capture(t, func() int { return cmdLint([]string{"-config", path}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("expected an error in output:\n%s", out)
	}
}

func TestCmdLintStrictWarnings(t *testing.T) {
	path := writeTemp(t, warnConfig)
	code, out, _ := capture(t, func() int { return cmdLint([]string{"-config", path, "-strict"}) })
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (warnings under -strict)\n%s", code, out)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("expected a warning in output:\n%s", out)
	}
	// Without -strict the same config passes.
	code2, _, _ := capture(t, func() int { return cmdLint([]string{"-config", path}) })
	if code2 != 0 {
		t.Errorf("non-strict exit code = %d, want 0", code2)
	}
}

func TestCmdLintParseError(t *testing.T) {
	path := writeTemp(t, "servers = [\n")
	code, _, errOut := capture(t, func() int { return cmdLint([]string{"-config", path}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "line ") {
		t.Errorf("expected an annotated parse error with a line reference:\n%s", errOut)
	}
}

// TestCmdLintJSONSchema pins the `jul lint -json` contract: lowercase field
// names and a string severity ("warning"/"error"), never an enum ordinal, so
// automation can rely on a stable shape (Finding UX-1).
func TestCmdLintJSONSchema(t *testing.T) {
	path := writeTemp(t, warnConfig)
	code, out, _ := capture(t, func() int { return cmdLint([]string{"-config", path, "-json"}) })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (warnings are not errors)\n%s", code, out)
	}

	// Raw-string assertions guard against regressions in key casing / severity type.
	if !strings.Contains(out, `"severity":"warning"`) {
		t.Errorf("expected string severity %q in JSON:\n%s", `"severity":"warning"`, out)
	}
	if strings.Contains(out, `"Severity"`) || strings.Contains(out, `"severity":0`) || strings.Contains(out, `"severity":1`) {
		t.Errorf("JSON must not use uppercase keys or a numeric severity:\n%s", out)
	}

	// Decode into a mirror of the documented schema and assert the shape.
	var got struct {
		Source   string   `json:"source"`
		Errors   []string `json:"errors"`
		Warnings []struct {
			Severity string `json:"severity"`
			Field    string `json:"field"`
			Message  string `json:"message"`
			Hint     string `json:"hint"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if got.Source == "" {
		t.Errorf("expected a non-empty source field:\n%s", out)
	}
	if len(got.Warnings) == 0 {
		t.Fatalf("expected at least one warning:\n%s", out)
	}
	for i, w := range got.Warnings {
		if w.Severity != "warning" && w.Severity != "error" {
			t.Errorf("warnings[%d].severity = %q, want %q or %q", i, w.Severity, "warning", "error")
		}
		if w.Message == "" {
			t.Errorf("warnings[%d].message is empty", i)
		}
	}
}

func TestCmdFmtStdout(t *testing.T) {
	path := writeTemp(t, validConfig)
	code, out, _ := capture(t, func() int { return cmdFmt([]string{"-config", path}) })
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "[[servers]]") {
		t.Errorf("expected canonical TOML on stdout:\n%s", out)
	}
	if _, err := config.Parse([]byte(out)); err != nil {
		t.Errorf("formatted output does not parse: %v", err)
	}
	// The source file is untouched without -w.
	orig, _ := os.ReadFile(path)
	if string(orig) != validConfig {
		t.Error("cmd fmt without -w must not modify the file")
	}
}

// TestCmdFmtOmitsReservedAndEmptyTables pins the UX-2 fix: a minimal static
// config declares no upstreams, streams, or plugins, so canonical `jul fmt`
// output must not surface any of them.
func TestCmdFmtOmitsReservedAndEmptyTables(t *testing.T) {
	path := writeTemp(t, validConfig)
	code, out, _ := capture(t, func() int { return cmdFmt([]string{"-config", path}) })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out)
	}
	for _, banned := range []string{"upstreams = []", "stream = []", "[plugins]"} {
		if strings.Contains(out, banned) {
			t.Errorf("canonical fmt output must not contain %q:\n%s", banned, out)
		}
	}
	if _, err := config.Parse([]byte(out)); err != nil {
		t.Errorf("formatted output does not parse: %v", err)
	}
}

func TestCmdFmtWriteIdempotent(t *testing.T) {
	path := writeTemp(t, validConfig)
	if code, _, errOut := capture(t, func() int { return cmdFmt([]string{"-config", path, "-w"}) }); code != 0 {
		t.Fatalf("first fmt -w exit = %d: %s", code, errOut)
	}
	after1, _ := os.ReadFile(path)
	if code, _, errOut := capture(t, func() int { return cmdFmt([]string{"-config", path, "-w"}) }); code != 0 {
		t.Fatalf("second fmt -w exit = %d: %s", code, errOut)
	}
	after2, _ := os.ReadFile(path)
	if !bytes.Equal(after1, after2) {
		t.Errorf("fmt -w not idempotent:\n--- first ---\n%s\n--- second ---\n%s", after1, after2)
	}
}

func TestCmdRunRequiresTarget(t *testing.T) {
	if code, _, _ := capture(t, func() int { return cmdRun(nil) }); code != 2 {
		t.Errorf("cmdRun() with no target exit = %d, want 2", code)
	}
	if code, _, errOut := capture(t, func() int { return cmdRun([]string{"--serve", "x", "--proxy", "y"}) }); code != 2 {
		t.Errorf("cmdRun() with both flags exit = %d, want 2: %s", code, errOut)
	}
}

func TestZeroConfigServesDirectory(t *testing.T) {
	// The static handler keeps an os.Root directory handle open for its whole
	// lifetime (path-traversal protection) and exposes no Close, so on Windows
	// the directory cannot be removed while the handler exists. Use a manual
	// temp dir with best-effort cleanup rather than t.TempDir (whose cleanup
	// would fail on the still-open handle).
	dir, err := os.MkdirTemp("", "zeroconf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir) // best-effort; handle releases at process exit
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>zero-config</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.ServeDir(dir, ":8080")
	h, err := handler.NewStaticWithOptions(cfg.Servers[0], cfg.Servers[0].Locations[0], handler.StaticOptions{})
	if err != nil {
		t.Fatalf("build static handler: %v", err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "zero-config") {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestCmdCheckInvalidNoJson(t *testing.T) {
	path := writeTemp(t, invalidConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if out != "" {
		t.Errorf("expected no stdout output, got: %q", out)
	}
	if !strings.Contains(errOut, "error") && !strings.Contains(errOut, "Error") {
		t.Errorf("expected error text in stderr, got: %q", errOut)
	}
}

func TestCmdCheckInvalidJson(t *testing.T) {
	path := writeTemp(t, invalidConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path, "-json"}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if errOut != "" {
		t.Errorf("expected no stderr output in json mode, got: %q", errOut)
	}
	if !strings.Contains(out, `"ok":false`) {
		t.Errorf("expected ok=false in JSON output: %s", out)
	}
	if !strings.Contains(out, `"errors":`) {
		t.Errorf("expected errors array in JSON output: %s", out)
	}
}

func TestCmdCheckValid(t *testing.T) {
	path := writeTemp(t, validConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path}) })
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "is valid (structural + runtime)") {
		t.Errorf("expected validation success message, got stdout: %q", out)
	}
	if errOut != "" {
		t.Errorf("expected no stderr, got: %q", errOut)
	}
}

func TestCmdCheckInvalidNoJSON(t *testing.T) {
	path := writeTemp(t, invalidConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if out != "" {
		t.Errorf("expected no stdout output, got: %q", out)
	}
	if !strings.Contains(errOut, "error") && !strings.Contains(errOut, "Error") {
		t.Errorf("expected error text in stderr, got: %q", errOut)
	}
}

func TestCmdCheckInvalidJSON(t *testing.T) {
	path := writeTemp(t, invalidConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path, "-json"}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if errOut != "" {
		t.Errorf("expected no stderr output in json mode, got: %q", errOut)
	}
	if !strings.Contains(out, `"ok":false`) {
		t.Errorf("expected ok=false in JSON output: %s", out)
	}
	if !strings.Contains(out, `"errors":`) {
		t.Errorf("expected errors array in JSON output: %s", out)
	}
}

func TestCmdCheckValidQuiet(t *testing.T) {
	path := writeTemp(t, validConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path, "-quiet"}) })
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if out != "" {
		t.Errorf("expected no output with -quiet, got stdout: %q", out)
	}
	if errOut != "" {
		t.Errorf("expected no stderr with -quiet, got: %q", errOut)
	}
}

func TestCmdCheckRuntimePreflightFailure(t *testing.T) {
	path := writeTemp(t, runtimePreflightConfig)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path}) })
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (runtime preflight failure)\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "basic auth") {
		t.Errorf("expected stderr to mention basic auth error; got:\n%s", errOut)
	}
}

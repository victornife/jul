// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
)

func TestRemoteCachePurgeCLIFlowsAndDeployScript(t *testing.T) {
	diskDir := t.TempDir()
	stripTags := true
	cacheCfg := config.CacheConfig{
		Enabled:         true,
		MemoryMaxSize:   config.Size(2 << 20),
		DiskPath:        diskDir,
		DiskMaxSize:     config.Size(8 << 20),
		DefaultTTL:      config.Duration(5 * time.Minute),
		TagHeaders:      []string{"Cache-Tag"},
		StripTagHeaders: &stripTags,
	}
	c, err := cache.New(cacheCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	origin := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Cache-Tag", "article:42, release:2026-10-25")
		_, _ = io.WriteString(w, "cached-content")
	}))

	for _, uri := range []string{"/articles/42", "/articles/42/comments", "/static/app.js"} {
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+uri, nil)
		req.Host = "portal.example.com"
		origin.ServeHTTP(httptest.NewRecorder(), req)
	}

	const adminToken = "cli-cache-purge-token-32-chars-621"
	srv := admin.New(config.AdminConfig{
		Enabled: true,
		Token:   adminToken,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), admin.Deps{
		Cache: c,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{Cache: cacheCfg}, nil
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	tokenFile := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(tokenFile, []byte(adminToken+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	// 1. Verify dispatchRemoteSubcommand routes `cache purge`.
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		handled, code := dispatchRemoteSubcommand([]string{
			"cache", "purge",
			"--endpoint", ts.URL,
			"--token-file", tokenFile,
			"--host", "portal.example.com",
			"--tag", "article:42",
			"--preview-only",
			"--json",
		})
		if !handled || code != 0 {
			t.Fatalf("preview-only failed: handled=%v code=%d err=%s", handled, code, errOut.String())
		}
		var prev adminapi.CachePurgePreviewResponse
		if err := json.Unmarshal(out.Bytes(), &prev); err != nil {
			t.Fatalf("unmarshal preview JSON: %v\n%s", err, out.String())
		}
		if prev.MatchedResources != 3 {
			t.Fatalf("expected 3 matched resources in preview, got %d", prev.MatchedResources)
		}
	})

	// 2. Execute tag purge via CLI (text output) and capture operation ID.
	var opID string
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteCache([]string{
			"purge",
			"--endpoint", ts.URL,
			"--token-file", tokenFile,
			"--host", "portal.example.com",
			"--tag", "article:42",
			"--idempotency-key", "cli-idem-621",
			"--json",
		})
		if code != 0 {
			t.Fatalf("execute tag purge failed: code=%d err=%s", code, errOut.String())
		}
		var res adminapi.CachePurgeResponse
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal purge JSON: %v\n%s", err, out.String())
		}
		if res.State != "succeeded" || res.InvalidatedResources != 3 {
			t.Fatalf("unexpected purge response: %+v", res)
		}
		opID = res.OperationID
	})

	// 3. Inspect operation status via --operation-id (human-readable text output).
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdRemoteCache([]string{
			"purge",
			"--endpoint", ts.URL,
			"--token-file", tokenFile,
			"--operation-id", opID,
		})
		if code != 0 || !strings.Contains(out.String(), opID) {
			t.Fatalf("operation lookup failed: code=%d out=%s err=%s", code, out.String(), errOut.String())
		}
	})

	// 4. Test --url, --prefix, --glob, --key, --host, human --preview, and --all (--confirm-broad).
	for _, args := range [][]string{
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--host", "portal.example.com", "--prefix", "/articles/", "--preview"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--url", "https://portal.example.com/articles/42"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--host", "portal.example.com", "--prefix", "/articles/"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--host", "portal.example.com", "--glob", "/articles/*/comments"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--key", "GET\nportal.example.com\n/articles/42"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--host", "portal.example.com", "--confirm-broad"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--all", "--confirm-broad"},
	} {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			code := cmdRemoteCache(args)
			if code != 0 {
				t.Fatalf("cmdRemoteCache(%v) failed: code=%d out=%s err=%s", args, code, out.String(), errOut.String())
			}
		})
	}

	// Usage error branches: missing selector, unexpected positional arg, conflicting --operation-id + --preview.
	for _, badArgs := range [][]string{
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--all", "extra-arg"},
		{"purge", "--endpoint", ts.URL, "--token-file", tokenFile, "--operation-id", opID, "--preview"},
	} {
		withRemoteOutput(t, func(_, _ *bytes.Buffer) {
			if code := cmdRemoteCache(badArgs); code == 0 {
				t.Fatalf("expected usage error for %v, got 0", badArgs)
			}
		})
	}

	// 5. Broad scope without --confirm-broad fails predictably with non-zero exit code.
	withRemoteOutput(t, func(_, errOut *bytes.Buffer) {
		code := cmdRemoteCache([]string{
			"purge",
			"--endpoint", ts.URL,
			"--token-file", tokenFile,
			"--all",
		})
		if code == 0 || !strings.Contains(errOut.String(), "confirm_broad") {
			t.Fatalf("expected --all without --confirm-broad to fail, got code=%d err=%s", code, errOut.String())
		}
	})

	// 6. Build jul binary and execute examples/cache-invalidation/purge-on-deploy.sh against live server!
	binPath := filepath.Join(t.TempDir(), "jul")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build jul failed: %v\n%s", err, string(out))
	}

	scriptPath := filepath.Join("..", "..", "examples", "cache-invalidation", "purge-on-deploy.sh")
	deployCmd := exec.Command("bash", scriptPath,
		"--endpoint", ts.URL,
		"--token-file", tokenFile,
		"--host", "portal.example.com",
		"--tag", "release:2026-10-25",
		"--idempotency-key", "deploy-script-621-1",
	)
	deployCmd.Env = append(os.Environ(), "JUL_BIN="+binPath)
	scriptOut, err := deployCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("purge-on-deploy.sh failed: %v\n%s", err, string(scriptOut))
	}
	var scriptRes adminapi.CachePurgeResponse
	if err := json.Unmarshal(scriptOut, &scriptRes); err != nil {
		t.Fatalf("unmarshal purge-on-deploy.sh JSON: %v\n%s", err, string(scriptOut))
	}
	if scriptRes.State != "succeeded" || scriptRes.InvalidationEffective != "effective" {
		t.Fatalf("unexpected purge-on-deploy.sh result: %+v", scriptRes)
	}
}

func TestAuditR6_ConsoleSnippetShellQuotingPreservesLiteralArgv(t *testing.T) {
	t.Parallel()

	panelPath := filepath.Join("..", "..", "internal", "admin", "ui", "src", "features", "traffic-controls", "CacheInvalidationPanel.tsx")
	srcBytes, err := os.ReadFile(panelPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", panelPath, err)
	}
	src := string(srcBytes)
	start := strings.Index(src, "function shellQuote(")
	end := strings.Index(src, "export interface CacheInvalidationPanelProps")
	if start < 0 || end <= start {
		t.Fatalf("could not locate shellQuote / buildCliCommandSnippet in %s", panelPath)
	}
	extracted := src[start:end]
	extracted = strings.ReplaceAll(extracted, "val: string): string", "val)")
	extracted = strings.ReplaceAll(extracted, "export function buildCliCommandSnippet(\n  selector: CacheSelector,\n  broadScope: boolean,\n): string", "function buildCliCommandSnippet(selector, broadScope)")
	extracted = strings.ReplaceAll(extracted, "const args: string[] =", "const args =")

	dangerousTag := "tag-$(echo PWNED)-`echo PWNED`-$HOME-'single'"
	jsRunner := extracted + "\n" + `console.log(buildCliCommandSnippet({kind:"tag", host:"portal.example.com", tags:[` + fmt.Sprintf("%q", dangerousTag) + `], tag_match:"any"}, false));`
	nodeCmd := exec.Command("node", "-e", jsRunner)
	snippetBytes, err := nodeCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node execution failed: %v\n%s", err, string(snippetBytes))
	}
	snippet := strings.TrimSpace(string(snippetBytes))

	shCmd := exec.Command("bash", "-c", "jul() { printf '%s\\n' \"$@\"; }\n"+snippet)
	argvOut, err := shCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash argv capture failed: %v\n%s", err, string(argvOut))
	}
	lines := strings.Split(strings.TrimSpace(string(argvOut)), "\n")
	foundLiteral := false
	for _, l := range lines {
		if l == dangerousTag {
			foundLiteral = true
		}
		if l == "PWNED" {
			t.Fatalf("shell substitution executed in generated CLI snippet: %s", string(argvOut))
		}
	}
	if !foundLiteral {
		t.Fatalf("expected literal %q in captured argv %v (snippet: %s)", dangerousTag, lines, snippet)
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"jul/internal/config"
	"jul/internal/migrate/nginx"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This paired lane uses the same pinned image and isolated container contract
// as the existing targeted-cache comparison, with repository-authored files.
func TestNGINXCorpusExpirationMIMEPair(t *testing.T) {
	if os.Getenv("REQUIRE_NGINX_E2E") != "1" {
		t.Skip("run make nginx-migration-e2e for the pinned NGINX comparison")
	}
	image := os.Getenv("NGINX_CACHE_REFERENCE_IMAGE")
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("digest-pinned reference image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"positive", "negative", "zero", "off", "empty", "custom"} {
		dir := filepath.Join(directory, prefix)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, ext := range []string{"ts", "foo"} {
			if err := os.WriteFile(filepath.Join(dir, "file."+ext), []byte("fixture content"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	source := `pid /tmp/nginx.pid;
 events {}
 http {
  access_log off;
  types {application/x-old ts;}
  types {application/x-older ts;video/mp2t "TS";}
  default_type "application/octet-stream";
  expires "1h 0m";
  server {
   listen 8080;
   root /fixture;
   location /positive/ {}
   location /negative/ {expires -1;}
   location /zero/ {expires 0;}
   location /off/ {expires off;}
   location /empty/ {types {}}
   location /custom/ {types {application/x-custom foo;}default_type text/plain;}
  }
 }`
	nginxPath := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(nginxPath, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	// Process access logging is outside the asserted dimensions and has no target
	// mapping; omit only that directive from the otherwise identical import source.
	importPath := filepath.Join(directory, "import.conf")
	if err := os.WriteFile(importPath, []byte(strings.Replace(source, "access_log off;", "", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, report, err := nginx.ImportFileWithImportOptions(importPath, nginx.ImportOptions{})
	if err != nil || report.Assessment.HasBlocking() {
		t.Fatalf("import: %v %+v", err, report)
	}
	for i := range cfg.Servers[0].Locations {
		cfg.Servers[0].Locations[i].Root = directory
	}
	raw, err := nginx.MarshalCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	baseURL, cleanup := startRealJulForCorpus(t, "expiration-mime", cfg)
	defer cleanup()
	output, err := exec.CommandContext(ctx, "docker", "run", "--detach", "--network", "none", "--read-only", "--user", "101:101", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m,uid=101,gid=101,mode=0700", "--tmpfs", "/var/cache/nginx:rw,noexec,nosuid,size=16m,uid=101,gid=101,mode=0700", "--volume", directory+":/fixture:ro", "--volume", nginxPath+":/etc/nginx/nginx.conf:ro", "--entrypoint", "nginx", image, "-g", "daemon off;").CombinedOutput()
	if err != nil {
		t.Fatalf("start NGINX: %v %s", err, output)
	}
	container := strings.TrimSpace(string(output))
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if output, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", container).CombinedOutput(); err != nil {
			t.Errorf("remove NGINX: %v %s", err, output)
		}
	})
	reference := func(path string, requestHeaders http.Header) (int, http.Header, []byte, error) {
		// Read the wire response directly: BusyBox wget omits headers for 206.
		var request strings.Builder
		fmt.Fprintf(&request, "GET %s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n", path)
		for name, values := range requestHeaders {
			for _, value := range values {
				fmt.Fprintf(&request, "%s: %s\r\n", name, value)
			}
		}
		request.WriteString("\r\n")
		cmd := exec.CommandContext(ctx, "docker", "exec", "-i", container, "nc", "-w", "3", "127.0.0.1", "8080")
		cmd.Stdin = strings.NewReader(request.String())
		output, err := cmd.CombinedOutput()
		if err != nil {
			return 0, nil, output, err
		}
		response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(output)), nil)
		if err != nil {
			return 0, nil, output, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		return response.StatusCode, response.Header, body, err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, _, _, err := reference("/positive/file.ts", nil)
		if err == nil && status == 200 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("NGINX readiness")
		case <-ticker.C:
		}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	for _, tc := range []struct {
		path, typ, cc string
		offset        time.Duration
	}{
		{"/positive/file.ts", "video/mp2t", "max-age=3600", time.Hour},
		{"/positive/file.foo", "application/octet-stream", "max-age=3600", time.Hour},
		{"/negative/file.ts", "video/mp2t", "no-cache", -time.Second},
		{"/zero/file.ts", "video/mp2t", "max-age=0", 0},
		{"/off/file.ts", "video/mp2t", "", 0},
		{"/empty/file.ts", "application/octet-stream", "max-age=3600", time.Hour},
		{"/custom/file.foo", "application/x-custom", "max-age=3600", time.Hour},
		{"/custom/file.ts", "text/plain", "max-age=3600", time.Hour},
	} {
		t.Run(strings.TrimPrefix(tc.path, "/"), func(t *testing.T) {
			before := time.Now()
			status, headers, output, err := reference(tc.path, nil)
			if err != nil || status != 200 {
				t.Fatalf("NGINX: %v %d %s", err, status, output)
			}
			resp, err := client.Get(baseURL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			for runtimeName, h := range map[string]http.Header{"NGINX": headers, "Jul": resp.Header} {
				if h.Get("Content-Type") != tc.typ || h.Get("Cache-Control") != tc.cc {
					t.Fatalf("%s: %v", runtimeName, h)
				}
				if tc.cc == "" {
					if h.Get("Expires") != "" {
						t.Fatal("off emitted Expires")
					}
					continue
				}
				exp, err := http.ParseTime(h.Get("Expires"))
				if err != nil || exp.Before(before.Add(tc.offset-time.Second)) || exp.After(time.Now().Add(tc.offset)) {
					t.Fatalf("%s Expires: %v %v", runtimeName, exp, err)
				}
			}
			// Original-resource MIME and expiration survive HEAD, conditional 304 and
			// partial responses. Each runtime uses its own ETag representation.
			for _, code := range []int{206, 304} {
				requestHeaders := http.Header{}
				julHeaders := http.Header{}
				if code == 206 {
					requestHeaders.Set("Range", "bytes=0-4")
					julHeaders.Set("Range", "bytes=0-4")
				} else {
					requestHeaders.Set("If-None-Match", headers.Get("ETag"))
					julHeaders.Set("If-None-Match", resp.Header.Get("ETag"))
				}
				status, h, output, err := reference(tc.path, requestHeaders)
				if err != nil || status != code || h.Get("Cache-Control") != tc.cc {
					t.Fatalf("NGINX %d: %v %d %s", code, err, status, output)
				}
				req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+tc.path, nil)
				req.Header = julHeaders
				conditional, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, conditional.Body)
				_ = conditional.Body.Close()
				if conditional.StatusCode != code || conditional.Header.Get("Cache-Control") != tc.cc {
					t.Fatal(conditional.StatusCode, conditional.Header)
				}
			}
			req, _ := http.NewRequestWithContext(ctx, "HEAD", baseURL+tc.path, nil)
			head, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = head.Body.Close()
			if head.StatusCode != 200 || head.Header.Get("Content-Type") != tc.typ || head.Header.Get("Cache-Control") != tc.cc {
				t.Fatal(fmt.Sprint(head.Header))
			}
		})
	}
}

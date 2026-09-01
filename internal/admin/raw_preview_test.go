// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestIssue81RawCachePreviewIsRestartOnlyAndValueFree(t *testing.T) {
	beforeRaw := []byte(`
[cache]
enabled = true
memory_max_size = "64m"
disk_path = "/tmp/jul-cache"
disk_max_size = "2g"
default_ttl = "1m"
stale_while_revalidate = "10s"
stale_if_error = "20s"

[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`)
	candidateRaw := []byte(`
[cache]
enabled = true
memory_max_size = "64m"
disk_path = "/tmp/jul-cache-2"
disk_max_size = "2g"
default_ttl = "1m"
stale_while_revalidate = "10s"
stale_if_error = "20s"

[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`)
	before, err := config.Parse(beforeRaw)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := previewRawCandidate(
		context.Background(),
		before,
		nil,
		lifecycle.Live{BoundHTTPAddrs: []string{"127.0.0.1:8080"}},
		"base-a",
		candidateRaw,
	)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.BaseVersion != "base-a" {
		t.Fatalf("base=%q", preview.BaseVersion)
	}
	if preview.Lifecycle.CanApplyHot {
		t.Fatal("cache preview unexpectedly permits hot apply")
	}
	if !preview.Lifecycle.CanStageRestart {
		t.Fatal("cache preview does not permit stage_restart")
	}
	found := false
	for _, path := range preview.Lifecycle.RestartRequired {
		if path == "cache.disk_path" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restart paths=%v", preview.Lifecycle.RestartRequired)
	}
}

func TestIssue81RawValidationErrorsDoNotEchoCandidateValues(t *testing.T) {
	const marker = "ISSUE81_SECRET_VALUE_MUST_NOT_ESCAPE"
	issues := secretSafeRawValidationErrors(errors.New("rate_limit.key: invalid value " + marker))
	encoded, err := json.Marshal(issues)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("validation response leaked configured value: %s", encoded)
	}
	if len(issues) == 0 || issues[0].Summary == "" || issues[0].Detail == "" {
		t.Fatalf("secret-safe validation response lost usable UX: %#v", issues)
	}
}

func TestIssue81MalformedRawCandidateReturnsBoundedSentinel(t *testing.T) {
	before, err := config.Parse([]byte(`
[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = previewRawCandidate(
		context.Background(),
		before,
		nil,
		lifecycle.Live{},
		"base-a",
		[]byte("token = \"ISSUE81_SECRET\"\n["),
	)
	if !errors.Is(err, errRawCandidateSyntax) {
		t.Fatalf("error=%v want bounded raw syntax sentinel", err)
	}
	if strings.Contains(err.Error(), "ISSUE81_SECRET") {
		t.Fatalf("syntax error echoed raw candidate: %v", err)
	}
}

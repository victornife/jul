// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package storagefs

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// stubStatFS replaces the platform capacity call for one test and counts calls.
func stubStatFS(t *testing.T, fn func(string) (Usage, error)) *int {
	t.Helper()
	orig := statFS
	calls := 0
	statFS = func(p string) (Usage, error) {
		calls++
		return fn(p)
	}
	t.Cleanup(func() { statFS = orig })
	return &calls
}

func realCapacityExpected() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "windows":
		return true
	}
	return false
}

func TestProbeExistingDirectoryAndFile(t *testing.T) {
	if !realCapacityExpected() {
		t.Skip("no capacity implementation on " + runtime.GOOS)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "access.log")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := Probe([]string{dir, file})
	for i, r := range res {
		if r.Reason != ReasonNone {
			t.Fatalf("result %d reason = %q", i, r.Reason)
		}
		if !r.HaveAvailable || !r.HaveTotal || r.Total == 0 {
			t.Fatalf("result %d has no real capacity: %+v", i, r)
		}
		if r.Available > r.Total {
			t.Fatalf("result %d available %d > total %d", i, r.Available, r.Total)
		}
		if r.ViaParent {
			t.Fatalf("existing path %d reported ViaParent", i)
		}
	}
	if res[0].Group != res[1].Group {
		t.Fatalf("file and its directory on different groups: %d vs %d", res[0].Group, res[1].Group)
	}
}

func TestProbeMissingNestedPathUsesNearestAncestorWithoutCreating(t *testing.T) {
	dir := t.TempDir()
	calls := stubStatFS(t, func(string) (Usage, error) {
		return Usage{Available: 50, HaveAvailable: true, Total: 100, HaveTotal: true}, nil
	})
	missing := filepath.Join(dir, "a", "b", "c", "audit.jsonl")
	res := Probe([]string{missing, dir})
	if res[0].Reason != ReasonNone || !res[0].ViaParent {
		t.Fatalf("missing path result = %+v, want measured via parent", res[0])
	}
	if res[1].ViaParent {
		t.Fatal("existing directory reported ViaParent")
	}
	if res[0].Group != res[1].Group {
		t.Fatalf("missing child not grouped with its existing ancestor: %d vs %d", res[0].Group, res[1].Group)
	}
	if *calls != 1 {
		t.Fatalf("statFS calls = %d, want 1 for one shared filesystem", *calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe created part of the missing path (stat err = %v)", err)
	}
}

func TestProbeRelativePathResolvesAgainstWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stubStatFS(t, func(string) (Usage, error) {
		return Usage{Available: 1, HaveAvailable: true, Total: 2, HaveTotal: true}, nil
	})
	res := Probe([]string{filepath.Join("jul-data", "certs"), "."})
	if res[0].Reason != ReasonNone || !res[0].ViaParent || res[0].Group != res[1].Group {
		t.Fatalf("relative path = %+v, cwd = %+v", res[0], res[1])
	}
}

func TestProbeSharedFilesystemIsMeasuredOnce(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"cache", "logs"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	calls := stubStatFS(t, func(string) (Usage, error) {
		return Usage{Available: 10, HaveAvailable: true, Total: 100, HaveTotal: true}, nil
	})
	res := Probe([]string{filepath.Join(dir, "cache"), filepath.Join(dir, "logs"), filepath.Join(dir, "logs", "access.log")})
	if *calls != 1 {
		t.Fatalf("statFS calls = %d, want 1", *calls)
	}
	for i, r := range res {
		if r.Group != 0 || r.Available != 10 || r.Total != 100 {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
}

func TestProbeSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs a privilege on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nowhere", "x"), dangling); err != nil {
		t.Fatal(err)
	}
	stubStatFS(t, func(string) (Usage, error) {
		return Usage{Available: 1, HaveAvailable: true, Total: 2, HaveTotal: true}, nil
	})
	res := Probe([]string{link, filepath.Join(link, "new.log"), dangling, filepath.Join(dangling, "child")})
	if res[0].Reason != ReasonNone || res[0].ViaParent {
		t.Fatalf("symlink to dir = %+v", res[0])
	}
	if res[1].Reason != ReasonNone || !res[1].ViaParent || res[1].Group != res[0].Group {
		t.Fatalf("missing file under symlinked dir = %+v", res[1])
	}
	for _, r := range res[2:] {
		if r.Reason != ReasonPathUnresolved || r.Group != -1 {
			t.Fatalf("dangling symlink = %+v, want path_unresolved", r)
		}
	}
}

func TestProbePermissionDeniedDoesNotFallBackToParent(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	calls := stubStatFS(t, func(string) (Usage, error) { return Usage{}, nil })
	res := Probe([]string{filepath.Join(locked, "history")})
	if res[0].Reason != ReasonPermissionDenied || res[0].Group != -1 {
		t.Fatalf("result = %+v, want permission_denied", res[0])
	}
	if *calls != 0 {
		t.Fatalf("statFS called %d times for an unresolvable path", *calls)
	}
}

func TestProbeNotADirectoryIsStatFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows reports a file-as-directory component as not-found")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	res := Probe([]string{filepath.Join(file, "child")})
	if res[0].Reason != ReasonStatFailed {
		t.Fatalf("result = %+v, want stat_failed", res[0])
	}
}

func TestProbeStatFSFailureModes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		err  error
		want Reason
	}{
		{errUnsupported, ReasonUnsupported},
		{&os.PathError{Op: "statfs", Path: dir, Err: os.ErrPermission}, ReasonPermissionDenied},
		{errors.New("io error"), ReasonStatFailed},
	} {
		calls := stubStatFS(t, func(string) (Usage, error) { return Usage{}, tc.err })
		res := Probe([]string{dir, filepath.Join(dir, "missing")})
		if *calls != 1 {
			t.Fatalf("%v: statFS calls = %d, want 1 (failure is cached per filesystem)", tc.err, *calls)
		}
		if res[0].Reason != tc.want || res[0].Group != -1 {
			t.Fatalf("%v: result = %+v, want %q", tc.err, res[0], tc.want)
		}
		if res[1].Reason != tc.want || !res[1].ViaParent {
			t.Fatalf("%v: missing child = %+v", tc.err, res[1])
		}
	}
}

func TestProbeEmptyInput(t *testing.T) {
	if got := Probe(nil); len(got) != 0 {
		t.Fatalf("Probe(nil) = %v", got)
	}
}

func TestUsageFromBlocks(t *testing.T) {
	u := usageFromBlocks(3, 10, 4096)
	if !u.HaveAvailable || !u.HaveTotal || u.Available != 3*4096 || u.Total != 10*4096 {
		t.Fatalf("usage = %+v", u)
	}
	if u := usageFromBlocks(3, 10, 0); u.HaveAvailable || u.HaveTotal {
		t.Fatalf("zero block size must be unavailable, got %+v", u)
	}
	if u := usageFromBlocks(1, math.MaxUint64, 4096); !u.HaveAvailable || u.HaveTotal {
		t.Fatalf("overflowing total must be unavailable, got %+v", u)
	}
}

func TestClassify(t *testing.T) {
	h := DefaultHints
	cap := func(avail, total uint64) Result {
		return Result{Usage: Usage{Available: avail, HaveAvailable: true, Total: total, HaveTotal: true}}
	}
	for _, tc := range []struct {
		name      string
		r         Result
		want      State
		wantRatio float64
		hasRatio  bool
	}{
		{"ok", cap(50, 100), StateOK, 0.5, true},
		{"low boundary is not low", cap(10, 100), StateOK, 0.10, true},
		{"low", cap(9, 100), StateLow, 0.09, true},
		{"critical boundary is low", cap(5, 100), StateLow, 0.05, true},
		{"critical", cap(4, 100), StateCritical, 0.04, true},
		{"exhausted", cap(0, 100), StateCritical, 0, true},
		{"zero total", cap(0, 0), StateUnavailable, 0, false},
		{"no total", Result{Usage: Usage{Available: 5, HaveAvailable: true}}, StateUnavailable, 0, false},
		{"no available", Result{Usage: Usage{Total: 5, HaveTotal: true}}, StateUnavailable, 0, false},
		{"unsupported", Result{Reason: ReasonUnsupported}, StateUnavailable, 0, false},
		{"unresolved", Result{Reason: ReasonPathUnresolved}, StateUnavailable, 0, false},
		{"denied", Result{Reason: ReasonPermissionDenied}, StateError, 0, false},
		{"failed", Result{Reason: ReasonStatFailed}, StateError, 0, false},
	} {
		state, ratio := h.Classify(tc.r)
		if state != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, state, tc.want)
		}
		if (ratio != nil) != tc.hasRatio {
			t.Errorf("%s: ratio presence = %v, want %v", tc.name, ratio != nil, tc.hasRatio)
		}
		if ratio != nil && math.Abs(*ratio-tc.wantRatio) > 1e-12 {
			t.Errorf("%s: ratio = %v, want %v", tc.name, *ratio, tc.wantRatio)
		}
	}
}

// BenchmarkProbe measures the per-poll cost of the full Jul category set on
// one real filesystem: seven paths, some not yet created.
func BenchmarkProbe(b *testing.B) {
	dir := b.TempDir()
	paths := []string{
		filepath.Join(dir, "cache"), filepath.Join(dir, "logs", "access.log"),
		filepath.Join(dir, "audit", "audit.jsonl"), filepath.Join(dir, "jul.toml"),
		filepath.Join(dir, "history"), filepath.Join(dir, "plugins"), filepath.Join(dir, "certs"),
	}
	_ = os.Mkdir(paths[0], 0o700)
	b.ReportAllocs()
	for b.Loop() {
		_ = Probe(paths)
	}
}

func TestProbeRootWithoutExistingAncestor(t *testing.T) {
	stubStatFS(t, func(string) (Usage, error) { return Usage{}, nil })
	orig := statObject
	statObject = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	t.Cleanup(func() { statObject = orig })
	if r := Probe([]string{filepath.Join(t.TempDir(), "x")})[0]; r.Reason != ReasonPathUnresolved {
		t.Fatalf("result = %+v, want path_unresolved when no ancestor exists", r)
	}
}

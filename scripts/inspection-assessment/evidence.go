// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ManifestSchema identifies the manifest format.
const ManifestSchema = "jul-inspection-assessment/run/v1"

// SumsFile lists every other file of a run with its SHA-256.
const SumsFile = "SHA256SUMS"

// BinaryIdentity describes a measured or harness executable.
type BinaryIdentity struct {
	Path      string            `json:"path"`
	SHA256    string            `json:"sha256"`
	GoVersion string            `json:"go_version,omitempty"`
	Module    string            `json:"module,omitempty"`
	Settings  map[string]string `json:"build_settings,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// IdentifyBinary hashes a binary and reads its embedded build information.
func IdentifyBinary(path string) BinaryIdentity {
	id := BinaryIdentity{Path: path}
	sum, err := fileSHA(path)
	if err != nil {
		id.Error = err.Error()
		return id
	}
	id.SHA256 = sum
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		id.Error = "buildinfo: " + err.Error()
		return id
	}
	id.GoVersion, id.Module, id.Settings = bi.GoVersion, bi.Main.Path, map[string]string{}
	for _, s := range bi.Settings {
		switch s.Key {
		case "-tags", "-trimpath", "-buildmode", "-ldflags", "CGO_ENABLED", "GOARCH", "GOOS", "GOARM64", "GOAMD64",
			"vcs", "vcs.revision", "vcs.time", "vcs.modified":
			id.Settings[s.Key] = s.Value
		}
	}
	return id
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TreeSHA hashes the sorted relative paths and contents of a source tree.
func TreeSHA(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		sum, err := fileSHA(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%s  %s\n", sum, filepath.ToSlash(rel))
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// TargetIdentity is a measured binary plus its configuration adapter.
type TargetIdentity struct {
	TargetSpec
	Binary  BinaryIdentity `json:"binary"`
	Adapter Adapter        `json:"adapter_definition"`
}

// Manifest is the machine-readable provenance of one run set.
type Manifest struct {
	Schema       string            `json:"schema"`
	RunID        string            `json:"run_id"`
	Started      time.Time         `json:"started"`
	Ended        *time.Time        `json:"ended"`
	State        string            `json:"state"` // running | complete | failed | interrupted
	Failure      string            `json:"failure,omitempty"`
	PlanVersion  string            `json:"plan_version"`
	PlanSHA      string            `json:"plan_sha256"`
	RulesSHA     string            `json:"interpretation_sha256"`
	Profile      Profile           `json:"profile"`
	Repetitions  int               `json:"repetitions"`
	Selection    []string          `json:"selection"`
	ScenarioSHAs map[string]string `json:"scenario_sha256"`
	Targets      []TargetIdentity  `json:"targets"`
	Harness      BinaryIdentity    `json:"harness"`
	Guest        *Artifact         `json:"guest,omitempty"`
	GuestNoBody  *Artifact         `json:"guest_nobody,omitempty"`
	StaticPage   Artifact          `json:"static_page"`
	TLS          Artifact          `json:"tls_certificate"`
	Fault        string            `json:"controlled_fault,omitempty"`
	KillJulAfter string            `json:"controlled_kill_jul_after,omitempty"`
	Platform     Platform          `json:"platform"`
	Envelope     Envelope          `json:"requested_envelope"`
	Placement    map[string]string `json:"placement"`
	Results      []string          `json:"results"`
	Interrupted  bool              `json:"interrupted"`
	Notes        []string          `json:"notes,omitempty"`
}

// Artifact identifies one input file.
type Artifact struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SourceSHA string `json:"source_tree_sha256,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Platform records the host.
type Platform struct {
	GOOS, GOARCH string
	CPUs         int    `json:"cpus"`
	Kernel       string `json:"kernel"`
	Machine      string `json:"machine"`
	MemTotal     string `json:"mem_total"`
	SwapTotal    string `json:"swap_total"`
}

// HostPlatform reads the current platform.
func HostPlatform(s Sys) Platform {
	p := Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CPUs: runtime.NumCPU()}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		p.Kernel = unix.ByteSliceToString(u.Release[:])
		p.Machine = unix.ByteSliceToString(u.Machine[:])
	}
	if b, err := os.ReadFile(filepath.Join(s.Proc, "meminfo")); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			k, v, _ := strings.Cut(line, ":")
			switch k {
			case "MemTotal":
				p.MemTotal = strings.TrimSpace(v)
			case "SwapTotal":
				p.SwapTotal = strings.TrimSpace(v)
			}
		}
	}
	return p
}

// WriteSums writes SHA256SUMS for every file except itself.
func WriteSums(dir string) error {
	lines, err := sumTree(dir)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SumsFile), []byte(strings.Join(lines, "")), 0o644) //nolint:gosec // evidence
}

func sumTree(dir string) ([]string, error) {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == SumsFile {
			return nil
		}
		sum, err := fileSHA(p)
		if err != nil {
			return err
		}
		lines = append(lines, sum+"  "+rel+"\n")
		return nil
	})
	slices.Sort(lines)
	return lines, err
}

// VerifyRun checks a run directory: every listed file is present and
// unmodified, no unlisted file exists, the manifest is well formed and
// finished, and every result belongs to the manifest's plan.
func VerifyRun(dir string) error {
	want, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		return fmt.Errorf("%s: %w", SumsFile, err)
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(want))
	for sc.Scan() {
		sum, rel, ok := strings.Cut(sc.Text(), "  ")
		if !ok || len(sum) != 64 {
			return fmt.Errorf("%s: malformed line %q", SumsFile, sc.Text())
		}
		listed[rel] = sum
	}
	got, err := sumTree(dir)
	if err != nil {
		return err
	}
	var problems []string
	seen := map[string]bool{}
	for _, l := range got {
		sum, rel, _ := strings.Cut(strings.TrimSuffix(l, "\n"), "  ")
		seen[rel] = true
		switch w, ok := listed[rel]; {
		case !ok:
			problems = append(problems, "unlisted file "+rel)
		case w != sum:
			problems = append(problems, "modified "+rel)
		}
	}
	for rel := range listed {
		if !seen[rel] {
			problems = append(problems, "missing "+rel)
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	m, err := ReadManifest(dir)
	if err != nil {
		return err
	}
	if m.State == "running" || m.Ended == nil {
		return errors.New("manifest: run did not finish writing")
	}
	if plan, err := fileSHA(filepath.Join(dir, "plan.json")); err != nil || plan != m.PlanSHA {
		return fmt.Errorf("plan.json does not match the manifest plan identity %s", m.PlanSHA)
	}
	for _, rel := range m.Results {
		var r RunResult
		if err := readJSON(filepath.Join(dir, rel), &r); err != nil {
			return fmt.Errorf("result %s: %w", rel, err)
		}
		if want := m.ScenarioSHAs[r.Scenario+"/"+r.Variant]; want == "" || want != r.ScenarioHash {
			return fmt.Errorf("result %s: scenario hash %s does not match the manifest", rel, r.ScenarioHash)
		}
	}
	return nil
}

// ReadManifest loads and schema-checks manifest.json.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	if err := readJSON(filepath.Join(dir, "manifest.json"), &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if m.Schema != ManifestSchema {
		return m, fmt.Errorf("manifest: schema %q, want %q", m.Schema, ManifestSchema)
	}
	if m.RunID == "" || m.PlanSHA == "" || len(m.Targets) == 0 {
		return m, errors.New("manifest: missing run id, plan identity or targets")
	}
	return m, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

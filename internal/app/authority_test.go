// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

// TestResolveConfigAuthority pins ADR 0019 §9.1's fixed-default rule across
// every combination of raw value and hasConfigPath.
func TestResolveConfigAuthority(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		hasConfigPath bool
		wantAuthority ConfigAuthority
		wantSource    ConfigAuthoritySource
	}{
		{"no config file wins regardless of raw", "managed", false, AuthorityFileOwned, AuthoritySourceNoConfigFile},
		{"no config file, empty raw", "", false, AuthorityFileOwned, AuthoritySourceNoConfigFile},
		{"explicit managed", "managed", true, AuthorityManaged, AuthoritySourceExplicit},
		{"explicit file_owned", "file_owned", true, AuthorityFileOwned, AuthoritySourceExplicit},
		{"omitted defaults to file_owned", "", true, AuthorityFileOwned, AuthoritySourceDefault},
		{"unrecognized value fails safe to file_owned/default", "controller_owned", true, AuthorityFileOwned, AuthoritySourceDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAuthority, gotSource := ResolveConfigAuthority(tc.raw, tc.hasConfigPath)
			if gotAuthority != tc.wantAuthority || gotSource != tc.wantSource {
				t.Errorf("ResolveConfigAuthority(%q, %v) = (%v, %v), want (%v, %v)",
					tc.raw, tc.hasConfigPath, gotAuthority, gotSource, tc.wantAuthority, tc.wantSource)
			}
		})
	}
}

// TestCheckManagedFilesystemNoopOutsideManaged pins that the check is
// entirely inert for file_owned authority or an empty config path — ADR
// 0019 §11.3 is a managed-mode-only constraint.
func TestCheckManagedFilesystemNoopOutsideManaged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(path, []byte("a = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if diags := CheckManagedFilesystem(path, AuthorityFileOwned); diags != nil {
		t.Errorf("file_owned = %+v, want nil", diags)
	}
	if diags := CheckManagedFilesystem("", AuthorityManaged); diags != nil {
		t.Errorf("empty path = %+v, want nil", diags)
	}
}

// TestCheckManagedFilesystemWritableRegularFileIsClean pins the ordinary
// case: a plain, writable configuration path reports no findings.
func TestCheckManagedFilesystemWritableRegularFileIsClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(path, []byte("a = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if diags := CheckManagedFilesystem(path, AuthorityManaged); diags != nil {
		t.Errorf("diags = %+v, want none for a plain writable path", diags)
	}
}

// TestCheckManagedFilesystemSymlinkIsError pins ADR 0019 §11.3: a symlinked
// config path under managed authority is an error-severity finding,
// detected with Lstat so the symlink itself — not its target — is what is
// inspected.
func TestCheckManagedFilesystemSymlinkIsError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(target, []byte("a = 1\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "server.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable in this environment: %v", err)
	}

	diags := CheckManagedFilesystem(link, AuthorityManaged)
	found := false
	for _, d := range diags {
		if d.Severity == config.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("diags = %+v, want an error-severity finding for a symlinked path", diags)
	}
}

// TestCheckManagedFilesystemUnwritableDirIsWarning pins the directory-not-
// writable case as a warning (not an error, and not fatal): a managed
// process still runs and reports its mode even though every managed write
// will fail.
func TestCheckManagedFilesystemUnwritableDirIsWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(path, []byte("a = 1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod directory read-only: %v", err)
	}
	defer os.Chmod(dir, 0o700) //nolint:errcheck // best-effort cleanup so t.TempDir can remove it

	diags := CheckManagedFilesystem(path, AuthorityManaged)
	if len(diags) == 0 {
		t.Skip("this platform's permission model did not make the directory unwritable")
	}
	found := false
	for _, d := range diags {
		if d.Severity == config.SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("diags = %+v, want a warning-severity finding for an unwritable directory", diags)
	}
}

// TestTruncatedDigest pins the §13 bound: the same 16 hex characters
// CanonicalVersion uses.
func TestTruncatedDigest(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	if got := truncatedDigest(full); got != "0123456789abcdef" {
		t.Errorf("truncatedDigest(full) = %q, want the first 16 hex characters", got)
	}
	if got := truncatedDigest(""); got != "" {
		t.Errorf("truncatedDigest(\"\") = %q, want empty", got)
	}
	short := "abcd"
	if got := truncatedDigest(short); got != short {
		t.Errorf("truncatedDigest(short) = %q, want unchanged %q", got, short)
	}
}

// TestConfigAuthorityString pins the wire rendering, including the default
// arm for any value other than AuthorityManaged.
func TestConfigAuthorityString(t *testing.T) {
	if got := AuthorityManaged.String(); got != "managed" {
		t.Errorf("AuthorityManaged.String() = %q, want managed", got)
	}
	if got := AuthorityFileOwned.String(); got != "file_owned" {
		t.Errorf("AuthorityFileOwned.String() = %q, want file_owned", got)
	}
	if got := ConfigAuthority(255).String(); got != "file_owned" {
		t.Errorf("unrecognized ConfigAuthority.String() = %q, want file_owned (fail-safe default)", got)
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"jul/internal/config"
)

// ConfigAuthority is the process-wide, immutable ownership mode of the
// configuration file, established exactly once at startup from the effective
// configuration and never changed while the process runs (ADR 0019 §9/§10).
//
// Every mutating server endpoint must consult one authoritative accessor
// derived from this value; it is never inferred per-surface and never
// hot-switched. Tests may inject a mode explicitly instead of relying on
// global mutable state, because it is passed as plain data rather than read
// from a package-level variable.
type ConfigAuthority uint8

const (
	// AuthorityManaged means Jul owns configuration persistence, managed
	// history, and drift detection. Console/API writes are subject to RBAC and
	// optimistic concurrency; external file edits become drift and are never
	// silently adopted.
	AuthorityManaged ConfigAuthority = iota
	// AuthorityFileOwned means an external file or GitOps pipeline owns the
	// configuration file. Every mutating admin endpoint is refused before any
	// side effect, and Jul never writes the file except the one bounded
	// epoch-close cleanup performed once at startup (ADR 0019 §17.2).
	AuthorityFileOwned
)

// String renders the wire value used in configuration, status, and audit
// output. It is never used for equality checks in Go code; compare the typed
// constants directly.
func (a ConfigAuthority) String() string {
	switch a {
	case AuthorityManaged:
		return "managed"
	default:
		return "file_owned"
	}
}

// ConfigAuthoritySource reports why the effective authority is what it is, so
// an operator never has to infer it (ADR 0019 §9.1).
type ConfigAuthoritySource string

const (
	// AuthoritySourceExplicit means the operator declared config_authority.
	AuthoritySourceExplicit ConfigAuthoritySource = "explicit"
	// AuthoritySourceDefault means the field was omitted and resolved to the
	// fixed file_owned default.
	AuthoritySourceDefault ConfigAuthoritySource = "default"
	// AuthoritySourceNoConfigFile means the process has no configuration file
	// at all (e.g. `jul run --serve`/`--proxy`), so there is no desired-state
	// file to own. Distinct from "default" so an operator can tell "nobody
	// declared it" from "there is nothing to declare it about" (ADR 0019
	// §9.1.1).
	AuthoritySourceNoConfigFile ConfigAuthoritySource = "no_config_file"
)

// ResolveConfigAuthority implements ADR 0019 §9.1's fixed-default rule:
// omitted resolves to file_owned; an explicit value always wins; there is no
// derivation from any other field (in particular, never from
// [admin].enabled). raw is the unresolved GlobalConfig.ConfigAuthority string;
// hasConfigPath reports whether the process has a configuration file at all.
// Validation (internal/config) has already rejected any value other than "",
// "managed", or "file_owned" by the time this runs, so the switch's default
// arm is unreachable in production and exists only to fail safe.
func ResolveConfigAuthority(raw string, hasConfigPath bool) (ConfigAuthority, ConfigAuthoritySource) {
	if !hasConfigPath {
		return AuthorityFileOwned, AuthoritySourceNoConfigFile
	}
	switch raw {
	case "managed":
		return AuthorityManaged, AuthoritySourceExplicit
	case "file_owned":
		return AuthorityFileOwned, AuthoritySourceExplicit
	case "":
		return AuthorityFileOwned, AuthoritySourceDefault
	default:
		return AuthorityFileOwned, AuthoritySourceDefault
	}
}

// ConfigState is the closed, emitted-only wire enum of ADR 0019 §16/§10. It is
// computed once per assessment from the same evidence that drives drift
// detection and managed-baseline recovery, and is identical across the status
// response, apply results, and any future CLI JSON for that assessment.
type ConfigState string

const (
	ConfigStateManagedClean          ConfigState = "managed_clean"
	ConfigStateManagedUnadopted      ConfigState = "managed_unadopted"
	ConfigStateManagedDrift          ConfigState = "managed_drift"
	ConfigStateManagedDesiredAhead   ConfigState = "managed_desired_ahead"
	ConfigStateManagedPendingRestart ConfigState = "managed_pending_restart"
	ConfigStateManagedFailedApply    ConfigState = "managed_failed_apply"
	ConfigStateManagedInconsistent   ConfigState = "managed_inconsistent"
	ConfigStateFileOwnedClean        ConfigState = "file_owned_clean"
	ConfigStateFileOwnedDesiredAhead ConfigState = "file_owned_desired_ahead"
	ConfigStateFileOwnedInvalid      ConfigState = "file_owned_invalid"
)

// ManagedInconsistentReason is the bounded reason accompanying
// ConfigStateManagedInconsistent (ADR 0019 §11.2.1/§11.2.1b). The state alone
// is not actionable; the reason distinguishes unrelated failure classes with
// unrelated remedies.
type ManagedInconsistentReason string

const (
	ReasonMarkerUnreadable               ManagedInconsistentReason = "marker_unreadable"
	ReasonMarkerMissing                  ManagedInconsistentReason = "marker_missing"
	ReasonCleanupIncomplete              ManagedInconsistentReason = "cleanup_incomplete"
	ReasonMarkerContradictsDisk          ManagedInconsistentReason = "marker_contradicts_disk"
	ReasonMarkerContradictsStagedRestart ManagedInconsistentReason = "marker_contradicts_staged_restart"
	ReasonSnapshotMissing                ManagedInconsistentReason = "snapshot_missing"
	ReasonSnapshotUnreadable             ManagedInconsistentReason = "snapshot_unreadable"
	ReasonSnapshotDigestMismatch         ManagedInconsistentReason = "snapshot_digest_mismatch"
	ReasonBaselineUnwritable             ManagedInconsistentReason = "baseline_unwritable"
	ReasonRestorationFailed              ManagedInconsistentReason = "restoration_failed"
)

// DegradedKind is the closed set of ADR 0019 §33.2 degradation kinds. A
// degradation never upgrades or downgrades an operation's terminal outcome;
// it answers a separate question ("is anything about this operation
// unhealthy") alongside it.
type DegradedKind string

const (
	DegradedBaselineError     DegradedKind = "baseline_error"
	DegradedStagingError      DegradedKind = "staging_error"
	DegradedStagingIncomplete DegradedKind = "staging_incomplete"
	DegradedDriftAfterAdopt   DegradedKind = "drift_after_adopt"
	DegradedDriftUnknown      DegradedKind = "drift_unknown"
	DegradedHistoryError      DegradedKind = "history_error"
	DegradedFinalizationError DegradedKind = "finalization_error"
)

// DegradedEntry is one bounded degradation carried on an apply/adopt result.
// Message carries only an error class, never a path, digest, or configuration
// content (ADR 0019 §33.2).
type DegradedEntry struct {
	Kind    DegradedKind `json:"kind"`
	Message string       `json:"message"`
}

// CheckManagedFilesystem implements ADR 0019 §11.3: managed mode requires a
// writable, non-symlinked configuration path. It is validation-adjacent
// rather than validation — the filesystem is not part of the configuration
// document, so a check that failed the configuration outright on a property
// of the machine would make it non-portable — which is why it returns
// lint-shaped diagnostics instead of an error. It is a no-op outside managed
// authority or when configPath is empty (no configuration file).
//
// Symlink detection uses os.Lstat, which reports the path's own type rather
// than following it; os.Stat would report the symlink's target and could
// never see that the path itself is a link. A Kubernetes ConfigMap/Secret
// mount is exactly this shape (`server.toml` -> `..data/server.toml`):
// os.Rename, which atomicfile.Write uses to commit every managed write,
// replaces the symlink itself rather than the file it points to, detaching
// the configuration from the volume that is supposed to update it.
//
// Directory writability is checked functionally — creating and removing a
// temporary file — rather than by inspecting permission bits, so the check
// behaves the same on every platform atomicfile.Write itself runs on.
func CheckManagedFilesystem(configPath string, authority ConfigAuthority) []config.Diagnostic {
	if authority != AuthorityManaged || configPath == "" {
		return nil
	}
	var diags []config.Diagnostic
	if fi, err := os.Lstat(configPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		diags = append(diags, config.Diagnostic{
			Severity: config.SeverityError,
			Field:    "global.config_authority",
			Message:  fmt.Sprintf("config_authority is managed, but %s is a symlink", configPath),
			Hint:     "a managed write replaces the symlink itself with a regular file, detaching it from whatever it points to (e.g. a Kubernetes ConfigMap/Secret mount); declare config_authority = \"file_owned\" instead",
		})
	}
	if dir := filepath.Dir(configPath); !dirIsWritable(dir) {
		diags = append(diags, config.Diagnostic{
			Severity: config.SeverityWarning,
			Field:    "global.config_authority",
			Message:  fmt.Sprintf("config_authority is managed, but %s is not writable", dir),
			Hint:     "every managed write will fail until the directory is writable, or declare config_authority = \"file_owned\"",
		})
	}
	return diags
}

// dirIsWritable reports whether dir accepts a new file, using the same
// create-temp-file operation atomicfile.Write performs for a real managed
// write, so the answer matches what a write would actually do.
func dirIsWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".jul-writable-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// truncatedDigest bounds a raw hex digest to the same 16 hex characters
// server.CanonicalVersion uses (ADR 0019 §13), so drift status never carries
// more of the digest than the wire contract permits. Shorter-than-16 inputs
// (notably an empty digest, when nothing has been read yet) pass through
// unchanged.
func truncatedDigest(digest string) string {
	const n = 16
	if len(digest) <= n {
		return digest
	}
	return digest[:n]
}

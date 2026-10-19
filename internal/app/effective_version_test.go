// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
	"jul/internal/versionid"
)

// This is the maintained inversion of the baseline reproduction. A real
// coordinator must emit a keyed candidate ID, using its immutable resolution.
func TestEffectiveVersionMustNotConfirmOfflineGuess(t *testing.T) {
	t.Setenv("JUL_644_WEAK", "warn")
	path := filepath.Join(t.TempDir(), "jul.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProxyTarget("127.0.0.1:9000", ":8081")
	cfg.Global.LogLevel = "${env:JUL_644_WEAK}"
	raw, _ := config.Marshal(cfg)
	versions, _ := versionid.New(bytes.Repeat([]byte{42}, 32))
	c := &ConfigApplyCoordinator{Versions: versions, BaseCtx: context.Background(), Path: path, Preflight: testPreflight(), PlannedRestart: &PlannedRestartStore{}, LiveSnapshot: func() server.LiveSnapshot { return server.LiveSnapshot{} }, SubmitReload: func(req server.ReloadRequest) error {
		req.Result <- server.ReloadResult{ID: req.ID, Outcome: server.ReloadAppliedLive, Published: true, ServingVersion: versions.Config(req.Candidate.Effective)}
		return nil
	}}
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, raw, ApplyHot)
	if err != nil || !res.OK {
		t.Fatalf("apply: %+v %v", res, err)
	}
	guessed, _ := config.Parse(raw)
	guessed.Global.LogLevel = "warn"
	if res.DesiredVersion == server.CanonicalVersion(guessed) {
		t.Fatal("public desired_version confirms the synthetic low-entropy reference offline")
	}
	if res.DesiredVersion != versions.Config(guessed) || res.ServingVersion != res.DesiredVersion {
		t.Fatalf("candidate/publication context mismatch: %+v", res)
	}
	rawCfg, _ := config.Parse(raw)
	if res.PersistedVersion != server.CanonicalVersion(rawCfg) {
		t.Fatal("raw CAS changed")
	}
}

func TestLegacyAndIndependentPendingVersionsNeverAuthorizeDiscard(t *testing.T) {
	versions, _ := versionid.New(bytes.Repeat([]byte{1}, 32))
	other, _ := versionid.New(bytes.Repeat([]byte{2}, 32))
	for _, tc := range []struct{ name, base, staged, comparison string }{
		{"legacy", "0123456789abcdef", "fedcba9876543210", "unverifiable"},
		{"unknown", "ev2:unknown", "ev2:unknown", "unverifiable"},
		{"mixed", versions.Digest("effective-config", nil), "0123456789abcdef", "unverifiable"},
		{"independent", other.Digest("effective-config", nil), other.Digest("effective-config", []byte("staged")), "different_context"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "jul.toml")
			base := []byte("base")
			staged := []byte("staged")
			os.WriteFile(path, base, 0600)
			store := NewFilePlannedRestartStore(path)
			store.Versions = versions
			m := PlannedRestartMarker{BaseRawSHA256: sha256Hex(base), StagedRawSHA256: sha256Hex(staged), BaseServingVersion: tc.base, StagedVersion: tc.staged, StagedPersistedVersion: "raw-cas"}
			if err := store.StageManaged(base, staged, m); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(path, staged, 0600)
			if err := store.PromoteToStaged(staged); err != nil {
				t.Fatal(err)
			}
			status := store.Status()
			if status.VersionComparison != tc.comparison || status.DiscardAvailable {
				t.Fatalf("unsafe comparison: %+v", status)
			}
			data, _ := json.Marshal(status)
			for _, old := range []string{"0123456789abcdef", "fedcba9876543210", "ev2:unknown"} {
				if strings.Contains(string(data), old) {
					t.Fatal("legacy oracle re-exported")
				}
			}
			if _, err := store.DiscardSafe(versions.Digest("effective-config", nil)); !errors.Is(err, ErrServingVersionChanged) {
				t.Fatalf("discard did not fail closed: %v", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, staged) {
				t.Fatal("refused discard persisted")
			}
			// Startup recovery is bound to raw bytes actually loaded, so a genuine
			// restart into staged bytes clears even legacy metadata without fake equality.
			restarted := NewFilePlannedRestartStore(path)
			restarted.Versions = versions
			if err := restarted.Reconcile(); err != nil {
				t.Fatal(err)
			}
			if restarted.IsPending() {
				t.Fatal("legacy state remains permanently pending")
			}
		})
	}
}

func TestPreparationErrorsUsePositionInsteadOfSecretDerivedScope(t *testing.T) {
	for _, method := range []string{"forward", "jwt", "basic"} {
		t.Run(method, func(t *testing.T) {
			factory, cleanup := minimalFactory(t)
			defer cleanup()
			cfg := config.ProxyTarget("127.0.0.1:9000", ":0")
			// The resolver permits an ordinary matcher to carry a weak reference.
			// Its resolved scope hash must not appear in a public preparation error.
			cfg.Servers[0].Locations[0].Match.Path = "/synthetic-low-entropy-a"
			access := &config.AuthConfig{}
			switch method {
			case "forward":
				access.ForwardAuth = &config.ForwardAuthConfig{URL: "http://%%%/verify"}
			case "jwt":
				access.JWT = &config.JWTAuthConfig{JWKSURL: "http://%%%/jwks"}
			case "basic":
				access.Basic = &config.BasicAuthConfig{File: filepath.Join(t.TempDir(), "missing")}
			}
			cfg.Servers[0].Locations[0].Auth = access
			private := AuthScope(cfg.Servers[0], cfg.Servers[0].Locations[0])
			_, done, err := factory.Build(context.Background(), cfg, false)
			if done != nil {
				defer done()
			}
			if err == nil || !strings.Contains(err.Error(), "servers[0].locations[0]") || strings.Contains(err.Error(), private) {
				t.Fatalf("scope exposed or missing position: %v", err)
			}
		})
	}
}

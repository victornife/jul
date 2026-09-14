#!/usr/bin/env python3
from pathlib import Path


def read(path):
    return Path(path).read_text()


def write(path, text):
    Path(path).write_text(text)


def rep(path, old, new, count=1):
    text = read(path)
    found = text.count(old)
    if found != count:
        raise SystemExit(f"{path}: expected {count} matches, found {found}: {old[:140]!r}")
    write(path, text.replace(old, new, count))

# ---- self-review corrections -------------------------------------------------
rep(
    "internal/server/server.go",
    "// bind-time settings: TLS, mTLS, h2c, HTTP/3, timeouts, header limits, and\n// connection cap. The entry is NOT yet registered in s.listeners and httpd.Serve",
    "// bind-time settings: TLS, mTLS, h2c, HTTP/3, timeouts and header limits.\n// The connection cap is listener-owned but live policy, initialized from cfg and\n// updated in place at Publish. The entry is NOT yet registered in s.listeners and httpd.Serve",
)

rep(
    "internal/lifecycle/registry.go",
    '''func rateLimitEntries() []Entry {
\treturn hotGroup(SubRateLimit, reasonRateLimitPolicy,
\t\t"rate_limit.burst",
\t\t"rate_limit.enabled",
\t\t"rate_limit.key",
\t\t"rate_limit.rate",
\t\t"rate_limit.max_conns",
\t)
}
''',
    '''func rateLimitEntries() []Entry {
\tout := hotGroup(SubRateLimit, reasonRateLimitPolicy,
\t\t"rate_limit.burst",
\t\t"rate_limit.enabled",
\t\t"rate_limit.key",
\t\t"rate_limit.rate",
\t)
\tout = append(out, hot("rate_limit.max_conns", SubRateLimit,
\t\t"the stable listener-owned connection admission limiter publishes the effective cap at reload Publish; admitted connections are never terminated (#106)"))
\treturn out
}
''',
)

# A failed metadata deletion must not be followed by deleting the raw snapshot:
# preserving the pair is safer than manufacturing an orphan sidecar. If sidecar
# removal succeeds and raw removal then fails, the remaining raw-only snapshot is
# already a supported backward-compatible form.
rep(
    "internal/admin/history.go",
    '''\t\tif err := h.removeFile(filepath.Join(h.dir, id+historyMetaExt)); err != nil && !errors.Is(err, os.ErrNotExist) {
\t\t\terrs = append(errs, fmt.Errorf("remove history metadata: %w", err))
\t\t}
\t\tif err := h.removeFile(filepath.Join(h.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
''',
    '''\t\tif err := h.removeFile(filepath.Join(h.dir, id+historyMetaExt)); err != nil && !errors.Is(err, os.ErrNotExist) {
\t\t\terrs = append(errs, fmt.Errorf("remove history metadata: %w", err))
\t\t\tcontinue // preserve the raw+sidecar pair on metadata deletion failure
\t\t}
\t\tif err := h.removeFile(filepath.Join(h.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
''',
)

# ---- stronger retention / transaction tests ---------------------------------
write("internal/admin/history_retention_contract_test.go", r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHistoryRetentionPrunesRawAndSidecarAsPair(t *testing.T) {
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	entries, err := h.list()
	if err != nil || len(entries) != 3 {
		t.Fatalf("seed list len=%d err=%v", len(entries), err)
	}
	oldest := entries[len(entries)-1].ID
	if !h.setRetention(2) {
		t.Fatal("3 -> 2 must request a post-Publish prune")
	}
	if err := h.pruneCurrent(); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{historyExt, historyMetaExt} {
		if _, err := os.Stat(filepath.Join(h.dir, oldest+ext)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("oldest %s survived pair prune: %v", ext, err)
		}
	}
}

func TestHistoryRetentionMetadataDeleteFailurePreservesPair(t *testing.T) {
	h := newHistory(t.TempDir(), 2)
	seedHistoryWithMeta(t, h, 2)
	entries, err := h.list()
	if err != nil || len(entries) != 2 {
		t.Fatalf("seed list len=%d err=%v", len(entries), err)
	}
	oldest := entries[len(entries)-1].ID
	h.setRetention(1)
	h.remove = func(path string) error {
		if filepath.Ext(path) == historyMetaExt {
			return errors.New("injected metadata delete failure")
		}
		return os.Remove(path)
	}
	if err := h.pruneCurrent(); err == nil {
		t.Fatal("expected prune degradation")
	}
	for _, ext := range []string{historyExt, historyMetaExt} {
		if _, err := os.Stat(filepath.Join(h.dir, oldest+ext)); err != nil {
			t.Fatalf("pair member %s was removed after metadata failure: %v", ext, err)
		}
	}
}

func TestManagedHistorySnapshotUsesPublishedCandidateRetention(t *testing.T) {
	// Managed history finalization runs only after the apply reaches a terminal
	// committed result. Therefore the candidate retention is already published;
	// the pre-apply rollback snapshot created by that same apply participates in
	// the candidate policy, never a speculative pre-Publish policy.
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	s := &Server{hist: h}
	if !h.setRetention(1) {
		t.Fatal("3 -> 1 must be a tightening")
	}
	previous := []byte("[global]\nlog_level = \"info\"\n")
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, ConfigApplyResult{
		ApplyID: "closure-retention",
		OK:      true,
		Mode:    "hot",
	}, previous)
	if err != nil || id == "" {
		t.Fatalf("managed snapshot id=%q err=%v", id, err)
	}
	entries, err := h.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("candidate retention did not govern same-apply snapshot: %+v want only %s", entries, id)
	}
}

func TestHistoryRetentionConcurrentSnapshotGetListPruneUpdateRaceClean(t *testing.T) {
	h := newHistory(t.TempDir(), 20)
	seedHistoryWithMeta(t, h, 5)
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				h.setRetention((worker+i)%8 + 1)
				id, _, _ := h.snapshotWithMeta([]byte("value = 1\n"), &HistoryMetadata{})
				_, _ = h.list()
				if id != "" {
					_, _ = h.get(id) // deletion races are an allowed not-found result
				}
				_ = h.pruneCurrent()
			}
		}(worker)
	}
	wg.Wait()
}
''')

# Explicit whole-candidate mixed transaction regression.
rep(
    "internal/lifecycle/final_tranche_test.go",
    'import "testing"\n',
    'import (\n\t"testing"\n\n\t"jul/internal/config"\n)\n',
)
rep(
    "internal/lifecycle/final_tranche_test.go",
    '''\t}
}
''',
    '''\t}
}

func TestHistoryRetentionMixedWithDirectoryRemainsWholeCandidateRestart(t *testing.T) {
\tbefore := &config.Config{Admin: config.AdminConfig{HistoryDir: "/history/a", HistoryKeep: 50}}
\tafter := &config.Config{Admin: config.AdminConfig{HistoryDir: "/history/b", HistoryKeep: 10}}
\tres, err := Classify(before, after, Live{})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif res.CanApplyHot || !res.CanStageRestart {
\t\tt.Fatalf("mixed history candidate must stage as a whole: %+v", res)
\t}
\tif !contains(res.HotReload, "admin.history_keep") || !contains(res.RestartRequired, "admin.history_dir") {
\t\tt.Fatalf("unexpected mixed history classification: hot=%v restart=%v", res.HotReload, res.RestartRequired)
\t}
}
''',
)

# ---- Console/API schemas -----------------------------------------------------
rep(
    "internal/admin/ui/src/api/client.ts",
    '''  preparation_failure: z.string().optional(),
  last_upload_rejection: z.string().optional(),
  rate_limit_read_per_min: z.number().int(),
''',
    '''  preparation_failure: z.string().optional(),
  last_upload_rejection: z.string().optional(),
  history_keep: z.number().int().nonnegative().optional().default(0),
  history_retention_health: z.string().optional().default("unknown"),
  rate_limit_read_per_min: z.number().int(),
''',
)
rep(
    "internal/admin/ui/src/api/client.ts",
    '''  audit_log_rotate_keep: z.number().int().nonnegative(),
  audit_sink: AuditSinkStatusSchema.optional(),
''',
    '''  audit_log_rotate_keep: z.number().int().nonnegative(),
  history_keep: z.number().int().nonnegative().optional().default(0),
  audit_sink: AuditSinkStatusSchema.optional(),
''',
)

# ---- documentation -----------------------------------------------------------
rep(
    "docs/hot-reload-strategy.md",
    '''## Current lifecycle baseline

At `main@5b00a26db7450000abf462b079c87c89201723d1`, the generated lifecycle
inventory contains 302 configurable leaves: 250 `hot_reload`, 37
`restart_required`, 8 `new_listener_only`, 4 `ignored_deprecated`, and 3
`validation_rejected_reserved`.

These numbers are descriptive, not a target score. The current counts are always
available from [the generated lifecycle reference](generated/config-lifecycle.md)
and may change as selected work lands.
''',
    '''## Current lifecycle baseline

The final bounded tranche was audited from
`main@bb95de16119102c8fd23e11c908f131d2323a6ab`, whose generated inventory was
302 configurable leaves: 253 `hot_reload`, 34 `restart_required`, 8
`new_listener_only`, 4 `ignored_deprecated`, and 3
`validation_rejected_reserved`.

After the bounded #106 implementation, the same 302 leaves are intentionally
classified as **256 `hot_reload`, 32 `restart_required`, 7 `new_listener_only`,
4 `ignored_deprecated`, and 3 `validation_rejected_reserved`**. The three live
promotions are `rate_limit.max_conns`, `admin.history_keep`, and
`servers.*.tls.acme.ocsp_stapling`; no structural/security-heavy field was
promoted merely to improve a percentage.

These numbers are descriptive, not a target score. The authoritative current
counts remain the generated lifecycle reference and machine registry.
''',
)
rep(
    "docs/hot-reload-strategy.md",
    '''## Selected final gaps — 2026-09-11

A post-#160 source audit and peer review selected two final runtime-dynamics
investments. Both are now implemented: #99 makes
`observability.tracing.sample_ratio` hot while the tracing pipeline identity remains
startup-bound, and #94 makes `egress.enabled`/`egress.allow` hot only after proving
generation correctness across every auxiliary consumer and reusable pool.

| Gap | Current lifecycle | Implemented contract | Evidence | Residual risk |
| --- | --- | --- | --- | --- |
| `observability.tracing.sample_ratio` (#99) | `hot_reload` | Atomic root-ratio update inside one stable provider/exporter pipeline | deterministic sampler/concurrency/local-OTLP gates | Low–medium |
| `egress.enabled`, `egress.allow` (#94) | `hot_reload` | Immutable Prepare→Publish egress generations across auth, Consul/K8s, WASM and ACME/OCSP; old H1/H2 pools/workers cannot serve newly admitted work | H1 + real H2 isolation, redirects, worker fencing, race/leak/security matrices and ≥90% added-statement gate | bounded old-generation drain only |
''',
    '''## Delivered final bounded tranche — 2026-09-14

The source audit selected only transitions with a clean ownership seam. #99 and
#94 landed first; #106 then closes the programme with three deliberately small
live policies and no listener/provider-generation expansion.

| Gap | Final lifecycle | Implemented contract | Architectural cost |
| --- | --- | --- | --- |
| `observability.tracing.sample_ratio` (#99) | `hot_reload` | Atomic root-ratio update inside one stable provider/exporter pipeline | small/two-way |
| `egress.enabled`, `egress.allow` (#94) | `hot_reload` | Immutable Prepare→Publish egress generations across all Boundary-C consumers | justified high-cost security tranche |
| `rate_limit.max_conns` (#106) | `hot_reload` | Stable listener-owned admission limiter; cap changes affect new admissions only and never terminate admitted connections | small/two-way implementation; admission semantics are a higher-cost compatibility contract |
| `admin.history_keep` (#106/#159) | `hot_reload` | Atomic scalar retention on the existing history backend; tightening prunes only after Publish and failure is advisory | small/two-way; directory identity remains restart-bound |
| `tls.acme.ocsp_stapling` (#106) | `hot_reload` | Stable OCSP wrapper + atomic enable policy around the existing ACME provider/cache | small/two-way; no ACME manager replacement |
''',
)
# Replace the old remaining-gaps paragraph with the actual closure decision set.
rep(
    "docs/hot-reload-strategy.md",
    '''## Why the remaining structural gaps are different

This selection does not authorize universal hot reload. Listener protocol mode,
admin-listener relocation, cache backend identity, ACME account/cache identity,
history backend replacement and similar structural transitions may remain
restart-bound when their operating frequency is low and a correct live handover
would add disproportionate permanent complexity.

A red `restart_required` row is therefore not automatically technical debt. It
is debt only when the value/risk analysis says the restart boundary no longer
meets the product's operational contract.
''',
    '''## Final disposition of the remaining structural gaps

The runtime-dynamics programme is **finished, not paused**. The final source
audit makes an explicit distinction between a potentially useful feature that
loses today's value/complexity contest and a restart boundary that is itself the
preferred architecture.

**Deferred for the current programme:**

- **#93 cache backend identity (`cache.enabled`, `cache.disk_path`)** — route-level
  enablement and scalar cache policy are already hot; backend/filesystem
  generations are not justified now. Revisit if operators repeatedly require
  backend/path changes without restart or reusable state-backend generation
  infrastructure emerges.
- **#101 TLS minimum-version / mTLS policy dynamics** — current TCP and HTTP/3
  already share complete mTLS policy; the stale parity concern is resolved.
  Revisit only if live security-policy tightening becomes a product requirement
  and Jul gains reusable H1/H2/H3 connection-epoch plus session invalidation.
- **#103 broader ACME runtime policy** — HTTP-01 and TLS-ALPN-01 are already
  exclusive/correct, #94 solved ACME/OCSP egress generations, and #106 makes
  OCSP stapling itself hot. Revisit if domain/challenge/provider changes become
  operationally frequent or reusable ACME-manager generation infrastructure is
  justified elsewhere.

**Retained as intentional restart boundaries:**

- **#97 `admin.enabled` / `admin.listen`** — management-plane listener identity,
  self-lockout and dual-endpoint handover are structural and rare.
- **#102 `http3.enabled`** — only UDP/QUIC listener existence remains; Alt-Svc
  max-age is already hot via `DynamicAltSvc`.
- **#104 ACME account/issuer/email/cache identity** — restart provides a useful
  ownership boundary for private account state, issuer/rate-limit domain and
  certificate-cache ownership.
- **#105 TLS/plaintext + h2c transitions** — changing how an already-bound socket
  interprets bytes would require a permanent raw-listener supervisor.
- **#159 `admin.history_dir`** — retention-only is complete; storage relocation
  stays restart-bound. Revisit only if operators need live storage relocation
  and safe filesystem-generation infrastructure exists for another reason.

A `restart_required` row is therefore not automatically technical debt. The
programme intentionally avoids connection epochs, listener supervisors, ACME
manager generations and filesystem generations unless future operational demand
pays for their permanent complexity.
''',
)
rep(
    "docs/hot-reload-strategy.md",
    '''#99 — sample_ratio-only hot reload COMPLETE
  ↓
#94 — generation-correct egress hot reload COMPLETE
  ↓
explicit retain/defer decisions for remaining gated fields
  ↓
#106 — integrated runtime-dynamics closure
  ↓
#88 — portfolio closure
''',
    '''#99 — sample_ratio-only hot reload COMPLETE
  ↓
#94 — generation-correct egress hot reload COMPLETE
  ↓
#106 — max_conns + history retention + OCSP policy; final dispositions
  ↓
#88 — portfolio closure; runtime-dynamics programme COMPLETE
''',
)
rep(
    "docs/hot-reload-strategy.md",
    '''The small tracing change goes first because it can close independently without
introducing tracing-provider generations. Egress follows as the final large
security-sensitive runtime transition so #106 can certify the complete selected
tranche once, rather than repeatedly reopening integrated evidence.
''',
    '''The final boundary is intentional: future hot-reload work requires a new
operational value signal or architectural leverage. Jul does not continue toward
100% lifecycle coverage for its own sake.
''',
)

# TLS/ACME stale #100 and OCSP claims.
rep(
    "docs/tls-acme.md",
    '| Static cert **hot reload** | ❌ | current static file changes remain restart-bound; see #100 |',
    '| Static cert **hot reload** | ✅ | `cert`/`key` path or file-content changes are preflighted and atomically swap the retained listener provider (#100) |',
)
rep(
    "docs/tls-acme.md",
    '| Static `cert`/`key` | **Not reloaded** — restart to pick up a new path or changed file contents until #100 lands. |',
    '| Static `cert`/`key` | **Hot reload** — candidate material is parsed during Prepare and the retained listener provider swaps atomically at Publish (#100). |',
)
rep(
    "docs/tls-acme.md",
    '| ACME enablement, domains, challenge, account, issuer, cache, or OCSP policy | **Not reloaded** — process-owned manager state requires planned restart. |',
    '| ACME enablement, domains, challenge, account, issuer, or cache | **Not reloaded** — process-owned manager identity/policy remains restart-bound or deferred. |\n| ACME `ocsp_stapling` | **Hot reload** — an atomic policy on the stable provider wrapper changes new certificate lookups without replacing the ACME manager or listener (#106). |',
)
rep(
    "docs/tls-acme.md",
    '''The autocert manager's domain allow-list, account, issuer, challenge, cache, and
OCSP policy are process-lifetime state. A candidate changing one of these values
must be reported as restart-required and must not partially publish only its hot
routing subset while claiming the complete candidate live.
''',
    '''The autocert manager's domain allow-list, account, issuer, challenge and cache
remain process-lifetime state. A candidate changing one of those values must be
reported as restart-required and must not partially publish only its hot subset.
`ocsp_stapling` is deliberately different: the manager/provider identity stays
stable while an atomic wrapper policy decides whether a new certificate lookup
uses the existing stapler/cache. Disabling starts no new OCSP refresh; in-flight
work may finish, and re-enabling may reuse still-valid cached state.
''',
)
rep(
    "docs/tls-acme.md",
    '- **ACME manager transitions are restart-bound.** Domain, account, issuer,\n  challenge, cache, and OCSP policy changes require planned restart.',
    '- **ACME manager transitions are restart-bound.** Domain, account, issuer,\n  challenge, and cache changes require planned restart/deferred manager work.\n  `ocsp_stapling` itself is hot and does not replace manager identity (#106).',
)

# Transactional contracts in reload semantics (append only current normative text).
with open("docs/reload-semantics.md", "a") as f:
    f.write(r'''

## Final bounded runtime-policy transitions (#106)

The final runtime-dynamics tranche adds three policy-only live transitions while
preserving the same whole-candidate transaction boundary.

### `rate_limit.max_conns`

- **Prepare:** canonical validation only; no live cap mutation.
- **Publish:** update each retained listener's stable Jul-owned admission limiter
  before the candidate configuration/runtime snapshot is published. Newly staged
  listeners were already built with the candidate effective cap.
- **Abort:** no cap mutation.
- **Retire/PostCommit:** none.

The cap governs **new admission**. Lowering it never closes admitted TCP/TLS/HTTP
connections; if active connections exceed the new finite cap, no new connection
is admitted until active drops below it. `0` is unlimited. The effective cap
continues to honor the existing `[rate_limit].enabled` master switch live.

### `admin.history_keep`

- **Prepare:** stage only the scalar retention value; no filesystem deletion or
  backend migration.
- **Publish:** atomically install retention on the existing history object before
  the candidate admin snapshot advertises it.
- **Abort:** live retention and files remain unchanged.
- **Retire/PostCommit:** a tightening runs a serialized prune. Failure is bounded
  advisory health (`prune_failed`) and never converts an applied config into a
  failed one.

Managed-apply history finalization happens after terminal commit, so the rollback
snapshot created by the same successful apply observes the **published candidate
retention**. A candidate that also changes `admin.history_dir` remains a complete
staged-restart candidate; Jul never partially applies only `history_keep`.

### `servers.*.tls.acme.ocsp_stapling`

- **Prepare:** validation only; ACME manager/account/cache/provider identity is
  unchanged.
- **Publish:** atomically toggle the stable OCSP provider wrapper before the new
  config snapshot is visible.
- **Abort/Retire/PostCommit:** no manager or listener resource work.

Disabling prevents new lookups from initiating a refresh through the stapler;
already-started refresh work may finish. Cached staple state remains owned by the
stable wrapper and may be reused when re-enabled. Broader ACME domain, challenge,
account, issuer and cache transitions remain outside this seam.
''')

# Known limitations: preserve intentional boundaries explicitly.
with open("docs/known-limitations.md", "a") as f:
    f.write(r'''

## Intentional runtime-dynamics boundaries after #106

The hot-reload programme deliberately stops short of universal dynamic
configuration. The following are intentional boundaries, not accidentally
unimplemented live swaps: cache backend identity/path (#93 deferred), admin
listener identity (#97 retained), TLS minimum-version and mTLS policy tightening
(#101 deferred pending connection/session epochs), HTTP/3 listener existence
(#102 retained; Alt-Svc max-age is already hot), broader ACME manager policy
(#103 deferred), ACME account/issuer/cache identity (#104 retained),
TLS/plaintext+h2c socket interpretation (#105 retained), and history directory
relocation (#159 retained). `admin.history_keep` is hot; `admin.history_dir` is
not. See [hot-reload-strategy.md](hot-reload-strategy.md) for objective revisit
triggers.
''')

with open("docs/status.md", "a") as f:
    f.write(r'''

## Runtime-dynamics programme closure (2026-09-14)

The bounded hot-reload programme is complete at the lifecycle-design level. The
final generated inventory is 302 leaves: **256 hot reload, 32 restart required,
7 new-listener-only, 4 ignored/deprecated, 3 validation-rejected/reserved**.
This is an intentional architecture inventory, not a score: structural listener,
resource-identity and security-tightening changes retain explicit restart/defer
boundaries. Future hot-reload work requires a new operational value signal or
reusable architectural leverage; it is not an automatic continuation toward
100%.
''')

# Changelog record at top of Unreleased.
rep(
    "CHANGELOG.md",
    '## [Unreleased]\n\n',
    '''## [Unreleased]

- **HR-17 / #106 — bounded runtime-dynamics closure.** `rate_limit.max_conns` now hot-applies through a stable listener-owned connection-admission limiter (new admissions observe the published cap; admitted connections are never terminated), `admin.history_keep` publishes atomically on the existing history backend with tightening prune only after Publish and advisory failure semantics, and ACME `ocsp_stapling` hot-applies through a stable provider wrapper without replacing manager/account/cache/listener identity. The generated 302-leaf lifecycle inventory is now 256 hot / 32 restart / 7 new-listener / 4 ignored / 3 reserved. The programme explicitly defers #93/#101/#103 and retains restart boundaries for #97/#102/#104/#105 plus `admin.history_dir` from #159 rather than introducing backend generations, connection epochs or listener supervisors solely to increase hot-reload coverage. Static TLS `cert`/`key` documentation is also corrected to the already-delivered #100 hot-reload behavior.
''',
)

print("issue106 final refinements applied")

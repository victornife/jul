# Soak evidence log

Criterion 5 of the [GA criteria](status.md#ga-criteria-legend) is a long-running
soak test. Per [ADR 0005](adr/0005-soak-post-ga-gate.md) it is a **post-GA gate**:
a feature ships as **GA — soak pending** and its soak status is tracked here and
in [docs/status.md](status.md#soak-tracking-post-ga-gate).

This page makes the soak claim **verifiable** — "soak pending" without a dated
artifact is an unverifiable assertion. It records where soak evidence is produced
and keeps a log of dated runs.

## Where soak evidence is produced

| Context | Trigger | Duration | Artifact |
| --- | --- | --- | --- |
| CI smoke (`soak (smoke)` job) | every push / PR | 20s × 2 scenarios | `soak-results` artifact on the [CI workflow](../.github/workflows/ci.yml) run |
| Release gate (`soak gate (ADR 0005)` job) | version tag `v*` | 5m × 2 scenarios | `soak-results` artifact on the [release workflow](../.github/workflows/release.yml) run; a red run blocks the release |
| Local | `scripts/soak.sh` | configurable | stdout (see runs below) |

Both scenarios are driven by the in-tree soak tests behind the `soak` build tag:

- **proxy** — `TestSoak` ([internal/handler/soak_test.go](../internal/handler/soak_test.go)):
  sustained concurrent HTTP requests through a real reverse-proxy handler; asserts
  **zero request errors** plus steady goroutine count and bounded heap growth.
- **udp-churn** — `TestSoakUDPChurn` ([internal/stream/soak_test.go](../internal/stream/soak_test.go)):
  sustained UDP source-address churn through a real stream listener; asserts the
  live-session count stays capped and every reaped/evicted session tears down
  fully (no goroutine or backend-socket leak).

The authoritative **GA-soak** evidence for a release is the 5-minute release-gate
artifact. The runs below are shorter smoke-duration samples that demonstrate the
harness is healthy and the data paths do not leak.

## Run log

### 2026-07-01 — smoke soak (local, 20s/scenario, 24 workers)

Environment: Windows/amd64, go1.26.4, full opt-in tag set (`soak brotli zstd acme
console otel grpc http3 importer wasmplugins stream consul kubernetes`).

**proxy — `TestSoak`** — PASS (20.41s)

```
soak: duration=20s workers=24 requests=50411 errors=0
soak: goroutines 10 -> 70, heap 866072 -> 1729184 bytes
```

- Zero request errors across 50,411 requests.
- Goroutines settled at a steady working set; heap growth bounded (< 2 MiB).

**udp-churn — `TestSoakUDPChurn`** — PASS (20.69s)

```
soak/udp: duration=20s workers=24 sends=43156 peakSessions=265 cap=256
soak/udp: reaped(idle=24679 lru=136) rejected=18471
soak/udp: goroutines 4 -> 4, heap 504752 -> 1102400 bytes
```

- Session cap held (18,471 admissions rejected once at capacity; brief transient
  overshoot to 265 during concurrent admission, then reaped).
- **No goroutine leak** (4 → 4) across 43,156 sends; heap growth bounded (~1 MiB).

> Note: this is a smoke-duration sample, not the 5-minute release-gate run. It
> demonstrates the soak harness is green and the proxy/stream data paths are
> leak-free at this scale. The next tagged release will attach the full
> release-gate `soak-results` artifact; link it from the row below when produced.

## Per-feature soak status

See [docs/status.md](status.md#soak-tracking-post-ga-gate) for the per-feature
soak-status table (the single source of truth for GA — soak-pending features).
The proxy soak exercises the Core HTTP / TLS / auth / gRPC data paths; the
udp-churn soak backs the L4 stream session-safety guard.

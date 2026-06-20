# ADR 0001 — Implementation language strategy (Go-first, native code at the edges)

- **Status:** Accepted
- **Date:** 2026-06-21
- **Deciders:** Jul.IA maintainers
- **Applies to:** all existing and future features

## Context

Jul.IA is a single, statically-linked Go binary with **no cgo** (`CGO_ENABLED=0`),
trivial `GOOS`/`GOARCH` cross-compilation, and a *lean-by-default* build whose
heavier features are opt-in behind build tags. That property is deliberate and
recurring: the project chose [wazero](https://github.com/tetratelabs/wazero)
(pure-Go WebAssembly), `modernc.org/sqlite` (pure-Go, CGO-free), pure-Go image
codecs, and a hand-rolled PROXY-protocol parser specifically to avoid C
toolchains and preserve the one-binary story.

A natural question for a performance-sensitive proxy/gateway is whether the most
CPU-intensive paths should be written in a systems language such as **Rust**.
This ADR records the decision and the rule future features follow.

## Decision

**Default to pure Go for everything.** Introduce native (Rust) code only for a
small set of genuinely CPU-bound transforms, and only across a
**process or WebAssembly boundary — never via cgo/FFI in the main binary.**

Integration model preference, in order:

1. **WebAssembly module** (Rust → `wasm32-wasip1`, run in wazero) — the default
   "native-speed escape hatch". Sandboxed, hot-reloadable, **no toolchain
   change**, single binary preserved. This is already how Jul.IA's plugin system
   works (`wasmplugins` tag), so "performance-critical in Rust" is *already*
   achievable as a plugin.
2. **Out-of-process sidecar** (Rust service over a unix socket / gRPC) — when a
   whole-payload transform amortizes a network hop (e.g. image encoding). Keeps
   the Go binary clean, gives crash isolation and independent scaling, and lives
   behind an opt-in build tag so the lean binary is untouched.
3. **cgo / static FFI** — **avoided.** It breaks `CGO_ENABLED=0`, explodes the
   cross-compile matrix and CI, and introduces GC↔FFI lifetime hazards;
   blocking cgo calls can also starve the Go scheduler. Reserved for a
   hypothetical sub-microsecond, millions-of-calls-per-second kernel that a
   gateway realistically never has.

**Process rule:** *profile before porting.* A path must be shown by benchmarks
(in the perf gate) to be CPU-bound and dominated by a pure-Go library before any
native alternative is considered. Reach for Profile-Guided Optimization (PGO),
`sync.Pool`, zero-copy, and stdlib asm fast-paths first.

## Rationale

- **A gateway is I/O-bound, not CPU-bound.** The hot path is accept → parse →
  route → dial → `io.Copy`. Go's netpoller is excellent here; Rust would yield
  near-zero throughput gain on the proxy core while adding large complexity.
- **FFI overhead often cancels the win.** Per-crossing cost (tens–hundreds of ns,
  plus scheduler constraints) erases the compute benefit for fine-grained calls.
- **The single static binary is Jul.IA's headline differentiator.** cgo trades it
  away for marginal gains on a workload that rarely needs them. (Tell: this repo
  can't even run `-race` locally because there is no C toolchain on PATH — that
  friction would become every contributor's daily build experience.)
- **WASM already gives us memory-safe, native-ish extensions** without touching
  the build story — users can write Rust guardrails/transforms/filters today.

## Per-feature guidance

CPU-bound transforms on the request/response body are the only real candidates.
Everything orchestration- or I/O-shaped stays pure Go.

| Feature / path | CPU-bound | Native win | Verdict | Go-native first choice |
| --- | --- | --- | --- | --- |
| Core proxy / static / router | No | Negligible | **Go** | PGO, `sync.Pool`, zero-copy |
| Compression (gzip/br/zstd) | Yes | Low–med | **Go** | `klauspost/compress` (SIMD-tuned) |
| TLS / mTLS / PQ-TLS | Yes | Low | **Go** | stdlib crypto asm fast-paths |
| WAF regex eval (Y2-06) | Yes | Med–high | **Go, benchmark** | Coraza (pure-Go); native only if proven hot |
| Image optimization webp/avif (Y4-07) | Yes (heavy) | High | **Sidecar candidate** | pure-Go codecs; native encoder behind `imageopt` tag |
| Semantic-cache HNSW / vectors (Y4-02) | Yes | Med | **Go, revisit** | `coder/hnsw` (pure-Go) |
| Tokenizer / BPE counting (Y4-03) | Yes | Med | **Go, revisit** | pure-Go BPE |
| JA3 / PoW verify (Y5-04) | Yes | Med | **WASM candidate** | Go asm or a WASM module |
| WASM runtime (Y2-02, Y4-06) | — | — | **Solved** | wazero; *users* bring Rust→WASM |
| Control plane / Cloud / mesh / GSLB (Y3/Y5) | No | None | **Go** | pure orchestration/I/O |

The strongest (and currently only sanctioned) native candidate is **on-the-fly
image optimization (Y4-07)**, to be delivered — if benchmarks justify it — as an
**optional out-of-process sidecar behind the `imageopt` tag**, leaving the lean
binary and the no-cgo guarantee intact.

## Consequences

**Positive**

- Single static binary, `CGO_ENABLED=0`, and clean cross-compilation are
  preserved for the default and full builds.
- One toolchain, one CI story, one supply-chain (govulncheck/SBOM) for the binary.
- Native speed remains *available* — as WASM plugins (now) or a tagged sidecar
  (later) — without polluting the core.
- Lower contributor onboarding cost; the codebase stays single-language.

**Negative / trade-offs**

- A few CPU-heavy transforms may run slower in pure Go than a native equivalent
  would, until/unless a sidecar is justified by benchmarks.
- The sidecar path, when used, adds an out-of-process dependency and a small
  per-call hop for that feature (isolated behind a build tag).

## Alternatives considered

- **Broad Rust rewrite of hot paths via cgo** — rejected: erodes the
  single-binary/no-cgo differentiator and complicates the build for little gain
  on an I/O-bound workload.
- **Stay 100% Go with no native escape hatch ever** — rejected as too absolute:
  image/avif encoding is a credible future case where a tagged sidecar is the
  right call.

## Review triggers

Revisit this ADR if any of the following hold, backed by perf-gate data:

- A pure-Go library is demonstrably the throughput bottleneck for a shipped
  feature (e.g. avif encode, WAF body scan, vector search at scale).
- WASM compute overhead (~1.2–3× vs native) becomes material for a hot transform.
- A native sidecar is introduced — it must arrive behind its own build tag with
  the lean binary unchanged.

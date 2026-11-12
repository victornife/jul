# #700 I2 plugin request path — evidence

Raw evidence for
[the I2 design note and review](../../2026-11-12-700-i2-plugin-request-path.md), section 3.
It was recorded on 2026-11-12 with the maintained harness in
[`scripts/inspection-assessment/`](../../../../scripts/inspection-assessment/README.md) under
plan **v3** (`inspection-assessment-plan/v3`, sha256
`5ce583d03555dddb1ecde57cd3e03c0edff0af95cae73a2f62efba7676314bd8`). Every run is kept, including
failed, invalid and OOM-killed runs. Later runs supplement earlier ones and never replace them.
v1 (I5) and v3 results are never merged.

## Identities

| Label | Source | Toolchain | sha256 | Build |
| --- | --- | --- | --- | --- |
| `base` | `858590dc7c430d24cd33b096a81ca6051591b427` | go1.27.3 | `520febcbad5669da0ea8ad98e29f0bb78304817511cd7ed63bfb48e9d63ea52f` | The I5 ADR-baseline build (historical toolchain). |
| `base4` | `858590dc7c430d24cd33b096a81ca6051591b427` | go1.27.4 | `e0f105cbae69939b5b91a5bbfd7165f993fcb181c4d813bfa241486ab3a2291b` | The I5 same-toolchain rebuild. It is `CGO_ENABLED=1`, dynamic, so it differs from the others in linkage too. Labelled separately; it never replaces `base`. |
| `main` | `d8d0520b1b57ef8c0df08e8902b407856f2d47cd` (pre-I2 main) | go1.27.4 | `65a0f8b9d42909b048fe3fe00ecda7dbf17ae28320bdef28bd82a1ea2f0add37` | (1) with `VERSION=d8d0520b` |
| `cand` | `b1a670fe519f64f9d28cd2c64f6263a06ec7399b` (I2) | go1.27.4 | `3edee005bc08754283e77c8b6f1688204d24927c2662697f2f82a1eccbf54e73` | (1) with `VERSION=b1a670fe519f64f9d28cd2c64f6263a06ec7399b` |
| harness | `b1a670fe`, `scripts/inspection-assessment` | go1.27.4 | `732e24feef6f52b58e2501751bb0e99bcfe229f6bbb86719f587affbdfc2291c` | `CGO_ENABLED=0 go build -trimpath` |
| `inspect-load` guest | `examples/plugins/inspect-load` at `b1a670fe` | go1.27.4 | `27b016a775020dca6cea66fdb98a836d1afbf056b2ac9883c142e4969c41034a` | (2) |
| `inspect-nobody` guest | `examples/plugins/inspect-nobody` at `b1a670fe` | go1.27.4 | `5df6763d8a9fc4ff15d0931376cade46f6d50238fcde412d0a341400f3afa22e` | (2) |

1. From a `git archive` export of the commit, outside any measured tree:
   `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" -ldflags "-s -w -X main.version=$VERSION" ./cmd/jul`.
   Rebuilding `main`, `cand` and the harness this way reproduced the sha256 values above.
2. `GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared`. Rebuilding
   from the clean export was bit-identical.

The PR head adds `e1c6357c` (the late fetch-body waiter fix, found by the security review) after
the measured `b1a670fe`. That change touches only the plugin `fetch` host function, which no
campaign fixture calls, so the measured binary is behaviourally identical to the head on every
measured path.

Host: arm64 WSL2 (kernel `6.18.54.1-microsoft-standard-WSL2`), 12 CPUs. Jul runs in a verified
envelope of 2 vCPU (CPUs 0–1, quota 200%), 768 MiB, no swap, `GOMAXPROCS=2`. The origin is on
CPUs 2–3 and the client on CPUs 4–9, both outside the envelope. The host shows time-correlated
throughput drift that moves every target together (section 3.4 of the review).

## Layout

| Path | Content |
| --- | --- |
| `runs/i2-normal/` | Frozen campaign: suite `i2-normal`, `base`/`main`/`cand`, 3 repetitions (u1b keeps its planned 5). |
| `runs/i2-plugin/` | Frozen campaign: suite `i2-plugin`, `main`/`cand`. |
| `runs/i2-deadline/` | Frozen campaign: suite `i2-deadline`, `main`/`cand`. |
| `runs/i2-normal-supplement/` | 3 more repetitions of the `none`/`wasm` normal cells (5 for u1b), adding `base4`. |
| `runs/i2-p3-repeat/` | 5 repetitions of `p3-nobody-plugin-h1/wasm`, investigating the 704 MiB frozen peak. |
| `runs/i2-p1-none-repeat/` | 3 repetitions of `p1-large-body-plugin-h1/none`, investigating a low single result. |
| `runs/pre-freeze-*` | Development and sizing runs before plan v3 was frozen, under the plan and harness hashes in each manifest. They are findings and design input, not campaign results. |
| `pprof/` | Heap (`heap-*`) and goroutine (`gr-*`) captures polled from the admin pprof endpoint during the pre-freeze p4 reload investigation, and `m-*`/`s-*` metric and status polls. |
| `attachments/` | Files captured beside a run that its `SHA256SUMS` does not list: a console log, and the `memory.stat` and heap captures of the no-backoff socket-buffer OOM. `bench/` holds the interleaved plugin benchmarks, `main` vs head. |
| `summary/` | Summaries regenerated from `runs/` by the committed harness, and `paired-ratios.md`. |
| `logs/` | Console logs of the scripts in `scripts/`. |
| `scripts/` | The exact campaign, supplement and investigation scripts, `paired-ratios.py` and `regenerate-summaries.sh`. The pre-freeze runs were started by hand; their manifests record the selection, targets and identities. |

The scripts and manifests record the absolute paths of the measurement host (`/home/victornf/...`).
Each manifest identifies binaries, guests and the harness by sha256, so those paths are provenance
only. The admin token in the configurations is the harness's synthetic, loopback-only constant.

## Verify and regenerate

From the repository root:

```sh
(cd docs/reviews/evidence/2026-11-12-700-i2 && sha256sum -c --quiet SHA256SUMS)
go build -o /tmp/ia ./scripts/inspection-assessment
docs/reviews/evidence/2026-11-12-700-i2/scripts/regenerate-summaries.sh /tmp/ia /tmp/i2-summary
diff -r docs/reviews/evidence/2026-11-12-700-i2/summary /tmp/i2-summary
```

`regenerate-summaries.sh` first runs `verify` on every run directory, including the nested
pre-freeze runs. `paired-ratios.md` is a descriptive drift check, not an ADR verdict rule.

`SHA256SUMS` in this directory covers every file here except itself.

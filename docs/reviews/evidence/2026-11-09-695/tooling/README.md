# #695 assessment tooling (archived source)

These files are the exact sources used for the #695 runs. They are stored as
`.go.txt`, so they are not compiled, vetted or coverage-gated: they are
single-assessment instruments, not maintained repository tooling. Byte-identical
to the files committed with the frozen plan (`70c7b4ce`); see `SHA256SUMS`.

| File | Original path | Purpose |
| --- | --- | --- |
| `waf-wasm-assessment.go.txt` | `scripts/waf-wasm-assessment.go` (`//go:build ignore`) | Load harness: origin, envelope launch, scenarios, sampling, `results.jsonl` |
| `inspect-load.go.txt` | `examples/plugins/inspect-load/main.go` | Synthetic `jul-abi/v2` guest (`read_body` + response `subscribe`) |

## Reproduce

From a checkout of the assessed runtime (`453e62375bdce0302756ff386637832d5fa35bad`)
or a later commit, restore the sources to their original paths and build:

```sh
T=docs/reviews/evidence/2026-11-09-695/tooling
(cd "$T" && sha256sum -c SHA256SUMS)
cp "$T/waf-wasm-assessment.go.txt" scripts/waf-wasm-assessment.go
mkdir -p examples/plugins/inspect-load
cp "$T/inspect-load.go.txt" examples/plugins/inspect-load/main.go
```

Then follow the report's reproduction section
([../../../2026-11-09-695-waf-wasm-resource-assessment.md](../../../2026-11-09-695-waf-wasm-resource-assessment.md#6-reproduction-and-supplemental-evidence)).
On a later commit, config, admin-API or SDK drift may need fixing first; record
any such change with the new results. Remove the restored copies before
committing unless they are promoted with tests (see #700).

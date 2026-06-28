# Plugin ABI compatibility policy

Jul.IA's WASM plugins talk to the host through a versioned ABI: the `jul-abi/v1`
contract of host import functions (the `jul` module) and the guest export
`handle_request`. This document is the stability contract for that ABI and the
test suite that enforces it.

See [plugins.md](plugins.md) for how to write and configure plugins; this page is
the compatibility guarantee only.

## Versioning

The ABI identifier is `<name>/v<major>` (`jul-abi/v1`). It is a major version,
not a SemVer triple: any binary-incompatible change is a new major (`jul-abi/v2`)
loaded behind a new entry in `abiRegistry`, never an in-place edit of v1.

| Change | Allowed in v1? |
| --- | --- |
| Add a new host import function | ✅ additive (old guests ignore it) |
| Add a new guest export the host calls only when present | ✅ additive |
| Loosen a numeric return-code meaning (new negative code) | ✅ additive |
| Rename a host function | ❌ new major |
| Change a function's parameter or result arity/types | ❌ new major |
| Remove a host function | ❌ new major |
| Repurpose an existing return code | ❌ new major |

A guest compiled against v1 must keep running unchanged on every later v1 host.

## What is pinned

The host-function **surface** — every function name with its parameter and
result arity — is pinned by a golden snapshot at
[testdata/plugins/abi-v1.golden](../testdata/plugins/abi-v1.golden). The
`TestABIV1Golden` test reconstructs the live surface from the instantiated `jul`
module and fails on any drift, so a rename, retype, or removal cannot merge
silently. An *additive* change regenerates the golden intentionally:

```bash
UPDATE_ABI_GOLDEN=1 go test -tags wasmplugins -run TestABIV1Golden ./internal/plugins/
```

## Prebuilt-guest compatibility

The committed modules under `testdata/plugins/*.wasm` are guests built against the
shipped SDK; the plugin tests run them against the current host on every CI run.
They are the regression fleet: a host change that breaks a previously compiled
guest fails the suite. Keep old `.wasm` fixtures in place across releases so
backward compatibility stays covered.

## Guarantees

- Within `jul-abi/v1`, host functions are only ever added; existing names,
  parameters, and return codes are frozen.
- A guest binary built for v1 needs no recompilation across v1 host upgrades.
- Breaking changes ship as a new ABI id; both can be registered so guests
  migrate at their own pace.

## CI

The golden and prebuilt-guest suites run under the `wasmplugins` tag, which is in
`FULL_TAGS`, so they execute in the standard test, race, and coverage jobs.

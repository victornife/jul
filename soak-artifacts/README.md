# Soak evidence retention (JUL-AUD-018)

This directory holds evidence from real soak/burn-in runs, one subdirectory per
run: `soak-artifacts/<YYYY-MM-DD>-<scope>/`. Every subdirectory carries a
`MANIFEST.md` (copy `MANIFEST.template.md`) so a run is reproducible and
auditable later, not just a claim in `docs/soak-evidence.md`.

## Why

ADR 0005 makes a long-running soak a release gate. A gate whose evidence is
not reproducible is a claim, not a gate — see
`docs/audit/2026-09-16-pre-soak-readiness-audit.md` (JUL-AUD-018). Loose
`.log` files with no SHA, config copy or environment record cannot answer
"what exactly was running when this passed?" a year later; a manifest can.

## Creating a new run's directory

```sh
scripts/soak-manifest-init.sh <scope>
# e.g. scripts/soak-manifest-init.sh resilience-24h
# writes soak-artifacts/<YYYY-MM-DD>-resilience-24h/MANIFEST.md
```

The script pre-fills the build SHA, `go version`, host spec, and
`jul capabilities -json` (if a `jul` binary is on `$PATH` or `$JUL_BIN` is
set). Fill in the remaining sections by hand as the run proceeds, and append
metric snapshots / pprof captures / the event log into the same directory.

## What a manifest must record

See `MANIFEST.template.md` for the exact structure. At minimum:

- exact build SHA (`git rev-parse HEAD`) and whether the tree was clean;
- `go version` and OS/kernel;
- `jul capabilities -json` output;
- every config file used, copied verbatim into the run directory;
- every command line executed (build, backends, server, load generator(s));
- a timestamped event log of every apply/rollback/restart/injected fault;
- metric snapshots (Prometheus scrape dump or exported CSV) at meaningful
  points, not only at the end;
- `pprof` heap/goroutine captures at fixed intervals (T0, T+2h, T+12h, T+24h
  for a 24h run — scale to the run's actual duration);
- a written conclusion: pass/fail against the run's stated exit criteria, and
  any anomaly observed.

## What NOT to do

- Do not overwrite a previous run's directory; each run gets its own dated
  directory even if it repeats an earlier scope.
- Do not summarize a run only in `docs/soak-evidence.md` without a
  corresponding manifest directory — the changelog entry should link to it.
- Do not commit secrets: redact tokens/credentials from copied configs and
  logs before committing (`internal/redact` already masks resolved
  `${env:}`/`${file:}`/`${secret:}` values in `jul`'s own logs, but a
  hand-copied config file still carries literal values you set for the run).

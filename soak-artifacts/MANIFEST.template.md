# Soak run manifest

> Copy this file to `soak-artifacts/<YYYY-MM-DD>-<scope>/MANIFEST.md` and fill
> in every section. `scripts/soak-manifest-init.sh <scope>` does the copy and
> pre-fills the fields it can determine automatically.

## Identity

| Field | Value |
| --- | --- |
| Scope | _e.g. "final soak", "resilience-24h", "cache-recert"_ |
| Date started (UTC) | |
| Date ended (UTC) | |
| Operator | |
| Build SHA | |
| Working tree clean at build time? | |
| `go version` | |
| OS / kernel | |
| Host spec (CPU, RAM, disk) | |
| `jul capabilities -json` | ```<paste here>``` |

## Configuration

List every config file used, and paste its exact content (or attach a copy in
this directory alongside this manifest).

| File | Copied to |
| --- | --- |
| | |

## Commands executed

Paste every command line, in order, exactly as run (build, backends, server,
load generator(s), any fault-injection commands).

```sh

```

## Event log

A timestamped log of every apply, rollback, restart, and injected fault
during the run. One line per event.

```
<UTC timestamp>  <event>
```

## Metric snapshots

List where scrape snapshots / exported CSVs / dashboards for this run are
stored (attach in this directory, or link to a retained dashboard/TSDB
snapshot). Note the scrape interval used.

## pprof captures

| Timestamp | heap | goroutine |
| --- | --- | --- |
| T0 | | |
| ... | | |
| Tend | | |

## Exit criteria and result

Restate the run's exit criteria (see `docs/soak-procedures.md`) and record the
actual measured value for each — not just pass/fail.

| Criterion | Target | Measured | Pass? |
| --- | --- | --- | --- |
| | | | |

## Conclusion

Free-form: what this run proves, what it does not, any anomaly observed and
how it was investigated, and whether this run's evidence should be cited in
`docs/soak-evidence.md` (if so, add a dated entry there linking back to this
directory).

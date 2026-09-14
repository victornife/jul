#!/usr/bin/env python3
from pathlib import Path
import re


def replace(path, old, new):
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise SystemExit(f"anchor not found in {path}: {old[:80]!r}")
    p.write_text(text.replace(old, new, 1))


def regex_replace(path, pattern, replacement):
    p = Path(path)
    text = p.read_text()
    new, n = re.subn(pattern, replacement, text, count=1, flags=re.S)
    if n != 1:
        raise SystemExit(f"regex anchor not found/unique in {path}: {pattern[:80]!r} count={n}")
    p.write_text(new)

# Dedicated OTel documentation: runtime truth after #99.
regex_replace(
    "docs/otel.md",
    r'''1\. \*\*Tracing settings currently require a restart\.\*\* Changing\n   `\[observability\.tracing\]` after startup keeps the running tracer\. #99 is now\n   \*\*selected with reduced scope\*\* to make only `sample_ratio` hot-reloadable:\n   new root spans after Publish will use the new ratio while parent sampling\n   decisions remain authoritative, without rebuilding the provider/exporter\.\n   Until that implementation lands, `sample_ratio` remains `restart_required`\.\n   `enabled`, `endpoint`, `exporter`, `service_name`, and `insecure` remain\n   deliberately restart-bound in this tranche\. See\n   \[hot-reload strategy\]\(hot-reload-strategy\.md\)\.''',
    '''1. **Only the root sampling ratio hot-reloads (#99).**
   `observability.tracing.sample_ratio` is applied atomically inside the successful
   reload Publish transaction. A new root span created after that boundary uses
   the new ratio; local and remote parent sampling decisions remain authoritative,
   and an already-started trace cannot change its sampling decision. The process-
   lifetime `TracerProvider`, exporter, resource, propagator, tracer and global OTel
   registration are not rebuilt, flushed, shut down or re-registered by a ratio
   change. `enabled`, `endpoint`, `exporter`, `service_name`, and `insecure` remain
   deliberately `restart_required`. See [hot-reload strategy](hot-reload-strategy.md).'''
)

# Configuration reference prose.
regex_replace(
    "docs/configuration.md",
    r'''Tracing configuration is currently read once at boot; a reload keeps the\nrunning tracer and a restart applies tracing changes\. #99 is selected with\nreduced scope to make only `observability\.tracing\.sample_ratio` hot-reloadable\nwithout replacing the provider/exporter\. Until #99 lands, `sample_ratio` remains\nrestart-required; `enabled`, `exporter`, `endpoint`, `service_name`, and `insecure`\nremain deliberately restart-bound in the selected tranche\.''',
    '''The tracing provider/exporter pipeline is created once at boot, but #99 makes
`observability.tracing.sample_ratio` hot-reloadable. A successful Publish atomically
updates only the stable ParentBased sampler's root ratio; new root spans use the new
ratio while parent decisions and already-started traces remain unchanged. `enabled`,
`exporter`, `endpoint`, `service_name`, and `insecure` remain deliberately
restart-required and take effect only after a process restart.'''
)

# Reload semantics: replace the current tracing paragraph and generic restart list.
replace(
    "docs/reload-semantics.md",
    '''- **Tracing** — the provider/exporter pipeline is wired once at startup, so all
  tracing fields are currently restart-bound. #99 is selected with reduced
  scope to make only `observability.tracing.sample_ratio` hot-reloadable without
  rebuilding the provider/exporter; the remaining tracing fields stay
  restart-bound.''',
    '''- **Tracing** — the provider/exporter/resource/propagator/tracer pipeline is
  wired once at startup. `observability.tracing.sample_ratio` is hot (#99):
  successful Publish atomically swaps only the stable ParentBased sampler's root
  delegate before the candidate handler/config becomes reachable. New root spans
  use the new ratio; parent decisions and already-started traces do not change.
  `enabled`, `endpoint`, `exporter`, `service_name`, and `insecure` remain
  restart-bound.'''
)
replace(
    "docs/reload-semantics.md",
    '''  lifecycle, `admin.enabled`, `admin.listen`, admin history resources,
  tracing, ACME, and retained-listener bind settings) are **rejected at swap time**''',
    '''  lifecycle, `admin.enabled`, `admin.listen`, admin history resources,
  tracing pipeline identity fields other than `sample_ratio`, ACME, and retained-listener bind settings) are **rejected at swap time**'''
)

# Current strategy truth.
replace(
    "docs/hot-reload-strategy.md",
    '''A post-#160 source audit and peer review selected two additional investments.
Neither is hot in the current binary yet.''',
    '''A post-#160 source audit and peer review selected two additional investments.
#99 is now implemented: `observability.tracing.sample_ratio` is hot while the
tracing pipeline identity remains startup-bound. #94 remains the next selected
implementation and its egress fields stay restart-required until its complete
consumer/pool security matrix passes.'''
)
replace(
    "docs/hot-reload-strategy.md",
    '''| `observability.tracing.sample_ratio` (#99) | `restart_required` | Hot-update the root sampling ratio without rebuilding the provider/exporter |''',
    '''| `observability.tracing.sample_ratio` (#99) | `hot_reload` | Atomic root-ratio update inside one stable provider/exporter pipeline |'''
)
replace(
    "docs/hot-reload-strategy.md",
    '''#99 — sample_ratio-only hot reload
  ↓
#94 — generation-correct egress hot reload''',
    '''#99 — sample_ratio-only hot reload COMPLETE
  ↓
#94 — generation-correct egress hot reload NEXT'''
)

# Known limitations: #99 is no longer a future limitation.
regex_replace(
    "docs/known-limitations.md",
    r'''Two current restart boundaries are now \*\*selected for removal\*\*, but remain\nlimitations until their implementation PRs merge: #99 is reduced to\n`observability\.tracing\.sample_ratio` only \(\*\*S/M, 4–7 focused engineer-days\*\*\),\nand #94 selects generation-correct `egress\.enabled` / `egress\.allow`''',
    '''One selected restart boundary is now removed and one remains: #99 makes
`observability.tracing.sample_ratio` hot while leaving the tracing pipeline identity
fields restart-bound. #94 remains selected for generation-correct `egress.enabled` /
`egress.allow`'''
)

# Sample configuration comments must not claim the whole block is startup-only.
for cfg in ("server.toml", "dev-server.toml"):
    replace(
        cfg,
        '''# Tracing settings are fixed at startup: changing this block needs a restart
# (a config reload keeps the running tracer).''',
        '''# Tracing pipeline identity settings are fixed at startup, but sample_ratio
# hot-reloads atomically for new root spans (#99); parent/in-flight decisions stay stable.'''
    )

# Changelog entry at the top of Unreleased.
replace(
    "CHANGELOG.md",
    '''## [Unreleased]\n\n''',
    '''## [Unreleased]\n\n- **HR-10 / #99 — OpenTelemetry root sample ratio hot-reloads without rebuilding tracing.** `observability.tracing.sample_ratio` now publishes as one atomic immutable root-sampler swap inside `ReloadPlan.Publish`; the stable `ParentBased` sampler preserves local/remote parent decisions and already-started traces while new roots use the new ratio. Provider, exporter, resource, propagator, tracer and global OTel registration remain process-lifetime, so a ratio-only change causes no exporter shutdown/flush/reconnect or provider churn. Disabled and lean builds remain inert, mixed candidates with the five tracing pipeline identity fields remain whole-candidate restart operations, lifecycle/config artifacts are generated from the authoritative registry, and deterministic SDK-delegation, concurrency and local OTLP tests cover the transition. The five pipeline identity fields remain `restart_required`.\n- **HR-07C / #160 — hot-reloadable durable audit sink.**'''
)

from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one replacement target, found {count}")
    p.write_text(text.replace(old, new, 1))


replace_once(
    "docs/status.md",
    "[`internal/lifecycle/lifecycle.go`](../internal/lifecycle/lifecycle.go)",
    "[`internal/lifecycle/registry.go`](../internal/lifecycle/registry.go)",
)

replace_once(
    "docs/adr/0011-reload-plan.md",
    "`internal/server/server.go` defines `ReloadPlan` with the phases:",
    "`internal/server/reload_plan.go` defines `ReloadPlan` with the phases:",
)
replace_once(
    "docs/adr/0011-reload-plan.md",
    "8. **Retire** — stop listeners no longer in the config and retire the old handler generation.\n9. **Refresh** — reload TLS certificates.\n10. **PostCommit** — apply dynamic side effects (`log_level`, `GOMAXPROCS`, stream reload).",
    "8. **Retire** — stop listeners no longer in the config, retire the old handler generation, and retire committed `PreparedRuntime` resources within their bounded lifetime.\n9. **PostCommit** — apply committed dynamic side effects that do not need a prepared resource (currently log level/format, metrics host-label mode, cache scalar policy/capacity, `GOMAXPROCS`, and stream reload).\n\nStatic certificate rotation is no longer a separate `Refresh` phase: since #100, candidate certificate providers are built during **Prepare** and published through `PreparedRuntime` before the new handler generation becomes reachable.",
)
replace_once(
    "docs/adr/0011-reload-plan.md",
    "7. **Classification records proven behavior.** Splitting a coarse entry never\n   promotes a field to `hot_reload` in anticipation of unlanded work: cache,\n   static certificate material, access-log sinks and tracing stay restart-bound\n   until #92/#93, #100 and #98 land. `stream.*.protocol` was reclassified to\n   `hot_reload` only after a real-socket characterization matrix proved the\n   listener transaction binds the candidate protocol before retiring the previous\n   one.",
    "7. **Classification records proven behavior.** Splitting a coarse entry never\n   promotes a field to `hot_reload` in anticipation of unlanded work. At the\n   time of the closed-world amendment, cache policy, static certificate material,\n   access-log sinks and tracing were deliberately restart-bound pending their\n   focused implementation issues. Later work promoted only the leaves whose\n   runtime semantics were actually proved (#92 cache scalars, #100 static\n   certificate/key rotation, #98 access-log sinks); tracing remains restart-bound\n   at this baseline. `stream.*.protocol` was likewise reclassified to\n   `hot_reload` only after a real-socket characterization matrix proved the\n   listener transaction binds the candidate protocol before retiring the previous\n   one.",
)
replace_once(
    "docs/adr/0011-reload-plan.md",
    "- `internal/lifecycle/lifecycle.go` — lifecycle registry",
    "- `internal/lifecycle/registry.go` — lifecycle registry",
)

replace_once(
    "docs/adr/0016-inbound-identity-and-backend-peer-trust.md",
    "(`reloadCertificates` remains a no-op. That concerns **inbound** listener certificates, which are\nrestart-only under R7-07, and was cited here as a `backend_tls` blocker in error.)",
    "(**Historical baseline note.** At the time this ADR was accepted, `reloadCertificates` was a no-op and inbound static listener certificates were restart-only under R7-07; that fact was cited here as a `backend_tls` blocker in error. #100 subsequently introduced the prepared `dynamicCertProvider`, so inbound static certificate/key content now hot-reloads on retained listeners. The architectural point of this paragraph is unchanged: inbound certificate rotation is not a Boundary-E/backend-peer-trust dependency.)",
)

replace_once(
    "docs/configuration.md",
    "Tracing configuration is read once at boot; a reload keeps the running tracer\n(the server logs a warning if the block changed) — restart to apply tracing\nchanges.",
    "Tracing configuration is currently read once at boot; a reload keeps the\nrunning tracer and a restart applies tracing changes. #99 is selected with\nreduced scope to make only `observability.tracing.sample_ratio` hot-reloadable\nwithout replacing the provider/exporter; until that implementation lands, the\nmachine lifecycle still reports it as `restart_required`. The other tracing\nfields remain restart-bound in this tranche. See\n[hot-reload strategy](hot-reload-strategy.md).",
)

Path("scripts/hot_reload_doc_followup_20260911.py").unlink(missing_ok=True)
Path(".github/workflows/hot-reload-doc-followup.yml").unlink(missing_ok=True)

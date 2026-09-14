from pathlib import Path


def replace(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise SystemExit(f"missing expected text in {path}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1))


editor = "internal/admin/ui/src/features/traffic-controls/TrafficControlEditor.tsx"
replace(
    editor,
    '      "The global request policy uses rate_limit_global_set. max_conns is a listener-level concurrent-connection cap whose final lifecycle is decided by the server.",',
    '      "The global request policy uses rate_limit_global_set. max_conns hot-applies to the stable listener admission limiter; lowering the cap never terminates connections already admitted.",',
)
replace(
    editor,
    '              max_conns is listener-level. The authoritative preview may permit it for listeners that are all new; retained listeners are saved for the next restart.',
    '              max_conns hot-applies to the stable listener admission limiter. New admissions observe the published cap; connections already admitted are never terminated.',
)

e2e = "internal/admin/ui/e2e/issue82-phase5.spec.ts"
replace(
    e2e,
    '''    // max_conns is listener-level and restart-required for a retained listener
    // (unlike the global rate itself), so it is what still exercises the
    // stage/update-staged flow below.
    const limiterForStage = await openTrafficEditor("Rate limiting", "Edit rate limiting");
    const maxConns = limiterForStage.getByLabel("Maximum concurrent connections");
    const currentMax = Number(await maxConns.inputValue()) || 0;
    await maxConns.fill(String(currentMax + 17));
    await waitForConfigQuiescence();
    await limiterForStage.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Save for next restart");
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();
    await expectStaticOK(request, "Jul static OK");

    const stagedLimiter = await openTrafficEditor("Rate limiting", "Edit rate limiting");
    const maxConns2 = stagedLimiter.getByLabel("Maximum concurrent connections");
    const currentMax2 = Number(await maxConns2.inputValue()) || 0;
    await maxConns2.fill(String(currentMax2 + 17));
    await waitForConfigQuiescence();
    await stagedLimiter.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Update staged configuration");
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();''',
    '''    // #106 made max_conns genuinely hot on retained listeners. Keep this Phase 5
    // planned-restart exercise truthful by changing a socket-owned timeout instead:
    // read_timeout remains new_listener_only, so changing it on an already-bound
    // listener stages the whole candidate for restart.
    const limitsForStage = await openTrafficEditor("Limits & Timeouts", "Edit limits & timeouts");
    const readTimeout = limitsForStage.getByLabel("Read timeout");
    const currentReadTimeout = await readTimeout.inputValue();
    await readTimeout.fill(currentReadTimeout === "31s" ? "32s" : "31s");
    await waitForConfigQuiescence();
    await limitsForStage.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Save for next restart");
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();
    await expectStaticOK(request, "Jul static OK");

    const stagedLimits = await openTrafficEditor("Limits & Timeouts", "Edit limits & timeouts");
    const readTimeout2 = stagedLimits.getByLabel("Read timeout");
    const stagedReadTimeout = await readTimeout2.inputValue();
    await readTimeout2.fill(stagedReadTimeout === "33s" ? "34s" : "33s");
    await waitForConfigQuiescence();
    await stagedLimits.getByRole("button", { name: "Review changes" }).click();
    await expect(page).toHaveURL(/\/config$/);
    await applyConfigAction("Update staged configuration");
    await expect(page.getByText("Restart required — configuration staged")).toBeVisible();''',
)

reload_doc = "docs/reload-semantics.md"
replace(
    reload_doc,
    "  admin read/write/apply limits plus the shared SSE cap, and the durable audit\n  sink path/rotation policy) apply through the same transaction.",
    "  admin read/write/apply limits plus the shared SSE cap, history retention, the durable audit\n  sink path/rotation policy, and the listener connection cap) apply through the same transaction.",
)
replace(
    reload_doc,
    "  lifecycle, `admin.enabled`, `admin.listen`, admin history resources,\n  tracing pipeline identity fields other than `sample_ratio`, ACME, and retained-listener bind settings)",
    "  lifecycle, `admin.enabled`, `admin.listen`, `admin.history_dir`,\n  tracing pipeline identity fields other than `sample_ratio`, ACME manager/account/challenge identity fields other than hot `ocsp_stapling`, and retained-listener bind settings)",
)
replace(
    reload_doc,
    '''A changed global
`max_conns` stages whenever any desired address is already bound; only an
all-new affected listener set can adopt it during live bind. `global.log_format`,
`observability.metrics.host_label`, and compression/global rate/key/burst
changes are all hot (#91).''',
    '''`rate_limit.max_conns` is hot (#106): retained listeners publish a new
admission cap in place, new listeners start with the candidate cap, and already
admitted connections are never terminated. `global.log_format`,
`observability.metrics.host_label`, and compression/global rate/key/burst
changes are also hot (#91).''',
)
replace(
    reload_doc,
    '''Restart-required and mixed candidates stage the
complete candidate. Listener-bound `rate_limit.max_conns` stages when an
existing listener is retained but may follow a server-authorized hot path when
all affected listeners are new.''',
    '''Restart-required and mixed candidates stage the
complete candidate. `rate_limit.max_conns` is hot on retained and new listeners;
listener-owned timeout/header/protocol settings keep their authoritative
`new_listener_only` or restart-required behavior.''',
)

config_doc = "docs/configuration.md"
replace(
    config_doc,
    '''A changed
`max_conns` requires staging whenever any currently bound desired address is
retained; it can apply live only when all affected desired listeners are new in
the same complete candidate.''',
    '''`max_conns` is hot (#106): the stable listener admission limiter publishes
the candidate cap in place for retained listeners, newly staged listeners start
with that cap, and existing admitted connections are never terminated.''',
)
replace(
    config_doc,
    "cache backend identity (`cache.enabled` / `cache.disk_path`), the currently restart-bound egress fields and tracing pipeline identity leaves (not hot `sample_ratio`), ACME identity/policy, admin structural resources, or retained-listener bind-time settings.",
    "cache backend identity (`cache.enabled` / `cache.disk_path`), tracing pipeline identity leaves (not hot `sample_ratio`), ACME manager/account/challenge identity policy (not hot `ocsp_stapling`), admin structural resources such as `history_dir`, or retained-listener bind-time settings.",
)

console_doc = "docs/console.md"
replace(
    console_doc,
    '''A
candidate containing a retained-listener `max_conns` change is staged as one
complete candidate; every other `global_set` field (`log_level`, `log_format`,
`worker_threads`, `shutdown_timeout`, `reload_timeout`,
`redact_min_secret_length`) is hot.''',
    '''`rate_limit.max_conns` is hot (#106) through the stable listener admission
limiter; lowering the cap affects new admissions without terminating existing
connections. Every `global_set` field (`log_level`, `log_format`,
`worker_threads`, `shutdown_timeout`, `reload_timeout`,
`redact_min_secret_length`) is also hot.''',
)
replace(
    console_doc,
    '''`log_format` is hot-reloadable (#91). `max_conns` is bound to
listeners: a retained listener stages, while an all-new listener transition may
be hot if the backend preview says so. Mixed candidates stage whole.''',
    '''`log_format` is hot-reloadable (#91). `max_conns` is hot-reloadable (#106):
new admissions observe the published listener cap while admitted connections
continue undisturbed. Mixed candidates still stage whole when another changed
field is restart-bound.''',
)

print("#106 Phase 5 lifecycle surfaces reconciled")

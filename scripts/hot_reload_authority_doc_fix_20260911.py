from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one replacement target, found {count}")
    p.write_text(text.replace(old, new, 1))


old = '''Jul.IA reloads configuration **without dropping connections**. A reload can be
triggered three ways:

- **Admin apply** — `POST /api/config/apply` (the Console "Apply changes"
  button) writes a new config and triggers a correlated reload. This path runs
  the full preflight gate before writing anything to disk and waits for the
  live reload outcome, returning it in the `reload` block of the response.
- **SIGHUP** (Unix) — operator sends the signal after editing the file directly.
- **Config file-watch** — the on-disk config file changed and the watcher fired.

**These three paths share the same live reload transaction, but the admin write
path validates *before* persistence and correlates the result with the request.**

> **This description is the `file_owned` behavior.** Whether SIGHUP and the
> file watcher adopt an external edit at all is governed by
> `[global].config_authority` — see
> ["Configuration authority: managed vs file_owned"](#configuration-authority-managed-vs-file_owned)
> below. In `managed` mode (not the default) neither SIGHUP nor the watcher
> triggers a reload; both become drift detectors instead, and an external edit
> is adopted only through an explicit `POST /api/config/adopt-external`.

The admin write path runs the full preflight (parse, dry-run, bind-probe, and
all restart-required checks) *before* the file is written. Nothing is saved
unless the config is validated to build and bind under preflight conditions.
Because preflight cannot observe every runtime condition (e.g. a bind race,
a late certificate file change, or transient disk errors), the live reload may
still fail after the file is written; such failures are recorded in the
structured `ReloadResult` and leave the previous generation authoritative.

SIGHUP and file-watch trigger the same live runtime swap **in `file_owned`
mode**, but they run restart-required checks *at swap time* rather than before
the file is written. This means:
'''

new = '''Jul.IA applies supported configuration changes **without dropping
connections**, but the entry path depends on `[global].config_authority`:

- **`managed` mode** — authenticated Console/API operations such as
  `POST /api/config/apply` run the full preflight before Jul writes the desired
  configuration, then trigger a correlated live reload and wait for its
  structured outcome. An external edit is not adopted implicitly: SIGHUP and
  file-watch events update drift state, and adoption requires the explicit
  `POST /api/config/adopt-external` workflow.
- **`file_owned` mode (default)** — an external file/GitOps owner changes the
  configuration and SIGHUP (Unix) or the file watcher triggers the live reload.
  Mutating admin endpoints are refused before side effects because Jul is not
  the desired-state writer in this mode.

After the authority-specific gate, every path that actually adopts a candidate
uses the same `ReloadPlan` transaction and the same lifecycle classifier. There
is no reduced SIGHUP/file-watch reload and no separate Admin-API runtime model.

The managed admin write/adoption path runs the full preflight (parse, dry-run,
bind-probe, and all restart-required checks) *before* Jul persists a candidate.
Nothing is saved unless the configuration is validated to build and bind under
preflight conditions. Because preflight cannot observe every runtime condition
(e.g. a bind race, a late certificate file change, or transient disk errors),
the live reload may still fail after persistence; such failures are recorded in
the structured `ReloadResult` and leave the previous generation authoritative.

SIGHUP and file-watch trigger the same live runtime swap **only in
`file_owned` mode**, but they run restart-required checks *at swap time* rather
than before the external owner wrote the file. This means:
'''
replace_once("docs/reload-semantics.md", old, new)

replace_once(
    "docs/reload-semantics.md",
    "- **restart_required** — takes effect only after a process restart. The admin\n  apply path returns HTTP 409 with `restart_required: true`; SIGHUP/file-watch\n  set `LastReload.OK=false`.",
    "- **restart_required** — takes effect only after a process restart. A managed\n  admin apply is rejected with HTTP 409 and `restart_required: true` (or can be\n  persisted explicitly through `stage_restart`); a `file_owned` SIGHUP/file-watch\n  adoption attempt records a failed/not-applied reload while the running value\n  remains unchanged.",
)

Path("scripts/hot_reload_authority_doc_fix_20260911.py").unlink(missing_ok=True)
Path(".github/workflows/hot-reload-authority-doc-fix.yml").unlink(missing_ok=True)

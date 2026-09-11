from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one replacement target, found {count}")
    p.write_text(text.replace(old, new, 1))


replace_once(
    "docs/configuration.md",
    "| `stage_restart` | Validates and persists the candidate without triggering a live reload. The running process continues serving the previous configuration. The candidate takes effect on the next process restart. Use this mode for changes to startup-bound settings (cache, egress, admin, tracing, ACME, log format, listener settings). |",
    "| `stage_restart` | Validates and persists the candidate without triggering a live reload. The running process continues serving the previous configuration. The candidate takes effect on the next process restart. Use this mode whenever preview reports `restart_required` — for example `global.config_authority`, cache backend identity (`cache.enabled` / `cache.disk_path`), the currently restart-bound egress and tracing leaves, ACME identity/policy, admin structural resources, or retained-listener bind-time settings. Do not infer lifecycle from the subsystem name; the generated lifecycle registry is field-specific. |",
)

replace_once(
    "docs/architecture.md",
    "| `preflight.go` | Admin write preflight gates (`Preflight.Apply` with `StreamPreflighter` iface) | Yes (`*_test.go`) |",
    "| `preflight.go` | Admin write preflight gates (`Preflight.Apply` with `StreamPreflighter` iface), including side-effect-free startup-resource validation for restart-bound candidates | Yes (`*_test.go`) |",
)
replace_once(
    "docs/architecture.md",
    "| `startup_restart.go` | Startup-bound subsystem restart checks (cache, egress, admin, metrics) | Yes (`*_test.go`) |\n",
    "",
)

Path("scripts/hot_reload_doc_final_audit_20260911.py").unlink(missing_ok=True)
Path(".github/workflows/hot-reload-doc-final-audit.yml").unlink(missing_ok=True)

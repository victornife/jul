#!/usr/bin/env python3
"""One-shot, idempotent documentation patch for #155 and #156."""

from __future__ import annotations

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def insert_before(path: str, marker: str, addition: str, sentinel: str) -> None:
    target = ROOT / path
    text = target.read_text(encoding="utf-8")
    if sentinel in text:
        return
    if marker not in text:
        raise SystemExit(f"marker not found in {path}: {marker!r}")
    target.write_text(text.replace(marker, addition + marker, 1), encoding="utf-8")


def insert_after(path: str, marker: str, addition: str, sentinel: str) -> None:
    target = ROOT / path
    text = target.read_text(encoding="utf-8")
    if sentinel in text:
        return
    if marker not in text:
        raise SystemExit(f"marker not found in {path}: {marker!r}")
    target.write_text(text.replace(marker, marker + addition, 1), encoding="utf-8")


insert_after(
    "README.md",
    "| **Observability** | Structured logging (text/JSON), pluggable access-log sinks (file/syslog with rotation), Prometheus metrics, OpenTelemetry tracing, health/readiness probes |\n",
    "| **Diagnostics** | Read-only `jul doctor` checks with deterministic human/JSON output, plus operator-triggered, bounded, secret-safe local support bundles with no automatic upload ([docs/diagnostics.md](docs/diagnostics.md)) |\n",
    "| **Diagnostics** | Read-only `jul doctor` checks",
)

insert_after(
    "README.md",
    "jul check [-config f] [-json] [-quiet]        full runtime preflight check\n",
    "jul doctor [-config f] [-json] [-strict] [-check-network]\n"
    "                                              run read-only local diagnostics\n"
    "jul support-bundle [-config f] [-output file] [-json] [-include-logs]\n"
    "                                              write a bounded local diagnostic archive\n",
    "jul support-bundle [-config f]",
)

insert_before(
    "README.md",
    "### `jul healthcheck`\n",
    "### `jul doctor`\n\n"
    "Runs deterministic, read-only checks for strict configuration parsing, semantic validation, configured files and certificates, admin exposure, bounded topology, and process/build state. The default run is network-free; `-check-network` explicitly enables bounded runtime preflight and immediate-close listener bind probes. Use `-json` for the versioned machine contract and `-strict` to make warnings fail CI.\n\n"
    "```bash\n"
    "jul doctor -config server.toml\n"
    "jul doctor -config server.toml -json -strict\n"
    "```\n\n"
    "Exit codes: `0` no errors, `1` one or more diagnostic errors, and `2` invalid usage or warnings under `-strict`. See [docs/diagnostics.md](docs/diagnostics.md).\n\n"
    "### `jul support-bundle`\n\n"
    "Creates an owner-only, bounded `tar.gz` containing a versioned manifest, safe build/configuration metadata, and the in-process `jul doctor` report. It never uploads automatically, never accepts arbitrary include paths, and excludes raw configuration, private keys, environment dumps, request/response bodies, and traffic captures. Logs are opt-in and limited to a bounded tail of the configured Jul access-log file.\n\n"
    "```bash\n"
    "jul support-bundle -config server.toml -output jul-support.tar.gz\n"
    "jul support-bundle -config server.toml -include-logs -json\n"
    "```\n\n"
    "Review every bundle before sharing it. See [docs/diagnostics.md](docs/diagnostics.md) for archive layout, limits, privacy guarantees, and limitations.\n\n",
    "### `jul doctor`\n",
)

insert_after(
    "docs/troubleshooting.md",
    "full config reference see [configuration.md](configuration.md); to validate a\nconfig before starting use `jul check` / `jul lint`.\n",
    "\n## Start with deterministic diagnostics\n\n"
    "Run `jul doctor -config server.toml` before changing a deployment. It reports strict parsing, semantic validation, lint, configured-path and certificate problems, admin exposure, and bounded topology without modifying the system or performing network checks by default. Add `-check-network` only when you deliberately want runtime preflight and immediate-close local bind probes.\n\n"
    "When a problem needs to be shared, create `jul support-bundle -config server.toml -output jul-support.tar.gz`. The archive is local, bounded, owner-only, and never uploaded automatically. Add `-include-logs` only after reviewing the configured access-log sensitivity. Always inspect the archive before sharing it. See [diagnostics.md](diagnostics.md) for the result codes, archive inventory, limits, and privacy model.\n",
    "## Start with deterministic diagnostics",
)

insert_before(
    "docs/security-posture.md",
    "## File permissions and atomic writes\n",
    "## Diagnostic output and support bundles\n\n"
    "`jul doctor` and `jul support-bundle` are explicit, local, read-only operator actions. Neither command uploads data, creates a persistent installation identifier, executes arbitrary commands, or accepts arbitrary filesystem include paths. Default doctor operation is network-free; network-capable preflight and listener probes require `-check-network`.\n\n"
    "Support bundles structurally exclude raw configuration values, private keys, credentials, environment dumps, request/response bodies, and traffic captures. JSON and text artifacts receive a defensive redaction pass, errors are sanitized, archive entries are fixed and traversal-safe, and the output is published owner-only without overwriting an existing path. Optional logs are limited to a bounded tail of the configured Jul access-log file and symbolic links are rejected.\n\n"
    "These controls reduce disclosure risk but cannot prove that every business-sensitive hostname, configured name, or log identifier is harmless. Operators must review bundles before sharing them and should not attach them to public issues by default. The full contract is in [diagnostics.md](diagnostics.md).\n\n---\n\n",
    "## Diagnostic output and support bundles",
)

insert_after(
    "CHANGELOG.md",
    "### Added\n",
    "- **Deterministic local diagnostics and secret-safe support bundles (#155, #156):** added `jul doctor` with stable human/JSON results, read-only network-free defaults, strict/semantic/lint/path/certificate/admin/topology checks, optional bounded runtime and listener probes, and documented exit codes. Added `jul support-bundle`, which invokes doctor in-process and writes a versioned, owner-only, bounded `tar.gz` with fixed collectors, per-artifact SHA-256 checksums, partial-failure reporting, cancellation, path/symlink/no-overwrite protections, and no phone-home or automatic upload. Raw configuration, private keys, environment dumps, traffic and request/response bodies are excluded; optional logs are limited to the configured access-log tail and receive defensive redaction. New CI gates independently require at least 85% coverage for `internal/diagnostics`, `internal/doctor`, and `internal/supportbundle`, plus full-tag race tests. See [docs/diagnostics.md](docs/diagnostics.md).\n",
    "Deterministic local diagnostics and secret-safe support bundles",
)

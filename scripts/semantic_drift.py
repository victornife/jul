#!/usr/bin/env python3
"""Cross-artifact semantic drift guards for Jul documentation consumers.

The module deliberately consumes existing machine projections. It does not
reconstruct runtime configuration, lifecycle, metrics, API, CLI, or feature
semantics.
"""
from __future__ import annotations

import json
import re
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable

try:
    import yaml
except ModuleNotFoundError:  # surfaced as an actionable finding by the feature check
    yaml = None

HTTP_METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
METRIC_RE = re.compile(r"\bjul_[a-z][a-z0-9_]*\b")
METHOD_PATH_RE = re.compile(
    r"\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+`?(/[A-Za-z0-9_{}./:-]+(?:\?[^`\s|)]*)?)`?"
)
REMOTE_COMMAND_RE = re.compile(r"\bjul\s+([a-z][a-z0-9-]+)\b")
CONFIG_TOKEN_RE = re.compile(r"`([a-z][a-z0-9_]*(?:\.(?:\*|[a-z][a-z0-9_]*))+|[a-z][a-z0-9_]*)`")


@dataclass(frozen=True, order=True)
class Finding:
    consumer: str
    line: int
    domain: str
    claim: str
    authority: str
    expected: str
    fix: str

    def message(self) -> str:
        return (
            f"{self.domain} semantic drift: {self.claim}\n"
            f"authority: {self.authority}\n"
            f"expected: {self.expected}\n"
            f"fix: {self.fix}"
        )


def _rel(root: Path, path: Path) -> str:
    try:
        return path.relative_to(root).as_posix()
    except ValueError:
        return path.as_posix()


def _line(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def _load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def _active_markdown(root: Path) -> list[Path]:
    """Return current human-facing Markdown, structurally excluding history/design archives."""
    paths: list[Path] = []
    readme = root / "README.md"
    if readme.exists():
        paths.append(readme)
    docs = root / "docs"
    if docs.exists():
        paths.extend(p for p in docs.glob("*.md") if p.is_file())
        roadmap = docs / "roadmap" / "README.md"
        if roadmap.exists():
            paths.append(roadmap)
    return sorted(set(paths))


def _table_rows(text: str) -> Iterable[tuple[int, list[str], list[str]]]:
    """Yield (line, header cells, row cells) for ordinary Markdown tables."""
    lines = text.splitlines()
    i = 0
    while i + 1 < len(lines):
        header = lines[i]
        sep = lines[i + 1]
        if "|" not in header or not re.match(r"^\s*\|?\s*:?-{3,}", sep):
            i += 1
            continue
        headers = [c.strip().strip("`") for c in header.strip().strip("|").split("|")]
        i += 2
        while i < len(lines) and "|" in lines[i] and lines[i].strip().startswith("|"):
            cells = [c.strip() for c in lines[i].strip().strip("|").split("|")]
            if len(cells) == len(headers):
                yield i + 1, headers, cells
            i += 1


def _without_wildcards(path: str) -> str:
    return path.replace(".*", "")


def _config_context_prefix(text: str, line_no: int) -> str | None:
    """Find the nearest objective TOML table reference before a structured table."""
    lines = text.splitlines()
    window = "\n".join(lines[max(0, line_no - 50): max(0, line_no - 1)])
    matches = list(re.finditer(r"\[\[?([a-z][a-z0-9_.]*)\]?\]", window))
    return matches[-1].group(1) if matches else None


def _resolve_config_field(
    token: str, fields: dict[str, dict[str, Any]], prefix: str | None = None
) -> tuple[str, dict[str, Any]] | None:
    if token in fields:
        return token, fields[token]
    normalized = [(path, meta) for path, meta in fields.items() if _without_wildcards(path) == token]
    if len(normalized) == 1:
        return normalized[0]
    if prefix:
        contextual = f"{prefix}.{token}"
        normalized = [(path, meta) for path, meta in fields.items() if _without_wildcards(path) == contextual]
        if len(normalized) == 1:
            return normalized[0]
    if "." in token:
        return None
    matches = [(path, meta) for path, meta in fields.items() if path.rsplit(".", 1)[-1] == token]
    return matches[0] if len(matches) == 1 else None


def _value_spellings(value: Any, meta: dict[str, Any]) -> set[str]:
    if isinstance(value, bool):
        return {"true", "on", "enabled"} if value else {"false", "off", "disabled"}
    if value is None:
        return {"null", "none"}
    if isinstance(value, (int, float)):
        return {str(value).lower()}
    if isinstance(value, list):
        vals = {json.dumps(value, separators=(",", ":")).lower()}
        vals.update(str(v).lower() for v in value)
        return vals
    s = str(value).lower()
    vals = {s, f'"{s}"', f"`{s}`"}
    if meta.get("scalar") == "size":
        m = re.fullmatch(r"(\d+)([kmg])", s)
        if m:
            unit = {"k": "kib", "m": "mib", "g": "gib"}[m.group(2)]
            vals.add(f"{m.group(1)} {unit}")
    return vals


def _cell_contains_expected(cell: str, value: Any, meta: dict[str, Any]) -> bool:
    normalized = cell.lower().replace("**", "")
    return any(v in normalized for v in _value_spellings(value, meta))


def check_config_claims(root: Path) -> list[Finding]:
    authority = root / "docs" / "generated" / "config-metadata.json"
    data = _load_json(authority)
    fields = data.get("fields", {})
    if not isinstance(fields, dict):
        return [Finding(_rel(root, authority), 0, "configuration", "metadata has no fields object",
                        _rel(root, authority), "generated field metadata", "run make config-contract-generate")]
    findings: list[Finding] = []
    for path in _active_markdown(root):
        text = path.read_text(encoding="utf-8")
        for line_no, headers, cells in _table_rows(text):
            low_headers = [h.lower() for h in headers]
            key_idx = next((i for i, h in enumerate(low_headers) if h in {"key", "field", "path", "config"}), None)
            if key_idx is None:
                continue
            tokens = CONFIG_TOKEN_RE.findall(cells[key_idx])
            prefix = _config_context_prefix(text, line_no)
            resolved = next((_resolve_config_field(t, fields, prefix) for t in tokens if _resolve_config_field(t, fields, prefix)), None)
            if not resolved:
                continue
            field_path, meta = resolved
            for label in ("default",):
                if label in low_headers and "default" in meta:
                    idx = low_headers.index(label)
                    if not _cell_contains_expected(cells[idx], meta["default"], meta):
                        findings.append(Finding(
                            _rel(root, path), line_no, "configuration",
                            f"{field_path} documents default {cells[idx]!r}", _rel(root, authority),
                            f"default {meta['default']!r}", "update the consumer or change the config authority and run make config-contract-generate",
                        ))
            value_header = next((h for h in ("allowed", "values", "accepted values") if h in low_headers), None)
            allowed = meta.get("allowed")
            if value_header and isinstance(allowed, list):
                idx = low_headers.index(value_header)
                actual = set(re.findall(r"`([^`]+)`|\"([^\"]+)\"", cells[idx]))
                flat = {a or b for a, b in actual}
                if flat and flat != {str(v) for v in allowed}:
                    findings.append(Finding(
                        _rel(root, path), line_no, "configuration",
                        f"{field_path} documents accepted values {sorted(flat)!r}", _rel(root, authority),
                        f"accepted values {sorted(str(v) for v in allowed)!r}",
                        "update the consumer or change the value-contract authority and run make config-contract-generate",
                    ))
    return sorted(set(findings))


def _lifecycle_fields(data: dict[str, Any]) -> dict[str, dict[str, Any]]:
    raw = data.get("fields", [])
    if isinstance(raw, dict):
        return raw
    out: dict[str, dict[str, Any]] = {}
    if isinstance(raw, list):
        for row in raw:
            if isinstance(row, dict) and isinstance(row.get("path"), str):
                out[row["path"]] = row
    return out


def check_lifecycle_claims(root: Path) -> list[Finding]:
    authority = root / "docs" / "generated" / "config-lifecycle.json"
    data = _load_json(authority)
    fields = _lifecycle_fields(data)
    classes = {str(x.get("name")) for x in data.get("classes", []) if isinstance(x, dict)}
    subsystems = {str(x.get("name")) for x in data.get("subsystems", []) if isinstance(x, dict)}
    findings: list[Finding] = []
    for path in _active_markdown(root):
        text = path.read_text(encoding="utf-8")
        for line_no, headers, cells in _table_rows(text):
            low = [h.lower() for h in headers]
            if "lifecycle" not in low:
                continue
            life_idx = low.index("lifecycle")
            path_idx = next((i for i, h in enumerate(low) if h in {"path", "field", "key", "config"}), None)
            if path_idx is None:
                continue
            token_match = CONFIG_TOKEN_RE.search(cells[path_idx])
            if not token_match:
                continue
            claim_path = token_match.group(1)
            actual_class = cells[life_idx].strip().strip("`")
            resolved = _resolve_config_field(claim_path, fields, _config_context_prefix(text, line_no))
            if not resolved:
                findings.append(Finding(_rel(root, path), line_no, "lifecycle",
                                        f"unknown lifecycle path {claim_path!r}", _rel(root, authority),
                                        "a path present in generated lifecycle metadata",
                                        "use an existing lifecycle path or change internal/lifecycle and run make lifecycle-generate"))
                continue
            claim_path, field_meta = resolved
            if actual_class not in classes:
                findings.append(Finding(_rel(root, path), line_no, "lifecycle",
                                        f"unknown lifecycle class {actual_class!r} for {claim_path}", _rel(root, authority),
                                        f"one of {sorted(classes)!r}",
                                        "use an existing lifecycle class or change internal/lifecycle and run make lifecycle-generate"))
                continue
            expected_class = str(field_meta.get("class") or field_meta.get("lifecycle_class") or "")
            if expected_class and actual_class != expected_class:
                findings.append(Finding(_rel(root, path), line_no, "lifecycle",
                                        f"{claim_path} documents lifecycle {actual_class!r}", _rel(root, authority),
                                        expected_class, "correct the consumer or change internal/lifecycle and run make lifecycle-generate"))
            if "subsystem" in low:
                actual_sub = cells[low.index("subsystem")].strip().strip("`")
                if actual_sub not in subsystems:
                    findings.append(Finding(_rel(root, path), line_no, "lifecycle",
                                            f"unknown lifecycle subsystem {actual_sub!r}", _rel(root, authority),
                                            f"one of {sorted(subsystems)!r}",
                                            "use an existing subsystem or change internal/lifecycle and run make lifecycle-generate"))
                else:
                    expected_sub = str(field_meta.get("subsystem") or "")
                    if expected_sub and actual_sub != expected_sub:
                        findings.append(Finding(_rel(root, path), line_no, "lifecycle",
                                                f"{claim_path} documents subsystem {actual_sub!r}", _rel(root, authority),
                                                expected_sub, "correct the consumer or regenerate lifecycle metadata"))
    return sorted(set(findings))


def _metric_names(data: dict[str, Any]) -> set[str]:
    names: set[str] = set()
    for row in data.get("metrics", []):
        if not isinstance(row, dict) or not isinstance(row.get("name"), str):
            continue
        name = row["name"]
        names.add(name)
        for key in ("aliases", "deprecated_aliases"):
            aliases = row.get(key, [])
            if isinstance(aliases, list):
                names.update(str(v) for v in aliases)
        if row.get("type") in {"histogram", "summary"}:
            names.update({name + "_sum", name + "_count"})
        if row.get("type") == "histogram":
            names.add(name + "_bucket")
    return names


def _metric_consumers(root: Path) -> list[Path]:
    paths = set(_active_markdown(root))
    ui = root / "internal" / "admin" / "ui" / "src"
    if ui.exists():
        for suffix in ("*.ts", "*.tsx", "*.js", "*.jsx", "*.json"):
            paths.update(p for p in ui.rglob(suffix) if p.is_file())
    for base in (root / "examples", root / "deploy", root / "monitoring"):
        if base.exists():
            for p in base.rglob("*"):
                if p.is_file() and any(k in p.name.lower() for k in ("dashboard", "alert", "prometheus", "grafana", "rule")):
                    paths.add(p)
    return sorted(paths)


def check_metric_claims(root: Path) -> list[Finding]:
    authority = root / "docs" / "metrics-contract.json"
    allowed = _metric_names(_load_json(authority))
    findings: list[Finding] = []
    for path in _metric_consumers(root):
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for m in METRIC_RE.finditer(text):
            name = m.group(0)
            if name not in allowed:
                findings.append(Finding(
                    _rel(root, path), _line(text, m.start()), "metrics", f"unknown/removed metric {name!r}",
                    _rel(root, authority), "a current metric or contract-declared supported alias",
                    "update the consumer or intentionally change the runtime metric contract through #126-owned sources",
                ))
    return sorted(set(findings))


def _openapi_operations(data: dict[str, Any]) -> set[tuple[str, str]]:
    ops: set[tuple[str, str]] = set()
    for path, item in data.get("paths", {}).items():
        if not isinstance(item, dict):
            continue
        for method in item:
            upper = method.upper()
            if upper in HTTP_METHODS:
                ops.add((upper, path))
    return ops


def _path_matches(template: str, concrete: str) -> bool:
    path = concrete.split("?", 1)[0]
    if path == template:
        return True
    pattern = "^" + re.sub(r"\\\{[^/{}]+\\\}", r"[^/]+", re.escape(template)) + "$"
    return re.fullmatch(pattern, path) is not None


def _api_consumers(root: Path) -> list[Path]:
    names = {"README.md", "admin-api.md", "remote-cli.md", "compatibility.md", "deployment.md"}
    return [p for p in _active_markdown(root) if p.name in names or _rel(root, p) == "README.md"]


def _structured_api_claims(text: str) -> Iterable[tuple[int, str, str, str, bool]]:
    """Yield objective supported-API claims. The final bool marks explicit support."""
    seen: set[tuple[int, str, str]] = set()
    for m in METHOD_PATH_RE.finditer(text):
        path = m.group(2)
        bare = path.split("?", 1)[0]
        # Arbitrary unversioned Console routes are legitimate prose examples.
        # Only versioned/public routes are inherently external claims.
        if not (bare.startswith("/api/v1/") or bare in {"/healthz", "/readyz", "/metrics"}):
            continue
        row = (_line(text, m.start()), m.group(1), path, m.group(0), True)
        key = row[:3]
        if key not in seen:
            seen.add(key)
            yield row
    for line_no, headers, cells in _table_rows(text):
        low = [h.lower() for h in headers]
        pidx = next((i for i, h in enumerate(low) if h in {"path", "endpoint"}), None)
        midx = next((i for i, h in enumerate(low) if h == "method"), None)
        if pidx is None or midx is None:
            continue
        method = cells[midx].strip().strip("`").upper()
        pm = re.search(r"(/[A-Za-z0-9_{}./:-]+(?:\?[^`\s|)]*)?)", cells[pidx])
        classification = ""
        if "classification" in low:
            classification = cells[low.index("classification")].strip().strip("`").lower()
        explicit_support = classification in {"external", "public", "deprecated"}
        if method in HTTP_METHODS and pm and (explicit_support or pm.group(1).startswith("/api/v1/")):
            key = (line_no, method, pm.group(1))
            if key not in seen:
                seen.add(key)
                yield line_no, method, pm.group(1), " | ".join(cells), explicit_support


def check_api_claims(root: Path) -> list[Finding]:
    authority = root / "docs" / "generated" / "openapi.json"
    operations = _openapi_operations(_load_json(authority))
    findings: list[Finding] = []
    for path in _api_consumers(root):
        text = path.read_text(encoding="utf-8")
        for line_no, method, claimed_path, raw, explicit_support in _structured_api_claims(text):
            bare = claimed_path.split("?", 1)[0]
            candidate = any(method == m and _path_matches(t, bare) for m, t in operations)
            if candidate:
                continue
            if explicit_support or bare.startswith("/api/v1/") or bare in {"/healthz", "/readyz", "/metrics"}:
                findings.append(Finding(
                    _rel(root, path), line_no, "external API", f"documented operation {method} {claimed_path}",
                    _rel(root, authority), "a method/path present in committed generated OpenAPI",
                    "correct the documentation, or intentionally change the API authority and run make api-contract-generate",
                ))
    return sorted(set(findings))


def _load_feature_manifest(path: Path) -> dict[str, Any]:
    if yaml is None:
        raise RuntimeError("PyYAML is required to read docs/feature-status.yaml")
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    return data if isinstance(data, dict) else {}


def check_feature_claims(root: Path) -> list[Finding]:
    authority = root / "docs" / "feature-status.yaml"
    try:
        data = _load_feature_manifest(authority)
    except Exception as exc:
        return [Finding(_rel(root, authority), 0, "feature maturity", str(exc), _rel(root, authority),
                        "valid canonical feature manifest", "install PyYAML or correct docs/feature-status.yaml")]
    features = [f for f in data.get("features", []) if isinstance(f, dict)]
    by_id = {str(f.get("id")): f for f in features if f.get("id")}
    docs_to_features: dict[str, list[dict[str, Any]]] = {}
    for feature in features:
        if feature.get("doc"):
            docs_to_features.setdefault(str(feature["doc"]), []).append(feature)
    # A bare canonical-guide maturity marker is unambiguous only when exactly
    # one product feature owns that guide. Shared guides require a feature ID.
    by_doc = {doc: rows[0] for doc, rows in docs_to_features.items() if len(rows) == 1}
    findings: list[Finding] = []
    for doc, feature in sorted(by_doc.items()):
        path = root / "docs" / doc
        if not path.exists():
            continue  # existing feature-status check owns canonical-doc existence
        text = path.read_text(encoding="utf-8")
        for m in re.finditer(r"\*\*Maturity:\*\*\s*`?([A-Za-z-]+)`?", text):
            actual = m.group(1)
            expected = str(feature.get("maturity"))
            if actual != expected:
                findings.append(Finding(_rel(root, path), _line(text, m.start()), "feature maturity",
                                        f"{feature.get('id')} canonical guide claims {actual!r}", _rel(root, authority), expected,
                                        "update the canonical guide or make an explicit product-maturity decision in feature-status.yaml"))
    marker = re.compile(r"\*\*Feature:\*\*\s*`?([A-Za-z0-9-]+)`?.*?\*\*Maturity:\*\*\s*`?([A-Za-z-]+)`?")
    for path in _active_markdown(root):
        text = path.read_text(encoding="utf-8")
        for m in marker.finditer(text):
            feat_id, actual = m.groups()
            if feat_id not in by_id:
                findings.append(Finding(_rel(root, path), _line(text, m.start()), "feature maturity",
                                        f"unknown feature ID {feat_id!r}", _rel(root, authority), "a feature ID in feature-status.yaml",
                                        "use the canonical feature ID or add the feature through the product-maturity authority"))
            elif actual != str(by_id[feat_id].get("maturity")):
                findings.append(Finding(_rel(root, path), _line(text, m.start()), "feature maturity",
                                        f"{feat_id} claims {actual!r}", _rel(root, authority), str(by_id[feat_id].get("maturity")),
                                        "update the consumer or feature-status.yaml if an explicit maturity decision changed"))
    return sorted(set(findings))


def _run(root: Path, args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, cwd=root, text=True, capture_output=True, check=False, timeout=120)


def load_cli_authority(root: Path) -> tuple[str, dict[str, Any]]:
    help_run = _run(root, ["go", "run", "./cmd/jul", "--help"])
    help_text = (help_run.stdout or "") + (help_run.stderr or "")
    if help_run.returncode != 0 and "Remote automation" not in help_text:
        raise RuntimeError(f"jul --help failed: {help_text.strip()}")
    caps_run = _run(root, ["go", "run", "./cmd/jul", "capabilities", "--json"])
    if caps_run.returncode != 0:
        raise RuntimeError(f"jul capabilities --json failed: {(caps_run.stderr or caps_run.stdout).strip()}")
    return help_text, json.loads(caps_run.stdout)


def _remote_help_commands(help_text: str) -> set[str]:
    m = re.search(r"Remote automation \(stable /api/v1\):\n(.*?)(?:\nRemote connection flags:|\Z)", help_text, re.S)
    if not m:
        return set()
    return {x.group(1) for x in re.finditer(r"(?m)^\s+jul\s+([a-z][a-z0-9-]+)\b", m.group(1))}


def _help_exit_codes(help_text: str) -> set[int]:
    m = re.search(r"Remote automation exit contract:\n(.*?)(?:\nSecurity:|\Z)", help_text, re.S)
    if not m:
        return set()
    return {int(x.group(1)) for x in re.finditer(r"(?m)^\s+(\d+)\s+", m.group(1))}


def _documented_remote_commands(text: str) -> list[tuple[int, str]]:
    commands: list[tuple[int, str]] = []
    section = re.search(r"(?ms)^## Commands\s*$\n(.*?)(?=^##\s|\Z)", text)
    if section:
        base = _line(text, section.start(1)) - 1
        for i, line in enumerate(section.group(1).splitlines(), 1):
            m = REMOTE_COMMAND_RE.search(line)
            if m:
                commands.append((base + i, m.group(1)))
    for m in re.finditer(r"(?m)^\s*jul\s+([a-z][a-z0-9-]+)\b[^\n]*--endpoint\b", text):
        commands.append((_line(text, m.start()), m.group(1)))
    return sorted(set(commands))


def _documented_exit_codes(text: str) -> set[int]:
    m = re.search(r"(?ms)^## Automation exit codes\s*$\n(.*?)(?=^##\s|\Z)", text)
    if not m:
        return set()
    return {int(x.group(1)) for x in re.finditer(r"(?m)^\|\s*(\d+)\s*\|", m.group(1))}


def check_cli_claims(root: Path, *, help_text: str | None = None, capabilities: dict[str, Any] | None = None) -> list[Finding]:
    authority_name = "jul --help + jul capabilities --json"
    if help_text is None or capabilities is None:
        try:
            help_text, capabilities = load_cli_authority(root)
        except (RuntimeError, json.JSONDecodeError) as exc:
            return [Finding("scripts/docs-check.py", 0, "remote CLI", str(exc), authority_name,
                            "locally executable shipped CLI authority", "ensure the repository CLI builds, then rerun docs-check")]
    commands = _remote_help_commands(help_text)
    exit_rows = capabilities.get("exit_codes", []) if isinstance(capabilities, dict) else []
    exit_codes = {int(r["code"]) for r in exit_rows if isinstance(r, dict) and isinstance(r.get("code"), int)}
    findings: list[Finding] = []
    if not commands:
        findings.append(Finding("cmd/jul --help", 0, "remote CLI", "remote automation section missing/empty", authority_name,
                                "the shipped remote command surface", "restore the shipped CLI help contract before checking docs"))
    help_codes = _help_exit_codes(help_text)
    if help_codes != exit_codes:
        findings.append(Finding("cmd/jul --help", 0, "remote CLI exits", f"help exposes exit codes {sorted(help_codes)}",
                                "jul capabilities --json / internal/adminapi.ExitCodes()", f"exit codes {sorted(exit_codes)}",
                                "render CLI help from or update it to agree with the existing exit-code authority"))
    doc = root / "docs" / "remote-cli.md"
    if doc.exists():
        text = doc.read_text(encoding="utf-8")
        for line_no, command in _documented_remote_commands(text):
            if command not in commands:
                findings.append(Finding(_rel(root, doc), line_no, "remote CLI", f"documented command 'jul {command}'",
                                        "shipped jul --help", f"one of {sorted(commands)!r}",
                                        "correct the remote CLI documentation or intentionally change the shipped CLI"))
        documented_codes = _documented_exit_codes(text)
        if documented_codes and documented_codes != exit_codes:
            findings.append(Finding(_rel(root, doc), 0, "remote CLI exits", f"documentation exposes {sorted(documented_codes)}",
                                    "jul capabilities --json / internal/adminapi.ExitCodes()", f"exit codes {sorted(exit_codes)}",
                                    "update the exit-code table; do not create a second exit registry"))
    return sorted(set(findings))


def check_capability_claims(root: Path, capabilities: dict[str, Any] | None = None) -> list[Finding]:
    config_authority = root / "docs" / "generated" / "config-metadata.json"
    meta = _load_json(config_authority)
    if capabilities is None:
        try:
            _, capabilities = load_cli_authority(root)
        except (RuntimeError, json.JSONDecodeError) as exc:
            return [Finding("scripts/docs-check.py", 0, "build capability", str(exc), "jul capabilities --json",
                            "locally executable capability projection", "ensure the CLI builds, then rerun docs-check")]
    features = capabilities.get("features", {}) if isinstance(capabilities, dict) else {}
    capability_names = set(features) if isinstance(features, dict) else set()
    cap_to_tag = meta.get("capability_build_tags", {}) if isinstance(meta, dict) else {}
    valid_tags = {str(v) for v in cap_to_tag.values()} | {c for c in capability_names if c not in cap_to_tag}
    findings: list[Finding] = []
    for path in _active_markdown(root):
        text = path.read_text(encoding="utf-8")
        for m in re.finditer(r"(?i)(?:build\s+tag|requires\s+the)\s+`([a-z][a-z0-9_]*)`(?:\s+tag)?", text):
            tag = m.group(1)
            if tag not in valid_tags:
                findings.append(Finding(_rel(root, path), _line(text, m.start()), "build capability",
                                        f"unknown build tag/capability {tag!r}",
                                        "jul capabilities --json + docs/generated/config-metadata.json",
                                        f"a current build tag derived from capabilities: {sorted(valid_tags)!r}",
                                        "correct the consumer or intentionally change the build-capability authority"))
        for line_no, headers, cells in _table_rows(text):
            low = [h.lower() for h in headers]
            if "tag" not in low or "capability" not in low:
                continue
            for tag in re.findall(r"`([a-z][a-z0-9_]*)`", cells[low.index("tag")]):
                if tag not in valid_tags:
                    findings.append(Finding(_rel(root, path), line_no, "build capability",
                                            f"unknown build tag {tag!r}",
                                            "jul capabilities --json + docs/generated/config-metadata.json",
                                            f"a current build tag derived from capabilities: {sorted(valid_tags)!r}",
                                            "correct the structured build-tag table or intentionally change the capability authority"))
    return sorted(set(findings))


def check_current_authority_links(root: Path) -> list[Finding]:
    required = {
        "docs/index.md": ("issues/62", "feature-status.yaml", "generated/config-reference.md", "generated/openapi.json", "audit-register.md"),
        "docs/roadmap/README.md": ("issues/62", "status.md"),
        "docs/status.md": ("issues/62", "feature-status.yaml", "audit-register.md"),
    }
    findings: list[Finding] = []
    for rel, needles in sorted(required.items()):
        path = root / rel
        if not path.exists():
            findings.append(Finding(rel, 0, "current authority", "current authority surface is missing", "repository documentation model",
                                    "an active current-authority navigation surface", "restore the active document"))
            continue
        text = path.read_text(encoding="utf-8")
        for needle in needles:
            if needle not in text:
                findings.append(Finding(rel, 0, "current authority", f"missing current authority reference {needle!r}",
                                        "#62 / generated contract / audit-status authorities", needle,
                                        "link the current authority; do not point active navigation at superseded review material"))
    return sorted(set(findings))


def check_repository(root: Path, *, cli_help: str | None = None, capabilities: dict[str, Any] | None = None) -> list[Finding]:
    root = root.resolve()
    if cli_help is None or capabilities is None:
        try:
            cli_help, capabilities = load_cli_authority(root)
        except (RuntimeError, json.JSONDecodeError) as exc:
            cli_help, capabilities = "", {}
            cli_failure = [Finding("scripts/docs-check.py", 0, "remote CLI", str(exc),
                                   "jul --help + jul capabilities --json", "locally executable shipped CLI authority",
                                   "ensure the repository CLI builds, then rerun docs-check")]
        else:
            cli_failure = []
    else:
        cli_failure = []
    findings = []
    findings.extend(check_config_claims(root))
    findings.extend(check_lifecycle_claims(root))
    findings.extend(check_metric_claims(root))
    findings.extend(check_feature_claims(root))
    findings.extend(check_api_claims(root))
    findings.extend(check_cli_claims(root, help_text=cli_help, capabilities=capabilities) if not cli_failure else cli_failure)
    findings.extend(check_capability_claims(root, capabilities=capabilities) if not cli_failure else [])
    findings.extend(check_current_authority_links(root))
    return sorted(set(findings))

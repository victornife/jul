#!/usr/bin/env python3
"""Behavioural tests for the #128 cross-artifact semantic drift guards."""
from __future__ import annotations

import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import semantic_drift as sd  # noqa: E402


HELP = """Jul.IA dev

Remote automation (stable /api/v1):
  jul plan --endpoint u --config candidate.toml
  jul diff --endpoint u --history-id id
  jul apply --endpoint u --config candidate.toml --base-version v
  jul stage --endpoint u --config candidate.toml --base-version v
  jul status --endpoint u
  jul rollback --endpoint u --history-id id
  jul export --endpoint u
  jul diagnostics --endpoint u
  jul apply --endpoint u --adopt-external

Remote connection flags:
  --endpoint u

Remote automation exit contract:
  0  success
  1  validation
  2  usage
  3  restart
  4  degraded
  5  conflict
  6  authority
  7  auth
  8  transport
  9  internal

Security:
  safe
"""

CAPS = {
    "features": {
        "waf": False,
        "stream_proxy": False,
        "wasm_plugins": False,
        "acme": False,
        "grpc": False,
        "http3": False,
        "otel": False,
        "console": False,
        "brotli": False,
        "zstd": False,
        "importer": False,
        "consul": False,
        "kubernetes": False,
    },
    "exit_codes": [{"code": i, "meaning": f"exit-{i}"} for i in range(10)],
}


def write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def write_json(path: Path, value: object) -> None:
    write(path, json.dumps(value, indent=2) + "\n")


def make_repo(root: Path) -> None:
    write_json(
        root / "docs/generated/config-metadata.json",
        {
            "capability_build_tags": {
                "waf": "waf",
                "stream_proxy": "stream",
                "wasm_plugins": "wasmplugins",
                "grpc": "grpc",
                "http3": "http3",
                "otel": "otel",
                "console": "console",
                "brotli": "brotli",
                "zstd": "zstd",
                "importer": "importer",
                "consul": "consul",
                "kubernetes": "kubernetes",
                "acme": "acme",
            },
            "fields": {
                "global.timeout": {"default": "30s", "scalar": "duration"},
                "global.mode": {"allowed": ["managed", "file_owned"], "default": "file_owned"},
                "cache.enabled": {"default": False, "scalar": "bool"},
                "cache.size": {"default": "64m", "scalar": "size"},
                "upstreams.*.health_check.enabled": {"default": False, "scalar": "bool"},
            },
        },
    )
    write_json(
        root / "docs/generated/config-lifecycle.json",
        {
            "classes": [{"name": "hot_reload"}, {"name": "restart_required"}],
            "subsystems": [{"name": "runtime"}, {"name": "cache"}],
            "fields": [
                {"path": "global.timeout", "class": "hot_reload", "subsystem": "runtime"},
                {"path": "cache.enabled", "class": "restart_required", "subsystem": "cache"},
            ],
        },
    )
    write_json(
        root / "docs/metrics-contract.json",
        {
            "metrics": [
                {"name": "jul_http_requests_total", "type": "counter", "state": "released"},
                {
                    "name": "jul_request_duration_seconds",
                    "type": "histogram",
                    "state": "released",
                    "deprecated_aliases": ["jul_request_latency_seconds"],
                },
            ]
        },
    )
    write(
        root / "docs/feature-status.yaml",
        """version: 2
features:
  - id: F-1
    name: One
    maturity: Beta
    delivery: merged
    doc: one.md
  - id: F-2
    name: Shared A
    maturity: GA
    delivery: soaked
    doc: shared.md
  - id: F-3
    name: Shared B
    maturity: Beta
    delivery: merged
    doc: shared.md
""",
    )
    write_json(
        root / "docs/generated/openapi.json",
        {
            "openapi": "3.1.0",
            "paths": {
                "/api/v1/status": {"get": {"operationId": "getStatus"}},
                "/api/v1/config/plan": {"post": {"operationId": "planConfig"}},
                "/api/v1/config/history/{id}/diff": {"get": {"operationId": "historyDiff"}},
                "/metrics": {"get": {"operationId": "metrics"}},
                "/healthz": {"get": {}},
                "/readyz": {"get": {}},
            },
        },
    )
    write(
        root / "docs/index.md",
        """# Index
[#62](https://github.com/victornife/jul/issues/62)
[status](feature-status.yaml)
[config](generated/config-reference.md)
[api](generated/openapi.json)
[audits](audit-register.md)

## Build tags
| Tag | Capability |
| --- | --- |
| `grpc` | gRPC |
| `stream` | Stream |
""",
    )
    write(root / "docs/roadmap/README.md", "[#62](https://github.com/victornife/jul/issues/62) [status](../status.md)\n")
    write(root / "docs/status.md", "[#62](https://github.com/victornife/jul/issues/62) [manifest](feature-status.yaml) [audit](audit-register.md)\n")
    write(root / "docs/audit-register.md", "# Audit register\n")
    write(root / "docs/generated/config-reference.md", "# Generated config\n")
    write(root / "docs/one.md", "# One\n\n**Maturity:** Beta\n")
    write(root / "docs/shared.md", "# Shared\n\n**Maturity:** GA\n")
    write(
        root / "docs/configuration.md",
        """# Configuration
| Key | Default | Allowed |
| --- | --- | --- |
| `global.timeout` | `30s` | |
| `global.mode` | `file_owned` | `managed`, `file_owned` |
| `cache.enabled` | false | |
| `cache.size` | 64 MiB | |

| Path | Lifecycle | Subsystem |
| --- | --- | --- |
| `global.timeout` | `hot_reload` | `runtime` |
""",
    )
    write(root / "docs/observability.md", "Use `jul_http_requests_total` and `jul_request_duration_seconds_bucket`.\n")
    write(
        root / "docs/admin-api.md",
        """# API
`GET /api/v1/status`
`GET /api/v1/config/history/abc123/diff`
| Path | Method | Classification |
| --- | --- | --- |
| `/metrics` | GET | external |
""",
    )
    write(
        root / "docs/remote-cli.md",
        """# Remote automation CLI
## Commands
| Command | Stable API workflow |
| --- | --- |
| `jul plan` | `POST /api/v1/config/plan` |
| `jul status` | `GET /api/v1/status` |
| `jul export` | read |

jul plan --endpoint https://example.test --config candidate.toml

## Automation exit codes
| Exit | Meaning |
| ---: | --- |
| 0 | a |
| 1 | b |
| 2 | c |
| 3 | d |
| 4 | e |
| 5 | f |
| 6 | g |
| 7 | h |
| 8 | i |
| 9 | j |
""",
    )
    write(root / "docs/compatibility.md", "Supported external API: `GET /api/v1/status`.\n")
    write(root / "docs/deployment.md", "Console-private `POST /api/config/apply` is internal.\n")
    write(root / "README.md", "See `GET /api/v1/status`. Requires the `grpc` build tag.\n")


def tree_digest(root: Path) -> str:
    h = hashlib.sha256()
    for path in sorted(p for p in root.rglob("*") if p.is_file()):
        h.update(path.relative_to(root).as_posix().encode())
        h.update(b"\0")
        h.update(path.read_bytes())
        h.update(b"\0")
    return h.hexdigest()


class SemanticDriftTests(unittest.TestCase):
    def repo(self):
        tmp = tempfile.TemporaryDirectory()
        root = Path(tmp.name)
        make_repo(root)
        self.addCleanup(tmp.cleanup)
        return root

    def assert_domain(self, findings, domain, needle):
        matched = [f for f in findings if f.domain == domain and needle in f.claim]
        self.assertTrue(matched, findings)
        msg = matched[0].message()
        self.assertIn("authority:", msg)
        self.assertIn("expected:", msg)
        self.assertIn("fix:", msg)
        self.assertTrue(matched[0].consumer)

    def test_clean_fixture_passes_and_repository_check_is_no_write(self):
        root = self.repo()
        before = tree_digest(root)
        self.assertEqual(sd.check_repository(root, cli_help=HELP, capabilities=CAPS), [])
        self.assertEqual(tree_digest(root), before)

    def test_config_default_and_allowed_value_drift(self):
        root = self.repo()
        p = root / "docs/configuration.md"
        s = p.read_text()
        s = s.replace("| `global.timeout` | `30s` | |", "| `global.timeout` | `45s` | |")
        s = s.replace("`managed`, `file_owned`", "`managed`, `controller_owned`")
        p.write_text(s)
        findings = sd.check_config_claims(root)
        self.assert_domain(findings, "configuration", "documents default")
        self.assert_domain(findings, "configuration", "accepted values")

    def test_config_unique_leaf_resolution_and_ambiguous_leaf_skip(self):
        root = self.repo()
        p = root / "docs/configuration.md"
        write(p, "| Key | Default |\n| --- | --- |\n| `timeout` | `45s` |\n")
        self.assert_domain(sd.check_config_claims(root), "configuration", "global.timeout")
        meta = json.loads((root / "docs/generated/config-metadata.json").read_text())
        meta["fields"]["other.timeout"] = {"default": "45s"}
        write_json(root / "docs/generated/config-metadata.json", meta)
        self.assertEqual(sd.check_config_claims(root), [])

    def test_config_and_lifecycle_use_nearby_toml_context_without_new_registry(self):
        root = self.repo()
        data = json.loads((root / "docs/generated/config-lifecycle.json").read_text())
        data["fields"].append({"path": "upstreams.*.health_check.enabled", "class": "hot_reload", "subsystem": "runtime"})
        write_json(root / "docs/generated/config-lifecycle.json", data)
        p = root / "docs/health.md"
        write(
            p,
            """### `[upstreams.health_check]`
| Key | Default | Lifecycle | Subsystem |
| --- | --- | --- | --- |
| `enabled` | `true` | `restart_required` | `runtime` |
""",
        )
        self.assert_domain(sd.check_config_claims(root), "configuration", "upstreams.*.health_check.enabled")
        self.assert_domain(sd.check_lifecycle_claims(root), "lifecycle", "documents lifecycle")

    def test_config_bool_size_and_invalid_metadata_shape(self):
        root = self.repo()
        self.assertEqual(sd.check_config_claims(root), [])
        write_json(root / "docs/generated/config-metadata.json", {"fields": []})
        self.assert_domain(sd.check_config_claims(root), "configuration", "metadata has no fields")

    def test_lifecycle_unknown_path_class_subsystem_and_mismatch(self):
        root = self.repo()
        p = root / "docs/configuration.md"
        write(
            p,
            """| Path | Lifecycle | Subsystem |
| --- | --- | --- |
| `missing.path` | `hot_reload` | `runtime` |
| `global.timeout` | `made_up` | `runtime` |
| `cache.enabled` | `restart_required` | `made_up` |
| `cache.enabled` | `hot_reload` | `runtime` |
""",
        )
        findings = sd.check_lifecycle_claims(root)
        self.assert_domain(findings, "lifecycle", "unknown lifecycle path")
        self.assert_domain(findings, "lifecycle", "unknown lifecycle class")
        self.assert_domain(findings, "lifecycle", "unknown lifecycle subsystem")
        self.assert_domain(findings, "lifecycle", "documents lifecycle")
        self.assertTrue(any("documents subsystem" in f.claim for f in findings))

    def test_lifecycle_dict_field_projection_is_supported(self):
        root = self.repo()
        data = json.loads((root / "docs/generated/config-lifecycle.json").read_text())
        data["fields"] = {row["path"]: row for row in data["fields"]}
        write_json(root / "docs/generated/config-lifecycle.json", data)
        self.assertEqual(sd.check_lifecycle_claims(root), [])

    def test_metric_current_histogram_and_alias_pass_removed_fails(self):
        root = self.repo()
        p = root / "docs/observability.md"
        write(p, "`jul_http_requests_total` `jul_request_duration_seconds_sum` `jul_request_latency_seconds`\n")
        self.assertEqual(sd.check_metric_claims(root), [])
        write(p, p.read_text() + "`jul_removed_metric_total`\n")
        self.assert_domain(sd.check_metric_claims(root), "metrics", "jul_removed_metric_total")

    def test_metric_console_consumer_is_scanned_and_historical_is_not(self):
        root = self.repo()
        write(root / "internal/admin/ui/src/metrics.ts", "const q = 'jul_removed_metric_total'\n")
        self.assert_domain(sd.check_metric_claims(root), "metrics", "jul_removed_metric_total")
        (root / "internal/admin/ui/src/metrics.ts").unlink()
        write(root / "docs/reviews/previous_reviews/old.md", "`jul_removed_metric_total`\n")
        self.assertEqual(sd.check_metric_claims(root), [])

    def test_api_templated_and_concrete_paths_pass_stale_v1_fails(self):
        root = self.repo()
        self.assertEqual(sd.check_api_claims(root), [])
        p = root / "docs/admin-api.md"
        write(p, p.read_text() + "`POST /api/v1/missing`\n")
        self.assert_domain(sd.check_api_claims(root), "external API", "POST /api/v1/missing")

    def test_api_internal_route_prose_is_ignored_but_supported_table_fails(self):
        root = self.repo()
        p = root / "docs/admin-api.md"
        write(p, "Console uses `POST /api/private` and it is internal.\n")
        self.assertEqual(sd.check_api_claims(root), [])
        write(p, "| Path | Method | Classification |\n| --- | --- | --- |\n| `/api/private` | POST | external |\n")
        self.assert_domain(sd.check_api_claims(root), "external API", "POST /api/private")

    def test_api_query_is_stripped_and_wrong_method_fails(self):
        root = self.repo()
        p = root / "docs/admin-api.md"
        write(p, "`POST /api/v1/config/plan?base_version=abc`\n")
        self.assertEqual(sd.check_api_claims(root), [])
        write(p, "`GET /api/v1/config/plan`\n")
        self.assert_domain(sd.check_api_claims(root), "external API", "GET /api/v1/config/plan")

    def test_feature_canonical_marker_and_explicit_feature_marker(self):
        root = self.repo()
        self.assertEqual(sd.check_feature_claims(root), [])
        write(root / "docs/one.md", "**Maturity:** GA\n")
        self.assert_domain(sd.check_feature_claims(root), "feature maturity", "F-1 canonical guide")
        write(root / "docs/one.md", "**Maturity:** Beta\n")
        write(root / "docs/extra.md", "**Feature:** F-1 **Maturity:** GA\n**Feature:** F-404 **Maturity:** Beta\n")
        findings = sd.check_feature_claims(root)
        self.assert_domain(findings, "feature maturity", "F-1 claims")
        self.assert_domain(findings, "feature maturity", "unknown feature ID")

    def test_feature_shared_guide_bare_maturity_is_intentionally_not_inferred(self):
        root = self.repo()
        write(root / "docs/shared.md", "**Maturity:** Deprecated\n")
        self.assertEqual(sd.check_feature_claims(root), [])

    def test_feature_invalid_manifest_and_missing_yaml_are_actionable(self):
        root = self.repo()
        write(root / "docs/feature-status.yaml", ": bad: yaml: [\n")
        self.assert_domain(sd.check_feature_claims(root), "feature maturity", "mapping")
        with mock.patch.object(sd, "yaml", None):
            findings = sd.check_feature_claims(root)
        self.assert_domain(findings, "feature maturity", "PyYAML")

    def test_cli_valid_nonexistent_command_and_exit_code_drift(self):
        root = self.repo()
        self.assertEqual(sd.check_cli_claims(root, help_text=HELP, capabilities=CAPS), [])
        p = root / "docs/remote-cli.md"
        text = p.read_text().replace("| `jul export` | read |", "| `jul explode` | read |")
        text = text.replace("| 9 | j |", "| 10 | j |")
        write(p, text)
        findings = sd.check_cli_claims(root, help_text=HELP, capabilities=CAPS)
        self.assert_domain(findings, "remote CLI", "jul explode")
        self.assert_domain(findings, "remote CLI exits", "documentation exposes")

    def test_cli_help_exit_contract_mismatch_and_empty_remote_surface(self):
        root = self.repo()
        bad_help = HELP.replace("  9  internal\n", "")
        self.assert_domain(sd.check_cli_claims(root, help_text=bad_help, capabilities=CAPS), "remote CLI exits", "help exposes")
        no_remote = "usage only\n"
        findings = sd.check_cli_claims(root, help_text=no_remote, capabilities=CAPS)
        self.assert_domain(findings, "remote CLI", "remote automation section missing")

    def test_cli_load_failure_is_actionable(self):
        root = self.repo()
        with mock.patch.object(sd, "load_cli_authority", side_effect=RuntimeError("boom")):
            self.assert_domain(sd.check_cli_claims(root), "remote CLI", "boom")
            self.assert_domain(sd.check_capability_claims(root), "build capability", "boom")
            self.assert_domain(sd.check_repository(root), "remote CLI", "boom")

    def test_load_cli_authority_success_and_failures(self):
        root = self.repo()
        ok_help = mock.Mock(returncode=0, stdout="", stderr=HELP)
        ok_caps = mock.Mock(returncode=0, stdout=json.dumps(CAPS), stderr="")
        with mock.patch.object(sd, "_run", side_effect=[ok_help, ok_caps]):
            help_text, caps = sd.load_cli_authority(root)
        self.assertIn("Remote automation", help_text)
        self.assertEqual(caps["exit_codes"][9]["code"], 9)
        bad = mock.Mock(returncode=2, stdout="", stderr="compile failed")
        with mock.patch.object(sd, "_run", return_value=bad):
            with self.assertRaisesRegex(RuntimeError, "jul --help failed"):
                sd.load_cli_authority(root)
        help_nonzero = mock.Mock(returncode=2, stdout="", stderr=HELP)
        caps_bad = mock.Mock(returncode=1, stdout="", stderr="caps failed")
        with mock.patch.object(sd, "_run", side_effect=[help_nonzero, caps_bad]):
            with self.assertRaisesRegex(RuntimeError, "capabilities --json failed"):
                sd.load_cli_authority(root)

    def test_capability_structured_and_prose_claims(self):
        root = self.repo()
        self.assertEqual(sd.check_capability_claims(root, capabilities=CAPS), [])
        p = root / "docs/index.md"
        write(p, p.read_text() + "Requires the `bogus` build tag.\n| `bogus2` | Broken |\n")
        findings = sd.check_capability_claims(root, capabilities=CAPS)
        self.assert_domain(findings, "build capability", "bogus")
        self.assertFalse(any("bogus2" in f.claim for f in findings))
        write(p, "| Tag | Capability |\n| --- | --- |\n| `bogus2` | Broken |\n")
        self.assert_domain(sd.check_capability_claims(root, capabilities=CAPS), "build capability", "bogus2")

    def test_current_authority_links_missing_surface_and_reference(self):
        root = self.repo()
        p = root / "docs/index.md"
        write(p, "# Index\n")
        findings = sd.check_current_authority_links(root)
        self.assert_domain(findings, "current authority", "missing current authority reference")
        (root / "docs/status.md").unlink()
        self.assert_domain(sd.check_current_authority_links(root), "current authority", "surface is missing")

    def test_deterministic_order_and_diagnostic_location(self):
        root = self.repo()
        write(root / "docs/observability.md", "jul_z_removed_total\njul_a_removed_total\n")
        one = sd.check_metric_claims(root)
        two = sd.check_metric_claims(root)
        self.assertEqual(one, two)
        self.assertEqual(one, sorted(one))
        self.assertEqual({f.line for f in one}, {1, 2})

    def test_helpers_edge_cases(self):
        root = self.repo()
        outside = Path("/definitely/outside")
        self.assertTrue(sd._rel(root, outside).endswith("definitely/outside"))
        self.assertTrue(sd._path_matches("/api/v1/{id}", "/api/v1/x"))
        self.assertFalse(sd._path_matches("/api/v1/{id}", "/api/v1/x/y"))
        self.assertEqual(sd._value_spellings(True, {}), {"true", "on", "enabled"})
        self.assertIn("null", sd._value_spellings(None, {}))
        self.assertIn("1", sd._value_spellings([1, 2], {}))
        self.assertIn("64 mib", sd._value_spellings("64m", {"scalar": "size"}))
        self.assertEqual(list(sd._table_rows("not a table\n")), [])
        self.assertIsNone(sd._resolve_config_field("does.not.exist", {}))
        self.assertEqual(sd._openapi_operations({}), set())
        self.assertEqual(sd._metric_names({}), set())

    def test_metric_non_utf8_consumer_is_ignored(self):
        root = self.repo()
        p = root / "monitoring/dashboard.bin"
        p.parent.mkdir(parents=True)
        p.write_bytes(b"\xff\xfejul_bad_total")
        self.assertEqual(sd.check_metric_claims(root), [])


if __name__ == "__main__":
    unittest.main(verbosity=2)

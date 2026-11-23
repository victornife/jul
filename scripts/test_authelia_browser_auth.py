#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Deterministic configuration and boundary checks for the #627 recipe."""

from __future__ import annotations

import runpy
import stat
import tempfile
import tomllib
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
EXAMPLE = ROOT / "examples" / "sharing" / "authelia-browser-auth"


class TestAutheliaBrowserAuthExample(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.config = tomllib.loads((EXAMPLE / "jul.toml").read_text(encoding="utf-8"))
        cls.servers = cls.config["servers"]
        cls.by_name = {server["name"]: server for server in cls.servers}

    def test_default_deny_precedes_disjoint_public_identity_and_dashboard_hosts(self) -> None:
        self.assertEqual(self.servers[0]["name"], "default-deny")
        self.assertEqual(self.servers[0]["locations"][0]["return"], 404)
        self.assertEqual(
            self.by_name["public"]["server_names"], ["public.example.test"]
        )
        self.assertEqual(
            self.by_name["authelia"]["server_names"], ["auth.private.example.test"]
        )
        self.assertEqual(
            self.by_name["dashboard"]["server_names"], ["dash.private.example.test"]
        )
        self.assertTrue(
            all(server["listen"] == "127.0.0.1:18443" for server in self.servers)
        )

    def test_dashboard_requires_forward_auth_and_never_uses_shared_cache(self) -> None:
        location = self.by_name["dashboard"]["locations"][0]
        self.assertEqual(location["sharing"]["mode"], "restricted")
        self.assertEqual(
            location["auth"]["forward_auth"]["url"],
            "http://authelia/api/authz/forward-auth",
        )
        self.assertEqual(
            location["auth"]["forward_auth"]["auth_response_headers"],
            ["Remote-User", "Remote-Groups", "Remote-Name", "Remote-Email"],
        )
        self.assertFalse(location["cache"])
        self.assertEqual(location["headers"]["Cookie"], "")
        self.assertEqual(
            self.by_name["dashboard"]["tls"]["cert"], ".lab/tls/server.crt"
        )

    def test_provider_origin_and_admin_are_loopback_only_and_image_is_pinned(self) -> None:
        self.assertEqual(
            self.config["upstreams"][0]["servers"][0]["address"], "127.0.0.1:19091"
        )
        self.assertEqual(
            self.config["upstreams"][1]["servers"][0]["address"], "127.0.0.1:13000"
        )
        self.assertEqual(self.config["admin"]["listen"], "127.0.0.1:19443")
        compose = (EXAMPLE / "compose.yaml").read_text(encoding="utf-8")
        self.assertIn(
            "ghcr.io/authelia/authelia:4.39.28@sha256:"
            "bd97cff4fcbf715b5ff1f9ae286afbe6033afce385302520b0368122d43a6f54",
            compose,
        )
        self.assertIn('"127.0.0.1:19091:9091"', compose)
        self.assertIn("./.lab/authelia:/config:ro", compose)

    def test_provider_denies_by_default_and_cookie_domain_excludes_public_host(self) -> None:
        provider = (
            EXAMPLE / "authelia" / "configuration.yml.template"
        ).read_text(encoding="utf-8")
        self.assertIn("disable_healthcheck: true", provider)
        self.assertIn("default_policy: deny", provider)
        self.assertIn("group:dashboard-users", provider)
        self.assertIn("domain: private.example.test", provider)
        self.assertIn("public.example.test", (EXAMPLE / "jul.toml").read_text(encoding="utf-8"))
        self.assertIn("inactivity: \"30 seconds\"", provider)
        self.assertIn("expiration: \"5 minutes\"", provider)

    def test_browser_harness_does_not_disable_tls_validation_or_print_cookie_values(self) -> None:
        harness = (EXAMPLE / "run_browser_checks.py").read_text(encoding="utf-8")
        self.assertNotIn("ignore_https_errors", harness)
        self.assertIn("certutil", harness)
        self.assertIn("cookie_metadata", harness)
        self.assertNotIn("print(cookie[", harness)

    def test_generated_secret_files_are_private(self) -> None:
        write_private = runpy.run_path(
            str(EXAMPLE / "generate_lab_assets.py")
        )["write_private"]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "configuration.yml"
            write_private(path, "session.secret: disposable-value\n")
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(
                path.read_text(encoding="utf-8"),
                "session.secret: disposable-value\n",
            )


if __name__ == "__main__":
    unittest.main()

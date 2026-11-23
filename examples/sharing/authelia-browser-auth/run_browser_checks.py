#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Run sanitized Authelia/Chromium browser checks against a started local lab."""

from __future__ import annotations

import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import time
from typing import Any
import urllib.error
import urllib.request
from urllib.parse import parse_qs, quote, urlsplit

from playwright.sync_api import BrowserContext, Page, sync_playwright


ROOT = Path(__file__).resolve().parent
LAB = ROOT / ".lab"
DASHBOARD = "https://dash.private.example.test:18443/"
AUTH = "https://auth.private.example.test:18443"
PUBLIC = "https://public.example.test:18443/"
PROVIDER_AUTHZ = "http://127.0.0.1:19091/api/authz/forward-auth"
IDENTITY_HEADERS = {
    "Remote-User": "forged-client",
    "Remote-Groups": "dashboard-users",
}


def run_certutil(*args: str) -> None:
    result = subprocess.run(
        ["certutil", *args],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        detail = result.stderr.strip().splitlines()
        suffix = f": {detail[-1]}" if detail else ""
        raise RuntimeError(f"certutil exited {result.returncode}{suffix}")


def trust_test_ca(home: Path) -> None:
    if shutil.which("certutil") is None:
        raise RuntimeError(
            "certutil is required to trust the test CA in the isolated Chromium profile "
            "(install libnss3-tools); TLS verification will not be disabled"
        )
    database = home / ".pki" / "nssdb"
    database.mkdir(mode=0o700, parents=True)
    run_certutil("-N", "-d", f"sql:{database}", "--empty-password")
    run_certutil(
        "-A",
        "-d",
        f"sql:{database}",
        "-n",
        "Jul 627 disposable test CA",
        "-t",
        "C,,",
        "-i",
        str(LAB / "tls" / "root-ca.pem"),
    )


def backend_counters(context: BrowserContext) -> dict[str, int]:
    response = context.request.get("http://127.0.0.1:13000/__control/counters")
    if response.status != 200:
        raise RuntimeError(f"sample backend counter endpoint returned {response.status}")
    return {key: int(value) for key, value in response.json().items()}


def write_user_database(content: str) -> None:
    path = LAB / "authelia" / "users_database.yml"
    temporary = path.with_suffix(".yml.tmp")
    temporary.write_text(content, encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)


def write_provider_config(content: str) -> None:
    path = LAB / "authelia" / "configuration.yml"
    temporary = path.with_suffix(".yml.tmp")
    temporary.write_text(content, encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)


def restart_provider() -> None:
    result = subprocess.run(
        ["docker", "compose", "restart", "authelia"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        detail = result.stderr.strip().splitlines()
        suffix = f": {detail[-1]}" if detail else ""
        raise RuntimeError(f"Authelia restart failed with status {result.returncode}{suffix}")
    for _ in range(50):
        try:
            with urllib.request.urlopen("http://127.0.0.1:19091/api/health", timeout=1) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            time.sleep(0.1)
    raise RuntimeError("Authelia did not become healthy after restart")


def rotate_session_secret(config: str) -> str:
    lines = config.splitlines()
    in_session = False
    replaced = 0
    for index, line in enumerate(lines):
        if line == "session:":
            in_session = True
        elif in_session and line.startswith("  secret: "):
            lines[index] = f"  secret: {secrets.token_urlsafe(48)}"
            replaced += 1
            break
        elif in_session and line and not line.startswith((" ", "\t", "#")):
            break
    if replaced != 1:
        raise RuntimeError("could not identify the generated Authelia session secret")
    return "\n".join(lines) + "\n"


def login(page: Page, username: str, password: str) -> None:
    page.goto(DASHBOARD, wait_until="domcontentloaded", timeout=20_000)
    page.locator('input[placeholder="Username *"]').wait_for(timeout=15_000)
    login_url = urlsplit(page.url)
    query = parse_qs(login_url.query)
    if query.get("rd") != [DASHBOARD] or query.get("rm") != ["GET"]:
        raise AssertionError("Authelia did not receive the exact protected return URL")
    page.fill('input[placeholder="Username *"]', username)
    password_field = page.locator('input[placeholder="Password *"]')
    password_field.fill(password)
    page.get_by_role("button", name="Sign in", exact=True).click()


def cookie_metadata(context: BrowserContext) -> list[dict[str, Any]]:
    return [
        {
            key: cookie.get(key)
            for key in ("name", "domain", "path", "secure", "httpOnly", "sameSite", "expires")
        }
        for cookie in context.cookies(DASHBOARD)
        if cookie["name"] == "jul627_session"
    ]


def new_context(playwright: Any, run_dir: Path, headers: bool = False) -> BrowserContext:
    home = run_dir / "home"
    profile = run_dir / "profile"
    trust_test_ca(home)
    context = playwright.chromium.launch_persistent_context(
        user_data_dir=str(profile),
        headless=True,
        env={**os.environ, "HOME": str(home)},
        args=[
            "--host-resolver-rules=MAP dash.private.example.test 127.0.0.1,"
            "MAP auth.private.example.test 127.0.0.1,"
            "MAP public.example.test 127.0.0.1"
        ],
    )
    if headers:
        context.set_extra_http_headers(IDENTITY_HEADERS)
    return context


def main() -> int:
    credentials = json.loads((LAB / "credentials.json").read_text(encoding="utf-8"))
    report: dict[str, Any] = {
        "fixture": "#627",
        "tls_validation": "enabled; generated CA trusted in isolated Chromium NSS database",
        "browser_mode": "headless",
    }

    with tempfile.TemporaryDirectory(prefix="browser-run-", dir=LAB) as tmp:
        with sync_playwright() as playwright:
            contexts: list[BrowserContext] = []
            try:
                intended = new_context(playwright, Path(tmp) / "intended", headers=True)
                contexts.append(intended)
                page = intended.pages[0]
                protected_responses: list[dict[str, Any]] = []
                page.on(
                    "response",
                    lambda response: protected_responses.append(
                        {
                            "status": response.status,
                            "set_cookie": bool(response.headers.get("set-cookie")),
                            "url": response.url,
                        }
                    )
                    if response.url.startswith(DASHBOARD)
                    else None,
                )
                login(
                    page,
                    credentials["intended"]["username"],
                    credentials["intended"]["password"],
                )
                page.wait_for_url(DASHBOARD + "**", timeout=30_000)
                page.wait_for_load_state("domcontentloaded")
                dashboard = page.locator("body").inner_text()
                data = json.loads(dashboard)
                if data["identity"]["user"] != credentials["intended"]["username"]:
                    raise AssertionError("forged Remote-User reached the application")
                if data["identity"]["groups"] != "dashboard-users":
                    raise AssertionError("provider group identity did not reach the application")
                if data["cookie_received"]:
                    raise AssertionError("the Authelia session cookie reached the dashboard origin")
                restart_provider()
                page.goto(DASHBOARD, wait_until="domcontentloaded", timeout=20_000)
                report["provider_restart_requires_login"] = (
                    urlsplit(page.url).hostname == "auth.private.example.test"
                )
                contexts.remove(intended)
                intended.close()
                intended = new_context(playwright, Path(tmp) / "post-restart", headers=True)
                contexts.append(intended)
                page = intended.pages[0]
                page.on(
                    "response",
                    lambda response: protected_responses.append(
                        {
                            "status": response.status,
                            "set_cookie": bool(response.headers.get("set-cookie")),
                            "url": response.url,
                        }
                    )
                    if response.url.startswith(DASHBOARD)
                    else None,
                )
                login(
                    page,
                    credentials["intended"]["username"],
                    credentials["intended"]["password"],
                )
                page.wait_for_url(DASHBOARD + "**", timeout=30_000)
                data = json.loads(page.locator("body").inner_text())
                if data["identity"]["user"] != credentials["intended"]["username"]:
                    raise AssertionError("provider restart did not preserve or restore intended access")
                success = next(
                    response
                    for response in reversed(protected_responses)
                    if response["status"] == 200 and response["url"] == DASHBOARD
                )
                if success["set_cookie"]:
                    raise AssertionError("Jul unexpectedly relayed a successful-probe Set-Cookie")
                cookies = cookie_metadata(intended)
                if len(cookies) != 1:
                    raise AssertionError("expected one Authelia session cookie for the private host")
                session_cookie = cookies[0]
                if (
                    session_cookie["domain"].lstrip(".") != "private.example.test"
                    or not session_cookie["secure"]
                    or not session_cookie["httpOnly"]
                    or session_cookie["sameSite"] != "Lax"
                ):
                    raise AssertionError("Authelia session cookie flags or scope differ from the fixture")
                public_cookie_names = [
                    cookie["name"] for cookie in intended.cookies(PUBLIC)
                ]
                public_response = page.goto(PUBLIC, wait_until="domcontentloaded")
                if public_response is None or public_response.status != 200:
                    raise AssertionError("the public sibling did not return 200")
                if "JUL-627-PUBLIC-MARKER" not in page.locator("body").inner_text():
                    raise AssertionError("the public static marker was not served")
                if "jul627_session" in public_cookie_names or intended.cookies(PUBLIC):
                    raise AssertionError("the private Authelia cookie was sent to the public host")
                page.goto(DASHBOARD, wait_until="domcontentloaded")
                if not page.locator("body").inner_text().startswith("{"):
                    raise AssertionError("intended session was not retained on the private host")

                provider_cookie = session_cookie["name"] + "=" + next(
                    cookie["value"]
                    for cookie in intended.cookies(DASHBOARD)
                    if cookie["name"] == session_cookie["name"]
                )
                probe_headers = {
                    "Cookie": provider_cookie,
                    "X-Forwarded-Method": "GET",
                    "X-Forwarded-Uri": "/",
                    "X-Forwarded-Host": "dash.private.example.test:18443",
                    "X-Forwarded-Proto": "https",
                    "X-Forwarded-For": "127.0.0.1",
                    "X-Real-Ip": "127.0.0.1",
                }
                stream_baseline = backend_counters(intended)
                page.evaluate(
                    """() => new Promise((resolve, reject) => {
                      const source = new EventSource('/events');
                      window.__jul627Events = source;
                      source.onopen = () => resolve(true);
                      source.onerror = () => reject(new Error('SSE did not open'));
                      setTimeout(() => reject(new Error('SSE open timed out')), 5000);
                    })"""
                )
                stream_counts = backend_counters(intended)
                if stream_counts["streams_opened"] != stream_baseline["streams_opened"] + 1:
                    raise AssertionError("the authorized sample SSE stream did not reach the origin")

                users_path = LAB / "authelia" / "users_database.yml"
                original_users = users_path.read_text(encoding="utf-8")
                if original_users.count("      - dashboard-users") != 1:
                    raise AssertionError("the generated intended-user group entry is not unique")
                try:
                    write_user_database(
                        original_users.replace("      - dashboard-users", "      - visitors", 1)
                    )
                    authz = None
                    for _ in range(50):
                        authz = intended.request.get(
                            PROVIDER_AUTHZ,
                            headers=probe_headers,
                        )
                        if authz.status == 403:
                            break
                        time.sleep(0.1)
                    if authz is None or authz.status != 403:
                        raise AssertionError("Authelia did not deny the session after group removal")
                    stream_counts = backend_counters(intended)
                    if stream_counts["streams_active"] != stream_baseline["streams_active"] + 1:
                        raise AssertionError("group removal unexpectedly changed the established SSE connection")
                    page.evaluate("window.__jul627Events.close()")
                    for _ in range(50):
                        stream_counts = backend_counters(intended)
                        if stream_counts["streams_active"] == stream_baseline["streams_active"]:
                            break
                        time.sleep(0.1)
                    if stream_counts["streams_active"] != stream_baseline["streams_active"]:
                        raise AssertionError("the sample SSE stream did not close after client cancellation")
                    origin_before_denied_request = backend_counters(intended)["requests"]
                    denied_response = page.goto(DASHBOARD, wait_until="domcontentloaded")
                    if denied_response is None or denied_response.status != 403:
                        raise AssertionError("a new request after group removal was not denied")
                    if backend_counters(intended)["requests"] != origin_before_denied_request:
                        raise AssertionError("a request after group removal reached the origin")
                finally:
                    write_user_database(original_users)

                restored = None
                for _ in range(50):
                    restored = intended.request.get(
                        PROVIDER_AUTHZ,
                        headers=probe_headers,
                    )
                    if restored.status == 200:
                        break
                    time.sleep(0.1)
                if restored is None or restored.status != 200:
                    raise AssertionError("restoring the group did not restore provider authorization")

                public_response = page.goto(PUBLIC, wait_until="domcontentloaded")
                if public_response is None or public_response.status != 200:
                    raise AssertionError("the public sibling did not return 200 after group restoration")

                authz = intended.request.get(
                    PROVIDER_AUTHZ,
                    headers=probe_headers,
                )
                if authz.status != 200:
                    raise AssertionError("Authelia's successful ForwardAuth probe did not return 200")
                report["provider_success_set_cookie"] = bool(authz.headers.get("set-cookie"))
                report["jul_success_set_cookie"] = success["set_cookie"]
                if report["provider_success_set_cookie"]:
                    raise AssertionError("provider refresh cookie behavior changed; review Jul relay contract")

                logout_url = AUTH + "/logout?rd=" + quote(DASHBOARD, safe="")
                page.goto(logout_url, wait_until="domcontentloaded", timeout=20_000)
                page.locator('input[placeholder="Username *"]').wait_for(timeout=15_000)
                report["logout_requires_login_again"] = True
                report["intended_identity"] = data["identity"]
                report["dashboard_cookie_forwarded"] = data["cookie_received"]
                report["cookie_metadata"] = session_cookie
                report["protected_statuses"] = [
                    item["status"] for item in protected_responses
                ]
                report["public_cookie_names"] = public_cookie_names
                report["sse_opened_at_origin"] = True
                report["sse_closed_after_client_cancellation"] = True
                report["group_removal_denied_new_request"] = True
                report["group_removal_kept_existing_stream_open"] = True
                report["group_restoration_reauthorized_session"] = True

                excluded = new_context(playwright, Path(tmp) / "excluded", headers=True)
                contexts.append(excluded)
                denied_page = excluded.pages[0]
                before = backend_counters(excluded)["requests"]
                denied_responses: list[int] = []
                denied_page.on(
                    "response",
                    lambda response: denied_responses.append(response.status)
                    if response.url.startswith(DASHBOARD)
                    else None,
                )
                login(
                    denied_page,
                    credentials["excluded"]["username"],
                    credentials["excluded"]["password"],
                )
                denied_page.wait_for_url(DASHBOARD + "**", timeout=30_000)
                denied_page.get_by_text("403 Forbidden", exact=True).wait_for(timeout=10_000)
                after = backend_counters(excluded)["requests"]
                if before != after:
                    raise AssertionError("excluded user reached the dashboard origin")
                report["excluded_statuses"] = denied_responses
                report["excluded_origin_count_unchanged"] = before == after

                expiring = new_context(playwright, Path(tmp) / "expiry")
                contexts.append(expiring)
                expiry_page = expiring.pages[0]
                login(
                    expiry_page,
                    credentials["intended"]["username"],
                    credentials["intended"]["password"],
                )
                expiry_page.wait_for_url(DASHBOARD + "**", timeout=30_000)
                time.sleep(35)
                expiry_page.goto(DASHBOARD, wait_until="domcontentloaded", timeout=20_000)
                expiry_page.locator('input[placeholder="Username *"]').wait_for(timeout=15_000)
                report["idle_session_requires_login_after_seconds"] = 35
                report["idle_expiry_returns_to_login"] = True

                rotation = new_context(playwright, Path(tmp) / "rotation", headers=True)
                contexts.append(rotation)
                rotation_page = rotation.pages[0]
                login(
                    rotation_page,
                    credentials["intended"]["username"],
                    credentials["intended"]["password"],
                )
                rotation_page.wait_for_url(DASHBOARD + "**", timeout=30_000)
                config_path = LAB / "authelia" / "configuration.yml"
                original_config = config_path.read_text(encoding="utf-8")
                try:
                    write_provider_config(rotate_session_secret(original_config))
                    restart_provider()
                    rotation_page.goto(DASHBOARD, wait_until="domcontentloaded", timeout=20_000)
                    rotation_page.locator('input[placeholder="Username *"]').wait_for(timeout=15_000)
                    report["session_secret_rotation_requires_login"] = True
                finally:
                    write_provider_config(original_config)
                    restart_provider()

                version_browser = playwright.chromium.launch(headless=True)
                report["browser_version"] = version_browser.version
                version_browser.close()
                report["result"] = "passed"
            finally:
                for context in contexts:
                    context.close()

    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

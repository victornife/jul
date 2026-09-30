#!/usr/bin/env python3
"""Scheduled protocol conformance lane (#513).

Each suite starts Jul (the full-profile binary passed with --jul) with a
suite-specific configuration, runs one external or corpus-driven test tool
against it, writes a machine-readable report under --out, and compares every
failing case with testdata/conformance/allowlist.yaml. A failure that is not
allow-listed fails the suite; an allow-listed case that now passes is reported
as stale so the list shrinks as deviations are fixed.

Suites: h2spec, framing, cache, autobahn, h3. Tools are supplied by the
workflow (see .github/workflows/conformance.yml); the framing corpus needs
nothing but Python.
"""

import argparse
import contextlib
import functools
import http.server
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
DATA = ROOT / "testdata" / "conformance"


# --- process helpers -------------------------------------------------------


def free_port(kind=socket.SOCK_STREAM):
    with socket.socket(socket.AF_INET, kind) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_tcp(port, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with contextlib.suppress(OSError), socket.create_connection(("127.0.0.1", port), timeout=1):
            return
        time.sleep(0.2)
    raise RuntimeError(f"nothing listening on 127.0.0.1:{port}")


@contextlib.contextmanager
def run_jul(jul, config_text, out, name, ports):
    cfg = out / f"{name}.toml"
    cfg.write_text(config_text)
    subprocess.run([jul, "check", "-config", str(cfg)], check=True)
    log = open(out / f"{name}-jul.log", "w")
    proc = subprocess.Popen([jul, "-config", str(cfg)], stdout=log, stderr=subprocess.STDOUT)
    try:
        for port in ports:
            wait_tcp(port)
        yield proc
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
        log.close()


def self_signed(out):
    cert, key = out / "cert.pem", out / "key.pem"
    if not cert.exists():
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
                        "-keyout", str(key), "-out", str(cert), "-subj", "/CN=localhost",
                        "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1"],
                       check=True, capture_output=True)
    return cert, key


def static_root(out):
    www = out / "www"
    (www / "proxy").mkdir(parents=True, exist_ok=True)
    page = "<!doctype html><title>conformance</title><h1>conformance</h1>\n" + "x" * 2048 + "\n"
    (www / "index.html").write_text(page)
    (www / "proxy" / "index.html").write_text(page)
    return www


@contextlib.contextmanager
def static_backend(www):
    """An HTTP/1.1 keep-alive origin serving www on a free port."""
    handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(www))
    handler.func.protocol_version = "HTTP/1.1"
    handler.func.log_message = lambda *a: None
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        yield srv.server_address[1]
    finally:
        srv.shutdown()


# --- verdicts --------------------------------------------------------------


def judge(suite, results, out):
    """results maps case id -> (passed: bool, detail: str)."""
    listed = {e["id"]: e for e in (yaml.safe_load((DATA / "allowlist.yaml").read_text()) or {}).get(suite, [])}
    for entry in listed.values():
        for field in ("id", "rationale", "issue"):
            if not entry.get(field):
                raise SystemExit(f"allowlist {suite}: entry {entry!r} lacks {field}")
    unexpected, allowed, stale = [], [], []
    for case, (passed, detail) in sorted(results.items()):
        entry = listed.get(case) or next((e for key, e in listed.items() if key.endswith("*") and case.startswith(key[:-1])), None)
        if not passed and entry is None:
            unexpected.append((case, detail))
        elif not passed:
            allowed.append((case, entry["issue"]))
        elif case in listed:
            stale.append(case)
    report = {
        "suite": suite,
        "total": len(results),
        "passed": sum(1 for p, _ in results.values() if p),
        "allow_listed_failures": [{"id": c, "issue": i} for c, i in allowed],
        "unexpected_failures": [{"id": c, "detail": d} for c, d in unexpected],
        "stale_allowlist_entries": stale,
    }
    (out / f"{suite}-verdict.json").write_text(json.dumps(report, indent=2))
    print(f"{suite}: {report['passed']}/{report['total']} passed, "
          f"{len(allowed)} allow-listed, {len(unexpected)} unexpected, {len(stale)} stale allowlist entries")
    for case, detail in unexpected:
        print(f"  UNEXPECTED {case}: {detail}")
    for case in stale:
        print(f"  STALE (now passing, remove from allowlist) {case}")
    return 1 if unexpected else 0


# --- h2spec ----------------------------------------------------------------


def suite_h2spec(args, out):
    cert, key = self_signed(out)
    www = static_root(out)
    tls, h2c = free_port(), free_port()
    with static_backend(www) as origin:
        locations = f"""
  [[servers.locations]]
  match = {{ type = "prefix", path = "/proxy/" }}
  proxy_pass = "http://127.0.0.1:{origin}"
  [[servers.locations]]
  match = {{ type = "prefix", path = "/" }}
  root = "{www}"
"""
        config = f"""
[global]
log_level = "warn"

[observability.access_log]
enabled = false

[[servers]]
listen = "127.0.0.1:{tls}"
  [servers.tls]
  enabled = true
  cert = "{cert}"
  key = "{key}"
{locations}
[[servers]]
listen = "127.0.0.1:{h2c}"
h2c = true
{locations}"""
        results = {}
        with run_jul(args.jul, config, out, "h2spec", [tls, h2c]):
            targets = {
                "tls-static": ["-t", "-k", "-p", str(tls), "-P", "/"],
                "tls-proxy": ["-t", "-k", "-p", str(tls), "-P", "/proxy/"],
                "h2c-static": ["-p", str(h2c), "-P", "/"],
                "h2c-proxy": ["-p", str(h2c), "-P", "/proxy/"],
            }
            for target, flags in targets.items():
                report = out / f"h2spec-{target}.xml"
                with open(out / f"h2spec-{target}.txt", "w") as text:
                    subprocess.run([args.h2spec, "-h", "127.0.0.1", "-o", "5", "-j", str(report), *flags],
                                   stdout=text, stderr=subprocess.STDOUT)
                for tc in ET.parse(report).iter("testcase"):
                    failure = tc.find("failure") if tc.find("failure") is not None else tc.find("error")
                    case = f"{tc.get('package')}: {tc.get('classname')} [{target}]"
                    results[case] = (failure is None, (failure.text or "").strip()[:300] if failure is not None else "")
    return judge("h2spec", results, out)


# --- ambiguous framing corpus ------------------------------------------------


class Recorder(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    seen = []
    lock = threading.Lock()

    def log_message(self, *a):
        pass

    def _record(self):
        if self.path == "/__log":
            body = json.dumps(Recorder.seen).encode()
        elif self.path == "/__reset":
            with Recorder.lock:
                Recorder.seen.clear()
            body = b"{}"
        else:
            data, complete = self._read_body()
            with Recorder.lock:
                Recorder.seen.append({"method": self.command, "path": self.path,
                                      "body": data.decode("latin-1"), "complete": complete,
                                      "te": self.headers.get("Transfer-Encoding")})
            if not complete:
                self.close_connection = True
                return
            body = b"ok"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_body(self):
        """Read the body per its framing; complete is False if it was cut off."""
        try:
            if (self.headers.get("Transfer-Encoding") or "").lower() == "chunked":
                data = b""
                while True:
                    size_line = self.rfile.readline(1024)
                    if not size_line.endswith(b"\n"):
                        return data, False
                    size = int(size_line.split(b";")[0].strip(), 16)
                    if size == 0:
                        while self.rfile.readline(1024) not in (b"\r\n", b"\n", b""):
                            pass
                        return data, True
                    chunk = self.rfile.read(size + 2)
                    if len(chunk) != size + 2:
                        return data + chunk, False
                    data += chunk[:size]
            length = int(self.headers.get("Content-Length") or 0)
            data = self.rfile.read(length) if length else b""
            return data, len(data) == length
        except (OSError, ValueError):
            return b"", False

    do_GET = do_POST = do_PUT = do_HEAD = _record


def exchange(port, raw, wait=1.5):
    """Send raw bytes on a fresh connection; return the response status codes
    and whether Jul closed the connection.

    The write side stays open: a half-close reads as a client abort (499).
    """
    data, closed = b"", False
    with socket.create_connection(("127.0.0.1", port), timeout=wait) as s:
        s.sendall(raw)
        deadline = time.monotonic() + wait
        while time.monotonic() < deadline:
            try:
                chunk = s.recv(65536)
            except socket.timeout:
                break
            except OSError:
                closed = True
                break
            if not chunk:
                closed = True
                break
            data += chunk
    return [int(m) for m in re.findall(rb"HTTP/1\.[01] (\d{3}) ", data)], closed


def suite_framing(args, out):
    corpus = yaml.safe_load((DATA / "framing.yaml").read_text())["cases"]
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Recorder)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    origin = srv.server_address[1]
    port = free_port()
    config = f"""
[global]
log_level = "warn"

[observability.access_log]
enabled = false

[[servers]]
listen = "127.0.0.1:{port}"
  [[servers.locations]]
  match = {{ type = "prefix", path = "/" }}
  proxy_pass = "http://127.0.0.1:{origin}"
"""
    results, transcript = {}, []
    with run_jul(args.jul, config, out, "framing", [port]):
        for case in corpus:
            urllib.request.urlopen(f"http://127.0.0.1:{origin}/__reset", timeout=5).read()
            raw = case["raw"].encode("latin-1")
            statuses, closed = exchange(port, raw)
            time.sleep(0.2)
            seen = json.loads(urllib.request.urlopen(f"http://127.0.0.1:{origin}/__log", timeout=5).read())
            expect = case["expect"]
            complete = [r for r in seen if r["complete"]]
            if expect == "reject":
                # A streaming proxy may have started forwarding before the
                # body proved malformed; the origin must never receive a
                # complete request from it.
                ok = bool(statuses) and all(s >= 400 for s in statuses) and not complete
            else:
                want = expect["forward"]
                got = [{k: r[k] for k in ("method", "path", "body")} for r in seen]
                ok = got == want and len(complete) == len(seen) and len(statuses) >= len(want) and all(s < 400 for s in statuses[:len(want)])
                if expect.get("close"):
                    ok = ok and closed
            detail = f"statuses={statuses} closed={closed} backend={[(r['method'], r['path'], r['body'][:40], r['complete']) for r in seen]}"
            results[case["id"]] = (ok, detail)
            transcript.append({"id": case["id"], "expect": expect, "statuses": statuses, "closed": closed, "backend": seen, "passed": ok})
    srv.shutdown()
    (out / "framing-report.json").write_text(json.dumps(transcript, indent=2))
    return judge("framing", results, out)


# --- HTTP caching (http-tests/cache-tests) -----------------------------------


def cache_test_kinds(tests):
    """Map every cache-tests id to its kind: required, optimal or check."""
    script = ("import suites from '" + (tests / "tests" / "index.mjs").as_uri() + "';"
              "const o = {}; for (const s of suites) for (const t of s.tests) o[t.id] = t.kind || 'required';"
              "console.log(JSON.stringify(o));")
    run = subprocess.run(["node", "--input-type=module", "-e", script], capture_output=True, text=True, check=True)
    return json.loads(run.stdout)


def suite_cache(args, out):
    """Only `required` cache-tests cases (RFC 9111 MUST-level) gate the suite;
    `optimal` and `check` results are recorded in the report for information."""
    tests = Path(args.cache_tests).resolve()
    origin, port = free_port(), free_port()
    config = f"""
[global]
log_level = "warn"

[observability.access_log]
enabled = false

[cache]
enabled = true
memory_max_size = "64m"

# Several cache-tests cases make the origin drop the connection on purpose
# (stale-close and friends). Those are real backend failures, but ejecting the
# only origin would turn every later case into a 503, so passive ejection is
# effectively off for this pool.
[[upstreams]]
name = "origin"
servers = [{{ address = "127.0.0.1:{origin}" }}]
  [upstreams.resilience]
  max_fails = 100000

[[servers]]
listen = "127.0.0.1:{port}"
  [[servers.locations]]
  match = {{ type = "prefix", path = "/" }}
  proxy_pass = "http://origin"
  cache = true
"""
    kinds = cache_test_kinds(tests)
    server = subprocess.Popen(["npm", "run", "--silent", "server", f"--port={origin}"], cwd=tests,
                              stdout=open(out / "cache-tests-server.log", "w"), stderr=subprocess.STDOUT)
    results, informational = {}, {}
    try:
        wait_tcp(origin)
        with run_jul(args.jul, config, out, "cache", [port]):
            run = subprocess.run(["npm", "run", "--silent", "cli", f"--base=http://127.0.0.1:{port}"],
                                 cwd=tests, capture_output=True, text=True, timeout=1800)
            (out / "cache-tests-results.json").write_text(run.stdout)
            (out / "cache-tests-cli.log").write_text(run.stderr)
    finally:
        server.terminate()
        server.wait(timeout=15)
    for case, value in json.loads(run.stdout).items():
        kind = kinds.get(case, "required")
        entry = (value is True, json.dumps(value)[:300])
        if kind == "required":
            results[f"cache-tests {case}"] = entry
        else:
            informational[case] = {"kind": kind, "passed": entry[0], "detail": entry[1]}
    (out / "cache-tests-informational.json").write_text(json.dumps(informational, indent=2))
    print(f"cache-tests optimal/check (informational): "
          f"{sum(1 for v in informational.values() if v['passed'])}/{len(informational)} passed")
    results.update(validators_across_codings(args, out))
    return judge("cache", results, out)


def validators_across_codings(args, out):
    """#504: a compressed representation must not reuse the identity's strong
    validator, so If-Range with it can never select identity byte ranges."""
    www = static_root(out)
    port = free_port()
    config = f"""
[global]
log_level = "warn"

[observability.access_log]
enabled = false

[compression]
enabled = true
encoders = ["gzip"]
min_size = "1k"

[[servers]]
listen = "127.0.0.1:{port}"
  [[servers.locations]]
  match = {{ type = "prefix", path = "/" }}
  root = "{www}"
"""
    base = f"http://127.0.0.1:{port}/index.html"
    with run_jul(args.jul, config, out, "validators", [port]):
        ident = urllib.request.urlopen(urllib.request.Request(base, headers={"Accept-Encoding": "identity"}), timeout=5)
        gz = urllib.request.urlopen(urllib.request.Request(base, headers={"Accept-Encoding": "gzip"}), timeout=5)
        strong, coded = ident.headers.get("ETag", ""), gz.headers.get("ETag", "")
        req = urllib.request.Request(base, headers={"Accept-Encoding": "identity", "Range": "bytes=10-", "If-Range": coded})
        status = urllib.request.urlopen(req, timeout=5).status
    return {
        "custom etag-weak-when-compressed": (
            gz.headers.get("Content-Encoding") == "gzip" and coded.startswith("W/") and coded != strong,
            f"identity={strong!r} gzip={coded!r} encoding={gz.headers.get('Content-Encoding')!r}"),
        "custom if-range-with-coded-etag-sends-full": (status == 200, f"status={status}"),
    }


# --- WebSocket (Autobahn|Testsuite) ----------------------------------------


def suite_autobahn(args, out):
    echo, port = free_port(), free_port()
    reports = out / "autobahn"
    reports.mkdir(exist_ok=True)
    image = args.autobahn_image
    config = f"""
[global]
log_level = "warn"

[observability.access_log]
enabled = false

[[servers]]
listen = "127.0.0.1:{port}"
  [[servers.locations]]
  match = {{ type = "prefix", path = "/" }}
  proxy_pass = "http://127.0.0.1:{echo}"
"""
    spec = {"outdir": "/reports", "servers": [{"agent": "jul-proxy", "url": f"ws://127.0.0.1:{port}"}],
            "cases": ["*"], "exclude-cases": ["12.*", "13.*"], "exclude-agent-cases": {}}
    (reports / "fuzzingclient.json").write_text(json.dumps(spec))
    name = f"jul-autobahn-echo-{os.getpid()}"
    subprocess.run(["docker", "run", "-d", "--rm", "--name", name, "--network", "host", image,
                    "wstest", "-m", "echoserver", "-w", f"ws://127.0.0.1:{echo}"], check=True, capture_output=True)
    results = {}
    try:
        wait_tcp(echo, timeout=60)
        with run_jul(args.jul, config, out, "autobahn", [port]):
            subprocess.run(["docker", "run", "--rm", "--network", "host", "-v", f"{reports}:/reports", image,
                            "wstest", "-m", "fuzzingclient", "-s", "/reports/fuzzingclient.json"],
                           check=True, stdout=open(out / "autobahn-client.log", "w"), stderr=subprocess.STDOUT)
        index = json.loads((reports / "index.json").read_text())
        for case, res in index["jul-proxy"].items():
            behavior, close = res["behavior"], res["behaviorClose"]
            passed = behavior in ("OK", "NON-STRICT", "INFORMATIONAL") and close in ("OK", "INFORMATIONAL")
            results[f"autobahn {case}"] = (passed, f"behavior={behavior} close={close}")
    finally:
        subprocess.run(["docker", "rm", "-f", name], capture_output=True)
    return judge("autobahn", results, out)


# --- HTTP/3 (h3spec) --------------------------------------------------------


def suite_h3(args, out):
    cert, key = self_signed(out)
    www = static_root(out)
    port = free_port()
    config = f"""
[global]
log_level = "warn"

[observability.access_log]
enabled = false

[[servers]]
listen = "127.0.0.1:{port}"
  [servers.tls]
  enabled = true
  cert = "{cert}"
  key = "{key}"
  [servers.http3]
  enabled = true
  [[servers.locations]]
  match = {{ type = "prefix", path = "/" }}
  root = "{www}"
"""
    results = {}
    with run_jul(args.jul, config, out, "h3", [port]):
        time.sleep(1)
        run = subprocess.run([args.h3spec, "-n", "127.0.0.1", str(port)], capture_output=True, text=True, timeout=900)
        text = run.stdout + run.stderr
        (out / "h3spec.txt").write_text(text)
    # hspec prints one line per example; failures are numbered and listed at
    # the end. Record each example as passed unless it is listed as failed.
    section = ""
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith(("Finished", "Failures", "Randomized", "To rerun")):
            continue
        indent = len(line) - len(line.lstrip())
        if indent <= 2 and not stripped.endswith(("✔", "✘", "FAILED")) and not re.search(r"\[(✔|✘)\]", stripped):
            section = stripped
            continue
        m = re.match(r"(.*?)\s*(\[✔\]|\[✘\]|FAILED \[\d+\])\s*$", stripped)
        if m:
            name = f"h3spec {section}: {m.group(1).strip()}"
            results[name] = ("✔" in m.group(2), m.group(2))
    if not results:
        results["h3spec run"] = (run.returncode == 0, text[-300:])
    return judge("h3", results, out)


SUITES = {"h2spec": suite_h2spec, "framing": suite_framing, "cache": suite_cache,
          "autobahn": suite_autobahn, "h3": suite_h3}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("suite", choices=sorted(SUITES))
    ap.add_argument("--jul", required=True, help="full-profile jul binary")
    ap.add_argument("--out", required=True, help="report directory")
    ap.add_argument("--h2spec", default="h2spec")
    ap.add_argument("--h3spec", default="h3spec")
    ap.add_argument("--cache-tests", default="cache-tests", help="checkout of http-tests/cache-tests")
    ap.add_argument("--autobahn-image", default="crossbario/autobahn-testsuite:0.8.2")
    args = ap.parse_args()
    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    args.jul = str(Path(args.jul).resolve())
    sys.exit(SUITES[args.suite](args, out))


if __name__ == "__main__":
    main()

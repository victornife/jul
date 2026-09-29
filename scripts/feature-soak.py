#!/usr/bin/env python3
"""Linux real-binary post-GA soak candidates for Y1-08 and Y1-09.

The short CI preflight proves the harness works. Only a run lasting at least
one hour for one feature can be considered against ADR 0005's duration floor.
The JSON artifact records what was actually exercised, not a maturity verdict.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import resource
import socket
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import urlopen

PAYLOAD = b"jul-feature-soak-ok\n"
CLEAN_NGINX = 'http { server { listen 8080; location / { return 200; } } }\n'
BLOCKING_NGINX = 'http { server { listen 8080; location / { if ($x) { return 403; } } } }\n'


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def pre_run_manifest(args):
    """Retain the exact binary, harness and fixture identity before exercising them."""
    binary = args.jul.resolve()
    build = json.loads(command([str(binary), "version", "-json"], 0).stdout)
    capabilities = json.loads(command([str(binary), "capabilities", "-json"], 0).stdout)
    if args.mode == "importer" and capabilities.get("features", {}).get("importer") is not True:
        raise RuntimeError("binary lacks importer capability")
    expected_sha = os.getenv("JUL_SOAK_SHA", "local")
    if expected_sha != "local" and build.get("commit") != expected_sha:
        raise RuntimeError(f"binary commit {build.get('commit')} differs from requested {expected_sha}")
    manifest = {
        "mode": args.mode,
        "requested_seconds": args.seconds,
        "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "source_sha": expected_sha,
        "harness_sha256": sha256(Path(__file__).read_bytes()),
        "binary_sha256": sha256(binary.read_bytes()),
        "build": build,
        "capabilities": capabilities,
        "environment": {
            "platform": platform.platform(), "python": sys.version.split()[0],
            "runner_os": os.getenv("RUNNER_OS"),
            "runner_arch": os.getenv("RUNNER_ARCH"),
            "runner_image": os.getenv("ImageOS"),
            "runner_image_version": os.getenv("ImageVersion"),
        },
        "fixtures": {
            "response_sha256": sha256(PAYLOAD),
            "nginx_clean_sha256": sha256(CLEAN_NGINX.encode()),
            "nginx_blocking_sha256": sha256(BLOCKING_NGINX.encode()),
            "lint": "env-backed admin token; literal-token warning and strict exit 2",
        },
    }
    write_json(args.out / "manifest.json", manifest)
    return manifest


def command(args, expected, *, env=None):
    result = subprocess.run(args, text=True, capture_output=True, timeout=30, env=env, check=False)
    if result.returncode != expected:
        raise RuntimeError(
            f"{args[1:4]} exited {result.returncode}, expected {expected}: "
            f"{result.stderr[-1200:]} {result.stdout[-300:]}"
        )
    return result


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def process_sample(proc):
    status = Path(f"/proc/{proc.pid}/status").read_text()
    rss = next(int(line.split()[1]) for line in status.splitlines() if line.startswith("VmRSS:"))
    return {"rss_kib": rss, "fds": len(list(Path(f"/proc/{proc.pid}/fd").iterdir()))}


def get_body(url):
    with urlopen(url, timeout=5) as response:
        if response.status != 200:
            raise RuntimeError(f"{url}: HTTP {response.status}")
        return response.read()


def wait_ready(proc, url, expected):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"Jul exited during startup: {proc.returncode}")
        try:
            if get_body(url) == expected:
                return
        except OSError:
            pass
        time.sleep(0.1)
    raise RuntimeError(f"Jul did not serve the expected body at {url}")


def stop(proc):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


def zero_config(binary, seconds, outdir, summary):
    payload = PAYLOAD
    class Backend(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *_):
            pass

    backend = ThreadingHTTPServer(("127.0.0.1", 0), Backend)
    threading.Thread(target=backend.serve_forever, daemon=True).start()
    procs = []
    logs = []
    try:
        with tempfile.TemporaryDirectory(prefix="jul-feature-soak-") as tmp:
            site = Path(tmp) / "public"
            site.mkdir()
            (site / "index.html").write_bytes(payload)
            serve_port, proxy_port = free_port(), free_port()
            if serve_port == proxy_port:
                raise RuntimeError("two zero-config listeners selected the same port")
            modes = {
                "serve": (["run", "--serve", str(site), "--listen", f"127.0.0.1:{serve_port}"], serve_port),
                "proxy": (["run", "--proxy", f"127.0.0.1:{backend.server_port}", "--listen", f"127.0.0.1:{proxy_port}"], proxy_port),
            }
            for name, (args, port) in modes.items():
                log = open(outdir / f"{name}.log", "w", encoding="utf-8")
                logs.append(log)
                proc = subprocess.Popen([str(binary), *args], stdout=log, stderr=subprocess.STDOUT)
                procs.append(proc)
                wait_ready(proc, f"http://127.0.0.1:{port}/", payload)

            fixture = Path(tmp) / "lint.toml"
            fixture.write_text(
                '[global]\nlog_level = "info"\n[admin]\nenabled = true\n'
                'listen = "127.0.0.1:9090"\ntoken = "${env:JUL_ADMIN_TOKEN}"\n'
                '[[servers]]\nlisten = "127.0.0.1:18081"\n'
                '[[servers.locations]]\nmatch = { type = "prefix", path = "/" }\n'
                f'root = {json.dumps(str(site))}\n', encoding="utf-8"
            )
            bad = Path(tmp) / "literal.toml"
            bad.write_text(fixture.read_text().replace('${env:JUL_ADMIN_TOKEN}', 'literal-test-only-secret'))
            env = {**os.environ, "JUL_ADMIN_TOKEN": "test-only-token"}
            urls = {name: f"http://127.0.0.1:{port}/" for name, (_, port) in modes.items()}
            counts = {name: 0 for name in modes}
            failures = []
            end = time.monotonic() + seconds
            halt = threading.Event()

            def load(name):
                while not halt.is_set() and time.monotonic() < end:
                    try:
                        if get_body(urls[name]) != payload:
                            raise RuntimeError("unexpected response body")
                        counts[name] += 1
                    except Exception as exc:
                        failures.append(f"{name}: {exc}")
                        halt.set()
                    time.sleep(0.05)

            threads = [threading.Thread(target=load, args=(name,), daemon=True) for name in modes]
            for thread in threads:
                thread.start()
            baseline = {name: process_sample(proc) for name, proc in zip(modes, procs)}
            samples = []
            lint_cycles = 0
            next_lint = time.monotonic()
            next_sample = next_lint
            while time.monotonic() < end and not halt.is_set():
                now = time.monotonic()
                if now >= next_lint:
                    valid = command([str(binary), "lint", "-json", "-config", str(fixture)], 0, env=env)
                    if json.loads(valid.stdout).get("errors"):
                        raise RuntimeError("valid lint fixture returned errors")
                    bad_result = command([str(binary), "lint", "-json", "-config", str(bad)], 0, env=env)
                    if not any(w.get("field") == "[admin].token" for w in json.loads(bad_result.stdout).get("warnings", [])):
                        raise RuntimeError("literal admin token warning missing")
                    command([str(binary), "lint", "-strict", "-quiet", "-config", str(bad)], 2, env=env)
                    lint_cycles += 1
                    next_lint = now + 5
                if now >= next_sample:
                    samples.append({name: process_sample(proc) for name, proc in zip(modes, procs)})
                    next_sample = now + 30
                    if len(samples) % 10 == 0:
                        print(f"zero-config progress: {counts}, lint_cycles={lint_cycles}", flush=True)
                if any(proc.poll() is not None for proc in procs):
                    raise RuntimeError("zero-config server exited under load")
                time.sleep(0.05)
            halt.set()
            for thread in threads:
                thread.join(timeout=6)
            if failures or min(counts.values()) < seconds * 2 or lint_cycles < max(1, seconds // 10):
                raise RuntimeError(f"insufficient or failed load: requests={counts}, lint={lint_cycles}, errors={failures[:5]}")
            final = {name: process_sample(proc) for name, proc in zip(modes, procs)}
            for name in modes:
                if final[name]["rss_kib"] - baseline[name]["rss_kib"] > 128 * 1024:
                    raise RuntimeError(f"{name} RSS grew over 128 MiB")
                if final[name]["fds"] - baseline[name]["fds"] > 64:
                    raise RuntimeError(f"{name} descriptors grew over 64")
            summary.update(requests=counts, lint_cycles=lint_cycles, baseline=baseline, final=final, samples=samples)
    finally:
        for proc in procs:
            stop(proc)
        for log in logs:
            log.close()
        backend.shutdown()
        backend.server_close()


def importer(binary, seconds, outdir, summary):
    with tempfile.TemporaryDirectory(prefix="jul-import-soak-") as tmp:
        root = Path(tmp)
        clean = root / "clean.conf"
        clean.write_text(CLEAN_NGINX)
        blocking = root / "blocking.conf"
        blocking.write_text(BLOCKING_NGINX)
        hashes = {}
        cycles = 0
        start = time.monotonic()
        end = start + seconds
        while time.monotonic() < end:
            for kind, source, expected, status in (
                ("clean", clean, 0, None),
                ("blocking", blocking, 3, "manual_action_required"),
            ):
                candidate = root / f"{kind}.toml"
                report = root / f"{kind}.json"
                command([str(binary), "import", "nginx", "-o", str(candidate), "--report", str(report), str(source)], expected)
                assessment = json.loads(report.read_text())
                if status and assessment.get("status") != status:
                    raise RuntimeError(f"{kind} assessment status: {assessment.get('status')}")
                if assessment.get("validation", {}).get("status") != "valid":
                    raise RuntimeError(f"{kind} candidate failed validation")
                command([str(binary), "lint", "-config", str(candidate)], 0)
                digest = hashlib.sha256(candidate.read_bytes()).hexdigest()
                if kind in hashes and hashes[kind] != digest:
                    raise RuntimeError(f"{kind} candidate changed across identical inputs")
                hashes[kind] = digest
            cycles += 1
            if cycles % 300 == 0:
                print(f"importer progress: {cycles} clean and blocking conversions each", flush=True)
            time.sleep(max(0, start + cycles - time.monotonic()))
        if cycles < max(1, seconds // 3):
            raise RuntimeError(f"only {cycles} importer cycles in {seconds}s")
        summary.update(cycles=cycles, candidates_sha256=hashes,
                       child_peak_rss_kib=resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("zero-config", "importer"))
    parser.add_argument("--jul", type=Path, required=True)
    parser.add_argument("--seconds", type=int, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if args.seconds < 1 or not args.jul.is_file():
        parser.error("--seconds must be positive and --jul must name a binary")
    args.out.mkdir(parents=True, exist_ok=True)
    start = time.monotonic()
    summary = {"mode": args.mode, "requested_seconds": args.seconds,
               "sha": os.getenv("JUL_SOAK_SHA", "local")}
    try:
        manifest = pre_run_manifest(args)
        summary.update(binary_sha256=manifest["binary_sha256"],
                       platform=manifest["environment"]["platform"],
                       python=manifest["environment"]["python"],
                       started_utc=manifest["started_utc"],
                       manifest_sha256=sha256((args.out / "manifest.json").read_bytes()))
        if args.mode == "zero-config":
            zero_config(args.jul.resolve(), args.seconds, args.out, summary)
        else:
            importer(args.jul.resolve(), args.seconds, args.out, summary)
        summary["elapsed_seconds"] = round(time.monotonic() - start, 3)
        if summary["elapsed_seconds"] < args.seconds:
            raise RuntimeError("wall-clock duration below requested minimum")
        summary["result"] = "pass"
    except Exception as exc:
        summary["elapsed_seconds"] = round(time.monotonic() - start, 3)
        summary["result"] = "fail"
        summary["error"] = str(exc)
        raise
    finally:
        summary["finished_utc"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        write_json(args.out / "summary.json", summary)
        print(json.dumps({k: v for k, v in summary.items() if k != "samples"}, sort_keys=True), flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"feature soak failed: {error}", file=sys.stderr)
        sys.exit(1)

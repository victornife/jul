#!/usr/bin/env python3
"""Exercise a pre-#643 projection retained through binary upgrade, with identical raw TOML.

Use explicitly built binaries: --old-bin /path/to/pre643 --new-bin /path/to/fixed.
Only synthetic credentials, temporary loopback listeners and disposable files.
"""
import argparse
import json
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


def free_address():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{listener.getsockname()[1]}"


def request(address, path, payload=None):
    req = urllib.request.Request(f"http://{address}{path}",
        data=json.dumps(payload).encode() if payload is not None else None,
        headers={"Authorization": "Bearer upgrade-probe-synthetic-token", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=2) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def start(binary, path, address, log):
    process = subprocess.Popen([str(binary), "-config", str(path)], stdout=log, stderr=log)
    for _ in range(250):
        if process.poll() is not None:
            raise RuntimeError(f"binary exited {process.returncode}")
        try:
            code, routes = request(address, "/api/routes")
            if code == 200:
                return process, routes
        except (OSError, ValueError):
            pass
        time.sleep(.02)
    process.terminate()
    process.wait(timeout=10)
    raise RuntimeError("binary did not start")


def stop(process):
    process.send_signal(signal.SIGTERM)
    process.wait(timeout=10)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--old-bin", required=True, type=Path)
    parser.add_argument("--new-bin", required=True, type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="jul-selector-upgrade-") as directory:
        root = Path(directory)
        admin, traffic = free_address(), free_address()
        path = root / "jul.toml"
        raw = f'''[global]
config_authority = "managed"
[admin]
enabled = true
listen = "{admin}"
token = "upgrade-probe-synthetic-token"
history_dir = "{root / 'history'}"
'''
        for status in (200, 201):
            raw += f'''[[servers]]
listen = "{traffic}"
server_names = ["a.test"]
[[servers.locations]]
match = {{type = "prefix", path = "/"}}
return = {status}
'''
        path.write_text(raw)
        with (root / "probe.log").open("wb") as log:
            process, old = start(args.old_bin.resolve(), path, admin, log)
            try:
                old_loc = old[1]["locations"][0]
                assert old_loc["match_ordinal"] == 0 and "selector_version" not in old_loc
                # Read the original base revision atomically from this response.
                req = urllib.request.Request(f"http://{admin}/api/routes", headers={"Authorization": "Bearer upgrade-probe-synthetic-token"})
                with urllib.request.urlopen(req) as response:
                    original_version = response.headers["X-Jul-Config-Version"]
            finally:
                stop(process)
            process, fresh = start(args.new_bin.resolve(), path, admin, log)
            try:
                assert path.read_text() == raw, "upgrade changed raw configuration"
                assert fresh[1]["base_version"] == original_version
                op = {"op": "location_set_action", "listen": old[1]["listen"], "server_names": old[1]["server_names"], "match_type": old_loc["type"], "path": old_loc["match"], "match_ordinal": old_loc["match_ordinal"], "action": {"kind": "return", "status": 202}}
                code, _ = request(admin, "/api/config/patch/preview", {"base_version": original_version, "ops": [op]})
                assert code == 400, f"retained legacy selector accepted: {code}"
                loc = fresh[1]["locations"][0]
                op.update(match_ordinal=loc["match_ordinal"], selector_version=loc["selector_version"])
                code, result = request(admin, "/api/config/patch/preview", {"base_version": original_version, "ops": [op]})
                assert code == 200 and result["valid"], "fresh selector refused"
                assert path.read_text() == raw, "preview persisted"
                print(json.dumps({"raw_revision_unchanged": True, "old_ordinal": old_loc["match_ordinal"], "corrected_ordinal": loc["match_ordinal"], "legacy_refused": True, "fresh_preview_valid": True}))
            finally:
                stop(process)


if __name__ == "__main__":
    main()

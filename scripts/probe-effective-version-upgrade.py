#!/usr/bin/env python3
"""Real old/fixed binaries: restart, rotation, raw CAS, staging and downgrade.

Only disposable loopback state and synthetic low-entropy references. No key,
secret-derived intermediates or old effective identifiers are printed.
"""
import argparse
import atexit
import hashlib
import json
import os
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

RUNNING = []

def cleanup():
    for p in RUNNING:
        if p.poll() is None:
            p.terminate()
            p.wait(timeout=12)

atexit.register(cleanup)

TOKEN = "version-upgrade-synthetic-token-644"


def free_address():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return f"127.0.0.1:{s.getsockname()[1]}"


def request(address, path, data=None, toml=False):
    body = data.encode() if toml else json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(f"http://{address}{path}", data=body, headers={
        "Authorization": f"Bearer {TOKEN}", "Content-Type": "application/toml" if toml else "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=8) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, json.load(e)


def stop(p):
    p.send_signal(signal.SIGTERM)
    p.wait(timeout=12)
    assert p.returncode == 0, "unclean shutdown"


def start(binary, path, address, env, log):
    p = subprocess.Popen([str(binary), "-config", str(path)], env=env, stdout=log, stderr=log)
    RUNNING.append(p)
    for _ in range(250):
        if p.poll() is not None:
            raise RuntimeError(f"binary exited {p.returncode}")
        try:
            code, status = request(address, "/api/v1/status")
            if code == 200 and status.get("ready"):
                # Authority tracking starts after the admin listener. Wait for
                # its startup reconciliation when a managed baseline exists.
                if status.get("config_state") == "managed_unadopted" and Path(str(path) + ".managed.json").exists():
                    time.sleep(.02)
                    continue
                return p, status
        except (OSError, ValueError):
            pass
        time.sleep(.02)
    p.terminate()
    p.wait(timeout=12)
    raise RuntimeError("startup timeout")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--old-bin", required=True, type=Path)
    parser.add_argument("--new-bin", required=True, type=Path)
    args = parser.parse_args()
    old, new = args.old_bin.resolve(), args.new_bin.resolve()
    with tempfile.TemporaryDirectory(prefix="jul-version-upgrade-") as d:
        root = Path(d)
        path, key, secret = root / "jul.toml", root / "private" / "key", root / "reference"
        admin, traffic = free_address(), free_address()
        env = dict(os.environ, JUL_EFFECTIVE_VERSION_KEY_FILE=str(key), JUL_644_NAME="a")
        raw = f'''[global]
config_authority = "managed"
[admin]
enabled = true
listen = "{admin}"
token = "{TOKEN}"
history_dir = "{root / 'history'}"
[[servers]]
name = "literal-${{env:JUL_644_NAME}}-${{file:{secret}}}-${{env:JUL_644_NAME}}"
listen = "{traffic}"
[[servers.locations]]
match = {{type = "prefix", path = "/"}}
return = 200
'''
        path.write_text(raw)
        secret.write_text("a")
        with (root / "operator.log").open("wb") as log:
            p, before = start(old, path, admin, env, log)
            code, adopted = request(admin, "/api/config/adopt-external", {"mode":"hot", "confirm":True, "observed_digest":hashlib.sha256(raw.encode()).hexdigest()})
            assert code == 200 and adopted["ok"], "baseline adoption failed"
            _, before = request(admin, "/api/v1/status")
            stop(p)
            # First launch cannot silently create a lost/missing established key.
            missing = subprocess.run([str(new), "-config", str(path)], env=env, stdout=log, stderr=log, timeout=12)
            assert missing.returncode != 0 and not key.exists()
            subprocess.run([str(new), "version-key", "init"], env=env, stdout=log, stderr=log, check=True)
            p, initial = start(new, path, admin, env, log)
            assert initial["persisted_version"] == before["persisted_version"]
            stable = initial["serving_version"]
            assert stable.startswith("ev1:") and stable != before["serving_version"]
            # A managed hot apply includes both desired/serving and raw fences.
            changed = raw.replace("return = 200", "return = 201")
            code, hot = request(admin, "/api/config/apply?base_version=" + initial["persisted_version"], changed, True)
            assert code == 200 and hot["ok"] and hot["desired_version"] == hot["serving_version"]
            code, _ = request(admin, "/api/config/apply?base_version=" + initial["persisted_version"], raw, True)
            assert code == 409, "stale CAS bypassed"
            # Configuration rollback returns to the earlier effective ID.
            code, rolled = request(admin, "/api/config/apply?base_version=" + hot["persisted_version"], raw, True)
            assert code == 200 and rolled["serving_version"] == stable
            stop(p)
            p, restarted = start(new, path, admin, env, log)
            assert restarted["serving_version"] == stable
            stop(p)
            # Rotation leaves the unresolved raw revision unchanged across startup.
            env["JUL_644_NAME"] = "b"
            p, rotated = start(new, path, admin, env, log)
            assert rotated["persisted_version"] == initial["persisted_version"] and rotated["serving_version"] != stable
            # File rotation while running uses the real managed resolution/reload.
            secret.write_text("b")
            code, rotated_file = request(admin, "/api/config/apply?base_version=" + rotated["persisted_version"], raw, True)
            assert code == 200 and rotated_file["serving_version"] != rotated["serving_version"]
            # No-change under unchanged effective inputs retains its ID.
            code, same = request(admin, "/api/config/apply?base_version=" + rotated["persisted_version"], raw, True)
            assert code == 200 and same["serving_version"] == rotated_file["serving_version"]
            # Stage a startup-only cache change; it must never look published.
            staged_raw = raw.replace("[admin]", "[cache]\nenabled = true\n[admin]")
            code, staged = request(admin, "/api/config/apply?mode=stage_restart&base_version=" + rotated["persisted_version"], staged_raw, True)
            assert code == 200 and staged["ok"] and staged["desired_version"] != staged["serving_version"]
            assert staged["serving_version"] == same["serving_version"]
            marker = Path(str(path) + ".pending-restart.json")
            assert marker.exists()
            stop(p)
            # Converge with the fixed binary before the supported binary downgrade.
            p, converged = start(new, path, admin, env, log)
            assert converged["serving_version"] == staged["desired_version"] and not marker.exists()
            stop(p)
            p, downgraded = start(old, path, admin, env, log)
            assert downgraded["persisted_version"] == converged["persisted_version"]
            assert key.exists() and (root / "history").exists()
            stop(p)
            p, upgraded_again = start(new, path, admin, env, log)
            assert upgraded_again["serving_version"] == converged["serving_version"]
            stop(p)
        print(json.dumps({"raw_cas_preserved": True, "missing_key_refused": True, "restart_stable": True,
            "env_and_file_rotation_detected": True, "hot_apply_and_rollback": True, "no_change_stable": True,
            "staged_not_serving": True, "restart_converged": True, "converged_binary_downgrade": True,
            "history_and_key_retained": True, "reupgrade_stable": True}))


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Verify downloaded release packages and native upgrade/rollback behavior."""

import argparse
import hashlib
import http.server
import json
import os
import platform
import re
import secrets
import shutil
import signal
import socket
import subprocess
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

REPOSITORY = "victornife/jul"
WORKFLOW = "victornife/jul/.github/workflows/release.yml"
PROVENANCE = "https://slsa.dev/provenance/v1"
SBOM = "https://spdx.dev/Document/v2.3"
HISTORICAL_RELEASES = {
    "v2.1.0": "0951090a6fb7703769a15b4749be9664a9842c98",
    "v2.2.0": "b12e051139f652b400cff37d9f063f5637a7d550",
}
# Digests of the full v2.2.0 archives from the committed journey-7 evidence.
# Historical deployment journeys also execute packaged scripts and service units.
V220_FULL_DIGESTS = {
    ("darwin", "amd64"): "e5d43a8fb91c34e40d969753b3aeb963323e95a105c4c4ecaf5d6ce7a52e40dd",
    ("darwin", "arm64"): "52841b1c4771483f9d3bd0cde6f947d4daa44d34600406400b53c38c79f75aa8",
    ("linux", "amd64"): "288ffa323d92d8abb586d364954eaaaa627026304471e33a3ec1174339287862",
    ("linux", "arm64"): "07d468c3e92232b0174cbdc66cd2abbb507719e332b3f60a5dfc60b22f172998",
    ("windows", "amd64"): "9a841f53e6e6b9f730daa206add1df4832a840cd3d17bd6c157291b286a4664d",
    ("windows", "arm64"): "fccc7c050d83898f3cc361c67937a0fda429b0a2c53a98efb0d6d6650232febe",
}


def command(*arguments, **options):
    result = subprocess.run(list(map(str, arguments)), text=True,
                            capture_output=True, timeout=180, **options)
    if result.returncode:
        raise RuntimeError(f"{Path(str(arguments[0])).name} failed ({result.returncode}): {result.stderr[-2000:]}")
    return result.stdout


def create_windows_key_root(program_data):
    root = Path(program_data) / f"jul-release-package-verification-{secrets.token_hex(16)}"
    provisioner = Path(__file__).resolve().parent.parent / "deploy/windows/new-secure-data-dir.ps1"
    try:
        command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive",
                "-ExecutionPolicy", "Bypass", "-File", provisioner, "-Path", root)
    except Exception:
        if root.exists():
            shutil.rmtree(root)
        raise
    return root


def check_identity(metadata, tag, commit, operating_system, architecture):
    expected = {"version": tag, "commit": commit, "os": operating_system,
                "arch": architecture, "dirty": False}
    for key, value in expected.items():
        if metadata.get(key) != value:
            raise RuntimeError(f"package identity mismatch for {key}")


def native_architecture(machine, environment, windows):
    native = environment.get("PROCESSOR_ARCHITEW6432", environment.get("PROCESSOR_ARCHITECTURE", machine)) if windows else machine
    return {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[native.lower()]


def unpack(archive, directory):
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as package:
            for member in package.infolist():
                target = (directory / member.filename).resolve()
                if not target.is_relative_to(directory.resolve()):
                    raise RuntimeError("unsafe zip member")
                if (member.external_attr >> 16) & 0o170000 == 0o120000:
                    raise RuntimeError("symlink in release archive")
            package.extractall(directory)
    else:
        with tarfile.open(archive) as package:
            for member in package.getmembers():
                if not (member.isfile() or member.isdir()):
                    raise RuntimeError("non-file member in release archive")
                if not (directory / member.name).resolve().is_relative_to(directory.resolve()):
                    raise RuntimeError("unsafe tar member")
            package.extractall(directory, filter="data")


def attest(subject, tag, commit, predicate, *, json_output=False):
    args = ["gh", "attestation", "verify", subject, "--repo", REPOSITORY,
            "--signer-workflow", WORKFLOW, "--source-ref", f"refs/tags/{tag}",
            "--source-digest", commit, "--deny-self-hosted-runners",
            "--predicate-type", predicate]
    if json_output:
        args += ["--format", "json"]
    return command(*args)


def verify_archive(folder, tag, commit, operating_system, architecture, profile, *, legacy=None):
    if legacy is not None and (legacy != tag or HISTORICAL_RELEASES.get(legacy) != commit):
        raise RuntimeError("legacy verification is restricted to pinned historical releases")
    name = f"jul_{tag}_{operating_system}_{architecture}_{profile}"
    archive = folder / (name + (".zip" if operating_system == "windows" else ".tar.gz"))
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if legacy == "v2.2.0" and (profile != "full" or
                               V220_FULL_DIGESTS.get((operating_system, architecture)) != digest):
        raise RuntimeError(f"historical archive digest mismatch: {archive.name}")
    if not legacy:
        verified = json.loads(attest(archive, tag, commit, PROVENANCE, json_output=True))
        if not any(
            result.get("verificationResult", {}).get("statement", {}).get("predicateType") == PROVENANCE
            and any(subject.get("name") == archive.name and subject.get("digest", {}).get("sha256") == digest
                    for subject in result.get("verificationResult", {}).get("statement", {}).get("subject", []))
            for result in verified
        ):
            raise RuntimeError(f"verified archive subject mismatch: {archive.name}")
    individual = Path(str(archive) + ".sha256").read_text().split()[0]
    aggregate = {line.split()[1].lstrip("*"): line.split()[0]
                 for line in (folder / "SHA256SUMS").read_text().splitlines() if line.strip()}
    if digest != individual or aggregate.get(archive.name) != digest:
        raise RuntimeError(f"archive checksum mismatch: {archive.name}")
    extraction = Path(tempfile.mkdtemp(prefix="release-extract-", dir=folder))
    unpack(archive, extraction)
    package = extraction / name
    binary = package / ("jul.exe" if operating_system == "windows" else "jul")
    for required in (binary.name, "README.md", "SECURITY.md", "server.toml", "jul.spdx.json", "LICENSE"):
        if not (package / required).is_file():
            raise RuntimeError(f"missing package file: {required}")
    sbom = json.loads((package / "jul.spdx.json").read_text())
    if not sbom.get("spdxVersion", "").startswith("SPDX-"):
        raise RuntimeError("invalid bundled SPDX SBOM")
    attest(binary, tag, commit, PROVENANCE)
    attest(binary, tag, commit, SBOM)
    metadata = json.loads(command(binary, "version", "-json"))
    check_identity(metadata, tag, commit, operating_system, architecture)
    capabilities = json.loads(command(binary, "capabilities", "-json"))["features"]
    if capabilities.get("waf") != (profile == "full") or capabilities.get("console") != (profile == "full"):
        raise RuntimeError("package build-profile capabilities mismatch")
    return binary, {"archive": archive.name, "sha256": digest, "metadata": metadata,
                    "checksums": "passed",
                    "archive_provenance": "unavailable (pinned historical release)" if legacy else "passed",
                    "archive_subject": None if legacy else archive.name,
                    "historical_digest": "passed (committed v2.2.0 evidence)" if legacy == "v2.2.0" else "not checked",
                    "binary_provenance": "passed", "binary_subject": binary.name,
                    "sbom": "passed"}


def free_port():
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        return reservation.getsockname()[1]


def request(url, token=None):
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=2) as response:
        return response.status, response.read()


def exercise(binary, configuration, traffic, admin, token, full, version_key_file=None):
    command(binary, "check", "-config", configuration)
    # A source harness can also validate an older released package. Only newer
    # binaries advertise this command; any supported init failure is fatal.
    completion = subprocess.run([str(binary), "completion", "bash"], capture_output=True, text=True, check=True).stdout
    key_file = version_key_file or (Path(configuration).parent / "private" / "version.key").resolve()
    env = dict(os.environ, JUL_EFFECTIVE_VERSION_KEY_FILE=str(key_file))
    if "version-key" in completion:
        subprocess.run([str(binary), "version-key", "init"], env=env, check=True, stdout=subprocess.DEVNULL)
    with tempfile.TemporaryFile(mode="w+t") as output:
        options = {"creationflags": subprocess.CREATE_NEW_PROCESS_GROUP} if os.name == "nt" else {}
        process = subprocess.Popen([str(binary), "-config", str(configuration)], stdout=output,
                                   stderr=subprocess.STDOUT, env=env, **options)
        try:
            deadline = time.monotonic() + 30
            while True:
                if process.poll() is not None:
                    raise RuntimeError(f"package startup exited: {process.returncode}")
                try:
                    if request(f"http://127.0.0.1:{admin}/readyz")[0] == 200:
                        break
                except (urllib.error.URLError, TimeoutError):
                    pass
                if time.monotonic() >= deadline:
                    raise RuntimeError("package did not become ready")
                time.sleep(0.1)
            if request(f"http://127.0.0.1:{traffic}/") != (200, b"release-static\n"):
                raise RuntimeError("static response mismatch")
            if request(f"http://127.0.0.1:{traffic}/proxy/") != (200, b"release-proxy\n"):
                raise RuntimeError("proxy response mismatch")
            if request(f"http://127.0.0.1:{admin}/metrics", token)[0] != 200:
                raise RuntimeError("authenticated metrics unavailable")
            if full:
                status, body = request(f"http://127.0.0.1:{admin}/", token)
                if status != 200 or b"<html" not in body.lower():
                    raise RuntimeError("embedded Console unavailable")
                try:
                    request(f"http://127.0.0.1:{traffic}/blocked")
                except urllib.error.HTTPError as error:
                    if error.code != 403:
                        raise
                else:
                    raise RuntimeError("packaged WAF did not block the synthetic rule")
        finally:
            if process.poll() is None:
                process.send_signal(signal.CTRL_BREAK_EVENT if os.name == "nt" else signal.SIGINT)
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def self_test():
    assert native_architecture("AMD64", {"PROCESSOR_ARCHITECTURE": "AMD64", "PROCESSOR_ARCHITEW6432": "ARM64"}, True) == "arm64"
    assert native_architecture("aarch64", {}, False) == "arm64"
    correct = {"version": "v2.2.0", "commit": "source", "os": "linux", "arch": "arm64", "dirty": False}
    check_identity(correct, "v2.2.0", "source", "linux", "arm64")
    try:
        check_identity({**correct, "dirty": True}, "v2.2.0", "source", "linux", "arm64")
    except RuntimeError:
        pass
    else:
        raise RuntimeError("dirty binary was accepted")
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        archive = root / "unsafe.zip"
        with zipfile.ZipFile(archive, "w") as package:
            package.writestr("../outside", "unsafe")
        try:
            unpack(archive, root / "extract")
        except RuntimeError:
            pass
        else:
            raise RuntimeError("unsafe archive was accepted")
    print("release verifier identity/path checks passed")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--tag")
    parser.add_argument("--commit")
    parser.add_argument("--os", choices=("linux", "darwin", "windows"))
    parser.add_argument("--arch", choices=("amd64", "arm64"))
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--report", type=Path)
    arguments = parser.parse_args()
    if arguments.self_test:
        self_test()
        return
    if not all((arguments.tag, arguments.commit, arguments.os, arguments.arch, arguments.directory, arguments.report)):
        parser.error("tag, commit, os, arch, directory and report are required")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?", arguments.tag):
        parser.error("tag must be a stable or rc.N version tag")
    if not re.fullmatch(r"[0-9a-f]{40}", arguments.commit):
        parser.error("commit must be a full lowercase commit SHA")
    host_os = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}[platform.system()]
    host_arch = native_architecture(platform.machine(), os.environ, os.name == "nt")
    if (host_os, host_arch) != (arguments.os, arguments.arch):
        raise RuntimeError("native verification requested on a different host architecture")
    arguments.directory.mkdir(parents=True, exist_ok=True)
    previous_tag = "v2.1.0"
    previous_commit = HISTORICAL_RELEASES[previous_tag]
    if arguments.tag == previous_tag:
        raise RuntimeError("target release must differ from the historical rollback fixture")
    downloaded = {}
    for tag, commit in ((arguments.tag, arguments.commit), (previous_tag, previous_commit)):
        folder = arguments.directory / tag
        folder.mkdir(exist_ok=True)
        command("gh", "release", "download", tag, "--repo", "victornife/jul", "--pattern",
                f"jul_{tag}_{arguments.os}_{arguments.arch}_*", "--pattern", "SHA256SUMS",
                "--dir", folder, "--clobber")
        downloaded[tag] = {profile: verify_archive(folder, tag, commit, arguments.os, arguments.arch, profile,
                                                    legacy=previous_tag if tag == previous_tag else None)
                           for profile in ("lean", "full")}
    class Backend(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"release-proxy\n")
        def log_message(self, *unused):
            pass
    backend = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Backend)
    worker = threading.Thread(target=backend.serve_forever, daemon=True)
    worker.start()
    report = {"tag": arguments.tag, "source": arguments.commit, "os": arguments.os,
              "arch": arguments.arch, "native": True, "gh_version": command("gh", "--version").splitlines()[0],
              "verifier_source": command("git", "rev-parse", "HEAD").strip(),
              "profiles": {}}
    key_root = None
    try:
        if os.name == "nt":
            program_data = os.environ.get("ProgramData")
            if not program_data:
                raise RuntimeError("Windows ProgramData directory is unavailable for protected key verification")
            key_root = create_windows_key_root(program_data)
        version_key_file = key_root / "private" / "version.key" if key_root else None
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            site = root / "public"
            site.mkdir()
            (site / "index.html").write_bytes(b"release-static\n")
            token = secrets.token_hex(24)
            for profile in ("lean", "full"):
                traffic, admin = free_port(), free_port()
                configuration = root / f"{profile}.toml"
                configuration.write_text(f'''[global]
shutdown_timeout = "2s"
[admin]
enabled = true
listen = "127.0.0.1:{admin}"
token = "{token}"
console = {str(profile == "full").lower()}
history_dir = {json.dumps(str(root / 'history'))}
[[servers]]
listen = "127.0.0.1:{traffic}"
[[servers.locations]]
match = {{type = "prefix", path = "/proxy/"}}
proxy_pass = "http://127.0.0.1:{backend.server_port}"
[[servers.locations]]
match = {{type = "prefix", path = "/"}}
root = {json.dumps(str(site))}
''')
                if profile == "full":
                    with configuration.open("a") as output:
                        output.write('''
[waf]
enabled = true
mode = "block"
inline_rules = """
SecRule REQUEST_URI "@streq /blocked" "id:9910201,phase:1,deny,status:403,log,msg:'release package check'"
"""
''')
                old, historical_verification = downloaded[previous_tag][profile]
                binary, verification = downloaded[arguments.tag][profile]
                verification["rollback_fixture"] = historical_verification
                for stage, executable in (("previous", old), ("upgrade", binary), ("rollback", old)):
                    exercise(executable, configuration, traffic, admin, token, profile == "full",
                             version_key_file=version_key_file)
                    verification[stage] = "passed"
                report["profiles"][profile] = verification
    finally:
        backend.shutdown()
        backend.server_close()
        worker.join()
        if key_root is not None:
            shutil.rmtree(key_root)
    arguments.report.parent.mkdir(parents=True, exist_ok=True)
    arguments.report.write_text(json.dumps(report, indent=2) + "\n")
    print(f"download/startup/upgrade/rollback verified: {arguments.os}/{arguments.arch}, lean/full")


if __name__ == "__main__":
    main()
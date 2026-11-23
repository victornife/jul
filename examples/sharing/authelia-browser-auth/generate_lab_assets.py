#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Generate disposable TLS, Authelia, and Jul credentials for the #627 lab."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import json
import secrets
import sys
import tempfile


SCRIPT_DIR = Path(__file__).resolve().parent
HOSTS = (
    "dash.private.example.test",
    "auth.private.example.test",
    "public.example.test",
)


def run_command(*args: str | Path, timeout: int = 120) -> str:
    import subprocess

    try:
        result = subprocess.run(
            [str(arg) for arg in args],
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise RuntimeError(f"{args[0]} could not run: {exc}") from exc
    if result.returncode != 0:
        detail = result.stderr.strip().splitlines()
        suffix = f": {detail[-1]}" if detail else ""
        raise RuntimeError(f"{args[0]} exited {result.returncode}{suffix}")
    return result.stdout.strip()


def random_argon2id_hash(password: str) -> str:
    from argon2.low_level import Type, hash_secret

    return hash_secret(
        password.encode("utf-8"),
        secrets.token_bytes(16),
        time_cost=3,
        memory_cost=65_536,
        parallelism=4,
        hash_len=32,
        type=Type.ID,
    ).decode("ascii")


def write_private(path: Path, content: str) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=f".{path.name}.",
        dir=path.parent,
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as file:
            file.write(content)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def generate_tls(lab_dir: Path) -> None:
    tls_dir = lab_dir / "tls"
    tls_dir.mkdir(mode=0o700, parents=True)
    root_key = tls_dir / ".root-ca.key"
    root_cert = tls_dir / "root-ca.pem"
    server_key = tls_dir / "server.key"
    request = tls_dir / ".server.csr"
    extension = tls_dir / ".server.ext"

    run_command(
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
        "-days", "2", "-sha256", "-subj", "/CN=Jul #627 disposable test CA",
        "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign",
        "-keyout", root_key, "-out", root_cert,
    )
    run_command(
        "openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes",
        "-sha256", "-subj", f"/CN={HOSTS[0]}",
        "-keyout", server_key, "-out", request,
    )
    extension.write_text(
        "basicConstraints=critical,CA:FALSE\n"
        "keyUsage=critical,digitalSignature,keyEncipherment\n"
        "extendedKeyUsage=serverAuth\n"
        f"subjectAltName={','.join('DNS:' + host for host in HOSTS)}\n",
        encoding="ascii",
    )
    run_command(
        "openssl", "x509", "-req", "-in", request,
        "-CA", root_cert, "-CAkey", root_key, "-CAcreateserial",
        "-days", "2", "-sha256", "-extfile", extension,
        "-out", tls_dir / "server.crt",
    )
    server_key.chmod(0o600)
    root_key.unlink()
    request.unlink()
    extension.unlink()
    (tls_dir / "root-ca.srl").unlink(missing_ok=True)


def generate_runtime_files(output_dir: Path, lab_dir: Path) -> None:
    authelia_dir = lab_dir / "authelia"
    data_dir = authelia_dir / "data"
    data_dir.mkdir(mode=0o700, parents=True)

    intended_password = secrets.token_urlsafe(24)
    excluded_password = secrets.token_urlsafe(24)
    users = {
        "intended": {
            "disabled": False,
            "displayname": "Synthetic dashboard member",
            "password": random_argon2id_hash(intended_password),
            "email": "intended@example.test",
            "groups": ["dashboard-users"],
        },
        "excluded": {
            "disabled": False,
            "displayname": "Synthetic excluded member",
            "password": random_argon2id_hash(excluded_password),
            "email": "excluded@example.test",
            "groups": ["visitors"],
        },
    }
    users_yaml = ["users:"]
    for username, record in users.items():
        users_yaml.extend(
            [
                f"  {username}:",
                f"    disabled: {str(record['disabled']).lower()}",
                f"    displayname: '{record['displayname']}'",
                f"    password: '{record['password']}'",
                f"    email: {record['email']}",
                "    groups:",
                *(f"      - {group}" for group in record["groups"]),
            ]
        )
    write_private(authelia_dir / "users_database.yml", "\n".join(users_yaml) + "\n")

    template = (SCRIPT_DIR / "authelia" / "configuration.yml.template").read_text(
        encoding="utf-8"
    )
    for placeholder in (
        "__RESET_PASSWORD_SECRET__",
        "__SESSION_SECRET__",
        "__STORAGE_ENCRYPTION_KEY__",
    ):
        template = template.replace(placeholder, secrets.token_urlsafe(48))
    if "__" in template:
        raise RuntimeError("Authelia configuration template has an unresolved secret placeholder")
    write_private(authelia_dir / "configuration.yml", template)

    write_private(
        lab_dir / "credentials.json",
        json.dumps(
            {
                "intended": {
                    "username": "intended",
                    "password": intended_password,
                },
                "excluded": {
                    "username": "excluded",
                    "password": excluded_password,
                },
            },
            indent=2,
        )
        + "\n",
    )
    write_private(
        lab_dir / "jul.env",
        f"JUL627_ADMIN_TOKEN={secrets.token_urlsafe(48)}\n",
    )
    uid = os.getuid() if hasattr(os, "getuid") else 1000
    gid = os.getgid() if hasattr(os, "getgid") else 1000
    write_private(
        output_dir / ".env",
        f"JUL627_UID={uid}\nJUL627_GID={gid}\n",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--out",
        type=Path,
        default=Path.cwd(),
        help="Fixture directory (default: current directory)",
    )
    parser.add_argument(
        "--config-check",
        action="store_true",
        help="Generate only ephemeral TLS files for Jul config validation",
    )
    args = parser.parse_args()
    output_dir = args.out.resolve()
    lab_dir = output_dir / ".lab"
    if lab_dir.exists() and any(lab_dir.iterdir()):
        print(f"refusing to overwrite existing lab files in {lab_dir}", file=sys.stderr)
        return 2
    lab_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    try:
        generate_tls(lab_dir)
        if not args.config_check:
            generate_runtime_files(output_dir, lab_dir)
    except RuntimeError as exc:
        print(f"asset generation failed: {exc}", file=sys.stderr)
        return 1
    print(f"Created disposable lab assets under {lab_dir}")
    if not args.config_check:
        print("Test credentials are in .lab/credentials.json; the local admin token is in .lab/jul.env.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

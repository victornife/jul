#!/usr/bin/env python3
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl

"""Generate ephemeral synthetic lab assets for Fixture F1 (#581, SHARE-02A).

Produces:
  - tls/root-ca.pem, tls/intermediate-ca.pem, tls/lab.crt (leaf + intermediate),
    tls/lab-leaf-only.crt (incomplete chain), tls/lab.key
  - tls/wrong-ca.pem, tls/wrong-host.crt, tls/wrong-host.key
  - tls/client-ca.pem, tls/client-intended.crt/.key, tls/client-revoked.crt/.key,
    tls/client-unauthorised-san.crt/.key, tls/client-wrong-ca.crt/.key
  - tls/clients-clean.crl, tls/clients-revoked.crl, tls/clients.crl
  - dashboard.htpasswd (two synthetic users), dashboard-revoked.htpasswd (one user removed)
  - portal/index.html (~4 KiB), portal/assets/bundle.bin (256 KiB)
  - state/config-history/

Never commits private keys or real credentials. Refuses to overwrite existing
assets unless --force is supplied.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys


# Precomputed bcrypt ($2a$10$) hashes for deterministic synthetic lab passwords
# when python3-bcrypt is not installed (e.g. minimal config-check environments).
# These are disposable lab-only test strings, never production secrets.
SYNTHETIC_INTENDED_USER = "family-intended"
SYNTHETIC_INTENDED_PASS = "f1-synthetic-intended-pass-01"
SYNTHETIC_INTENDED_BCRYPT = "$2b$10$V.X/qQKBldqcEqu70PbGEO2iAJCCXjzF.E45O1BVbTXgix0.pCY1e"

SYNTHETIC_SECOND_USER = "family-guest"
SYNTHETIC_SECOND_PASS = "f1-synthetic-guest-pass-02"
SYNTHETIC_SECOND_BCRYPT = "$2b$10$oLDUSsOIbeBQ7At7b0djl.5IBClDTEAQ7NKXZnu17P8qHwy.oj8.."


def run_cmd(*args: str | Path) -> str:
    proc = subprocess.run([str(a) for a in args], text=True, capture_output=True, timeout=60)
    if proc.returncode != 0:
        raise RuntimeError(f"command {args[0]} failed ({proc.returncode}): {proc.stderr[-1200:]}")
    return proc.stdout.strip()


def hash_bcrypt(password: str, fallback_hash: str, deterministic: bool) -> str:
    if deterministic:
        return fallback_hash
    try:
        import bcrypt  # type: ignore
        return bcrypt.hashpw(password.encode("utf-8"), bcrypt.gensalt(rounds=10)).decode("ascii")
    except Exception:
        return fallback_hash


def generate_server_pki(tls_dir: Path, lan_ip: str, extra_ips: list[str]) -> dict[str, str]:
    root_key = tls_dir / "root-ca.key"
    root_pem = tls_dir / "root-ca.pem"
    inter_key = tls_dir / "intermediate-ca.key"
    inter_csr = tls_dir / "intermediate-ca.csr"
    inter_pem = tls_dir / "intermediate-ca.pem"
    leaf_key = tls_dir / "lab.key"
    leaf_csr = tls_dir / "lab.csr"
    leaf_only = tls_dir / "lab-leaf-only.crt"
    leaf_chain = tls_dir / "lab.crt"

    run_cmd(
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-sha256",
        "-subj", "/CN=Jul F1 Lab Root CA",
        "-addext", "basicConstraints=critical,CA:TRUE,pathlen:1",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign",
        "-keyout", root_key, "-out", root_pem,
    )
    run_cmd(
        "openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-sha256",
        "-subj", "/CN=Jul F1 Lab Intermediate CA",
        "-keyout", inter_key, "-out", inter_csr,
    )
    inter_ext = tls_dir / "intermediate.ext"
    inter_ext.write_text(
        "basicConstraints=critical,CA:TRUE,pathlen:0\n"
        "keyUsage=critical,keyCertSign,cRLSign\n"
        "subjectKeyIdentifier=hash\n"
        "authorityKeyIdentifier=keyid,issuer\n",
        encoding="utf-8",
    )
    run_cmd(
        "openssl", "x509", "-req", "-in", inter_csr,
        "-CA", root_pem, "-CAkey", root_key, "-CAcreateserial",
        "-days", "2", "-sha256", "-extfile", inter_ext, "-out", inter_pem,
    )

    ip_sans = ["IP:127.0.0.1", f"IP:{lan_ip}"]
    for ip in extra_ips:
        entry = f"IP:{ip}"
        if entry not in ip_sans:
            ip_sans.append(entry)
    sans = ",".join([
        "DNS:portal.lab.test",
        "DNS:portal-alias.lab.test",
        "DNS:dash.lab.test",
        *ip_sans,
    ])
    run_cmd(
        "openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-sha256",
        "-subj", "/CN=portal.lab.test",
        "-keyout", leaf_key, "-out", leaf_csr,
    )
    leaf_ext = tls_dir / "leaf.ext"
    leaf_ext.write_text(
        "basicConstraints=critical,CA:FALSE\n"
        "keyUsage=critical,digitalSignature,keyEncipherment\n"
        "extendedKeyUsage=serverAuth\n"
        f"subjectAltName={sans}\n",
        encoding="utf-8",
    )
    run_cmd(
        "openssl", "x509", "-req", "-in", leaf_csr,
        "-CA", inter_pem, "-CAkey", inter_key, "-CAcreateserial",
        "-days", "2", "-sha256", "-extfile", leaf_ext, "-out", leaf_only,
    )
    leaf_chain.write_bytes(leaf_only.read_bytes() + inter_pem.read_bytes())

    wrong_ca_key = tls_dir / "wrong-ca.key"
    wrong_ca_pem = tls_dir / "wrong-ca.pem"
    run_cmd(
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-sha256",
        "-subj", "/CN=Jul F1 Untrusted Root CA",
        "-addext", "basicConstraints=critical,CA:TRUE",
        "-keyout", wrong_ca_key, "-out", wrong_ca_pem,
    )

    return {
        "root_ca_sha256": hashlib.sha256(root_pem.read_bytes()).hexdigest(),
        "intermediate_ca_sha256": hashlib.sha256(inter_pem.read_bytes()).hexdigest(),
        "leaf_chain_sha256": hashlib.sha256(leaf_chain.read_bytes()).hexdigest(),
        "wrong_ca_sha256": hashlib.sha256(wrong_ca_pem.read_bytes()).hexdigest(),
    }


def generate_client_mtls_pki(tls_dir: Path) -> dict[str, str]:
    ca_dir = tls_dir / "client-ca-db"
    ca_dir.mkdir(parents=True, exist_ok=True)
    (ca_dir / "newcerts").mkdir(exist_ok=True)
    (ca_dir / "index.txt").write_text("", encoding="utf-8")
    (ca_dir / "serial").write_text("1000\n", encoding="utf-8")
    (ca_dir / "crlnumber").write_text("1000\n", encoding="utf-8")

    client_ca_key = tls_dir / "client-ca.key"
    client_ca_pem = tls_dir / "client-ca.pem"
    run_cmd(
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-sha256",
        "-subj", "/CN=Jul F1 Client Auth CA",
        "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign",
        "-keyout", client_ca_key, "-out", client_ca_pem,
    )

    ca_cnf = ca_dir / "openssl-ca.cnf"
    ca_cnf.write_text(
        f"""[ ca ]
default_ca = CA_default

[ CA_default ]
dir = {ca_dir.as_posix()}
database = $dir/index.txt
new_certs_dir = $dir/newcerts
certificate = {client_ca_pem.as_posix()}
private_key = {client_ca_key.as_posix()}
serial = $dir/serial
crlnumber = $dir/crlnumber
default_md = sha256
default_days = 2
default_crl_days = 2
policy = policy_anything
unique_subject = no
copy_extensions = copy

[ policy_anything ]
commonName = supplied
""",
        encoding="utf-8",
    )

    def issue_client_cert(prefix: str, cn: str, san_dns: str) -> Path:
        key_p = tls_dir / f"{prefix}.key"
        csr_p = tls_dir / f"{prefix}.csr"
        crt_p = tls_dir / f"{prefix}.crt"
        ext_p = tls_dir / f"{prefix}.ext"
        run_cmd(
            "openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-sha256",
            "-subj", f"/CN={cn}",
            "-keyout", key_p, "-out", csr_p,
        )
        ext_p.write_text(
            "basicConstraints=critical,CA:FALSE\n"
            "keyUsage=critical,digitalSignature\n"
            "extendedKeyUsage=clientAuth\n"
            f"subjectAltName=DNS:{san_dns}\n",
            encoding="utf-8",
        )
        run_cmd(
            "openssl", "ca", "-batch", "-config", ca_cnf,
            "-extfile", ext_p, "-in", csr_p, "-out", crt_p,
        )
        return crt_p

    intended_crt = issue_client_cert("client-intended", "intended-client", "intended-client.lab.test")
    revoked_crt = issue_client_cert("client-revoked", "revoked-client", "revoked-client.lab.test")
    issue_client_cert("client-unauthorised-san", "unauthorised-san-client", "other-client.lab.test")

    # Wrong-CA client certificate signed by wrong-ca.pem
    wrong_client_key = tls_dir / "client-wrong-ca.key"
    wrong_client_csr = tls_dir / "client-wrong-ca.csr"
    wrong_client_crt = tls_dir / "client-wrong-ca.crt"
    wrong_client_ext = tls_dir / "client-wrong-ca.ext"
    wrong_client_ext.write_text(
        "basicConstraints=critical,CA:FALSE\n"
        "keyUsage=critical,digitalSignature\n"
        "extendedKeyUsage=clientAuth\n"
        "subjectAltName=DNS:intended-client.lab.test\n",
        encoding="utf-8",
    )
    run_cmd(
        "openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-sha256",
        "-subj", "/CN=wrong-ca-client",
        "-keyout", wrong_client_key, "-out", wrong_client_csr,
    )
    run_cmd(
        "openssl", "x509", "-req", "-in", wrong_client_csr,
        "-CA", tls_dir / "wrong-ca.pem", "-CAkey", tls_dir / "wrong-ca.key", "-CAcreateserial",
        "-days", "2", "-sha256", "-extfile", wrong_client_ext, "-out", wrong_client_crt,
    )

    clean_crl = tls_dir / "clients-clean.crl"
    revoked_crl = tls_dir / "clients-revoked.crl"
    active_crl = tls_dir / "clients.crl"

    run_cmd("openssl", "ca", "-config", ca_cnf, "-gencrl", "-out", clean_crl)
    run_cmd("openssl", "ca", "-config", ca_cnf, "-revoke", revoked_crt)
    run_cmd("openssl", "ca", "-config", ca_cnf, "-gencrl", "-out", revoked_crl)
    shutil.copyfile(clean_crl, active_crl)

    shutil.rmtree(ca_dir, ignore_errors=True)
    for csr in tls_dir.glob("*.csr"):
        csr.unlink(missing_ok=True)
    for ext in tls_dir.glob("*.ext"):
        ext.unlink(missing_ok=True)
    for srl in tls_dir.glob("*.srl"):
        srl.unlink(missing_ok=True)
    for key_file in tls_dir.glob("*.key"):
        key_file.chmod(0o600)

    return {
        "client_ca_sha256": hashlib.sha256(client_ca_pem.read_bytes()).hexdigest(),
        "client_intended_sha256": hashlib.sha256(intended_crt.read_bytes()).hexdigest(),
        "client_revoked_sha256": hashlib.sha256(revoked_crt.read_bytes()).hexdigest(),
        "clean_crl_sha256": hashlib.sha256(clean_crl.read_bytes()).hexdigest(),
        "revoked_crl_sha256": hashlib.sha256(revoked_crl.read_bytes()).hexdigest(),
    }


def generate_portal_and_htpasswd(out_dir: Path, deterministic: bool) -> dict[str, str]:
    portal_dir = out_dir / "portal"
    assets_dir = portal_dir / "assets"
    assets_dir.mkdir(parents=True, exist_ok=True)

    header = (
        "<!doctype html>\n"
        "<html lang=\"en\">\n"
        "<head><meta charset=\"utf-8\"><title>Jul F1 Public Portal</title></head>\n"
        "<body>\n"
        "<h1>Jul F1 Public Portal — JUL-F1-PUBLIC-PORTAL-MARKER-v1</h1>\n"
        "<p>Anonymous public portal entry (route_id=portal-home, mode=public).</p>\n"
    )
    footer = "\n</body>\n</html>\n"
    pad_len = max(0, 4096 - len(header) - len(footer))
    filler = ("<!-- portal-static-padding-line-0123456789abcdef -->\n" * ((pad_len // 53) + 2))[:pad_len]
    index_bytes = (header + filler + footer).encode("utf-8")
    index_path = portal_dir / "index.html"
    index_path.write_bytes(index_bytes)

    # 256 KiB deterministic static asset for W1/W2/W3/W4 10% asset traffic class
    chunk = (b"JUL-F1-PORTAL-ASSET-256KIB-BLOCK-" * 32)[:1024]
    bundle_bytes = chunk * 256
    bundle_path = assets_dir / "bundle.bin"
    bundle_path.write_bytes(bundle_bytes)

    if deterministic:
        p1, p2 = "soakpass", "admin"
        h1, h2 = SYNTHETIC_INTENDED_BCRYPT, SYNTHETIC_SECOND_BCRYPT
    else:
        p1 = "f1-intended-" + secrets.token_urlsafe(12)
        p2 = "f1-second-" + secrets.token_urlsafe(12)
        h1 = hash_bcrypt(p1, SYNTHETIC_INTENDED_BCRYPT, False)
        h2 = hash_bcrypt(p2, SYNTHETIC_SECOND_BCRYPT, False)
        if h1 == SYNTHETIC_INTENDED_BCRYPT:
            p1 = "soakpass"
        if h2 == SYNTHETIC_SECOND_BCRYPT:
            p2 = "admin"

    htpasswd_full = out_dir / "dashboard.htpasswd"
    htpasswd_revoked = out_dir / "dashboard-revoked.htpasswd"
    htpasswd_full.write_text(
        f"{SYNTHETIC_INTENDED_USER}:{h1}\n{SYNTHETIC_SECOND_USER}:{h2}\n",
        encoding="utf-8",
    )
    htpasswd_revoked.write_text(
        f"{SYNTHETIC_INTENDED_USER}:{h1}\n",
        encoding="utf-8",
    )
    htpasswd_full.chmod(0o600)
    htpasswd_revoked.chmod(0o600)

    creds_meta = out_dir / ".lab-credentials.json"
    creds_meta.write_text(
        json.dumps(
            {
                "note": "SYNTHETIC-DISPOSABLE-LAB-ONLY — never committed or exported",
                "intended_user": SYNTHETIC_INTENDED_USER,
                "intended_password": p1,
                "second_user": SYNTHETIC_SECOND_USER,
                "second_password": p2,
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    creds_meta.chmod(0o600)

    return {
        "portal_index_bytes": str(len(index_bytes)),
        "portal_index_sha256": hashlib.sha256(index_bytes).hexdigest(),
        "portal_bundle_bytes": str(len(bundle_bytes)),
        "portal_bundle_sha256": hashlib.sha256(bundle_bytes).hexdigest(),
        "htpasswd_sha256": hashlib.sha256(htpasswd_full.read_bytes()).hexdigest(),
        "htpasswd_revoked_sha256": hashlib.sha256(htpasswd_revoked.read_bytes()).hexdigest(),
    }


def generate_all(out_dir: Path, lan_ip: str = "192.0.2.10", extra_ips: list[str] | None = None,
                 force: bool = False, deterministic: bool = False) -> dict[str, str]:
    out_dir = out_dir.resolve()
    tls_dir = out_dir / "tls"
    portal_dir = out_dir / "portal"
    state_dir = out_dir / "state" / "config-history"

    if not force and (tls_dir.exists() or (out_dir / "dashboard.htpasswd").exists()):
        raise RuntimeError(
            f"refusing to overwrite existing lab assets in {out_dir}; pass --force or use a fresh directory"
        )

    tls_dir.mkdir(parents=True, exist_ok=True)
    tls_dir.chmod(0o700)
    state_dir.mkdir(parents=True, exist_ok=True)
    state_dir.chmod(0o700)

    summary: dict[str, str] = {}
    summary.update(generate_server_pki(tls_dir, lan_ip, extra_ips or []))
    summary.update(generate_client_mtls_pki(tls_dir))
    summary.update(generate_portal_and_htpasswd(out_dir, deterministic))
    return summary


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=Path("."), help="Target directory for generated lab assets")
    parser.add_argument("--lan-ip", default="192.0.2.10", help="Controlled-LAN server IP for certificate SANs")
    parser.add_argument("--extra-ip", action="append", default=[], help="Additional IP SANs for leaf cert")
    parser.add_argument("--force", action="store_true", help="Overwrite existing generated lab assets in --out")
    parser.add_argument("--deterministic", action="store_true", help="Use deterministic synthetic test password hashes")
    args = parser.parse_args()
    summary = generate_all(
        args.out,
        lan_ip=args.lan_ip,
        extra_ips=args.extra_ip,
        force=args.force,
        deterministic=args.deterministic,
    )
    print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())

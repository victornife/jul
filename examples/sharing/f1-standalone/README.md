# Fixture F1 — Standalone Restricted & Public Sharing (`#581`, `SHARE-02A`)

This directory contains the reproducible configurations and zero-dependency
asset/backend helpers for **Fixture F1**
([`docs/specs/application-sharing.md` §10](../../../docs/specs/application-sharing.md#f1--standalone-restricted-and-public-581),
[ADR 0026](../../../docs/adr/0026-application-sharing-contract.md)): one
Jul.IA instance serving an anonymously readable public static portal
(`portal.lab.test`, `route_id = "portal-home"`) and a separately restricted
HTTP dashboard (`dash.lab.test`, `route_id = "dashboard"`, HTTP Basic + WAF
Core Rule Set) behind a first-declared `default-deny` virtual host, without a
Jul-hosted or external provider control plane.

For the complete executable operator runbook, topology/trust diagram,
managed vs `file_owned` authority workflows, complete withdrawal (`#642`),
mTLS variants, bounded `W0`–`W4` resource measurements, and `T1`–`T6` dry-run
record, see:

- **Operator runbook:** [`docs/sharing/f1-standalone-runbook.md`](../../../docs/sharing/f1-standalone-runbook.md)
- **Configuration & evidence manifest:** [`docs/sharing/f1-configuration-manifest.json`](../../../docs/sharing/f1-configuration-manifest.json)
- **Qualifying acceptance & resource report:** [`docs/sharing/f1-standalone-evidence.md`](../../../docs/sharing/f1-standalone-evidence.md)

## Configuration progression and variants

| File | Config ID | Authority | Listener(s) | Purpose |
| --- | --- | --- | --- | --- |
| `01-loopback-preview.toml` | `CFG-F1-01` | `managed` | `127.0.0.1:443` | Step 1: Loopback-only preview before LAN exposure |
| `02-loopback-dashboard-restricted.toml` | `CFG-F1-02` | `managed` | `127.0.0.1:443` | Step 2: Install Basic check + `sharing.mode = "restricted"` on loopback (`narrowed`) |
| `03-loopback-portal-public.toml` | `CFG-F1-03` | `managed` | `127.0.0.1:443` | Step 3: Declare `portal-home` `sharing.mode = "public"` (`metadata_only`) |
| `jul.toml` | `CFG-F1-04` | `managed` | `192.0.2.10:443` | Step 4: Canonical controlled-LAN active state (`default-deny` + public `portal` + restricted `dashboard`, `uncertain` bind move) |
| `05-lan-withdrawn-gate.toml` | `CFG-F1-05` | `managed` | `192.0.2.10:443` | Step 5a: Complete withdrawal via `withdrawn = true` stage-0 gate (`#642`, `2s` stream/WebSocket retirement) |
| `06-lan-withdrawn-legacy-deny.toml` | `CFG-F1-06` | `managed` | `192.0.2.10:443` | Step 5b: Legacy mechanism via `auth.deny = ["0.0.0.0/0", "::/0"]` preserving `auth.basic` |
| `07-lan-dashboard-public-transition.toml` | `CFG-F1-07` | `managed` | `192.0.2.10:443` | Deliberate `dashboard` → `public` widening plan/acknowledgement test & recovery |
| `08-lan-mtls-shared-listener.toml` | `CFG-F1-08` | `managed` | `192.0.2.10:443` | mTLS variant on shared listener (`client_auth.mode = "request"` + `require_client_cert = true` on `dashboard`) |
| `09-lan-mtls-dedicated-listener.toml` | `CFG-F1-09` | `managed` | `192.0.2.10:443`, `192.0.2.10:8443` | mTLS variant on dedicated listener (`:443` portal without cert prompt; `:8443` `client_auth.mode = "require"` for `dashboard`) |
| `10-file-owned-lan.toml` | `CFG-F1-10` | `file_owned` | `192.0.2.10:443` | Read-only `file_owned` authority equivalent of `jul.toml` (`deploy/systemd/jul-readonly.service`) |
| `11-file-owned-withdrawn-gate.toml` | `CFG-F1-11` | `file_owned` | `192.0.2.10:443` | Read-only `file_owned` complete withdrawal via `withdrawn = true` + `SIGHUP` |
| `12-file-owned-mtls-shared-listener.toml` | `CFG-F1-12` | `file_owned` | `192.0.2.10:443` | Read-only `file_owned` mTLS variant on shared listener (`client_auth.mode = "request"` + `require_client_cert = true` on `dashboard`) |
| `13-file-owned-mtls-dedicated-listener.toml` | `CFG-F1-13` | `file_owned` | `192.0.2.10:443`, `192.0.2.10:8443` | Read-only `file_owned` mTLS variant on dedicated `:8443` listener (`client_auth.mode = "require"`) |
| `14-lan-managed-no-opaque-noop.toml` | `CFG-F1-14` | `managed` | `192.0.2.10:443` | Opaque-input-free (`auth.allow`) managed candidate for deterministic `no_change` audience-delta verification (`§9.5`) |

## Quick local validation (no Go or Docker required for a downloaded binary)

```bash
# 1. Stage a clean working copy and generate ephemeral lab PKI, CRLs, htpasswd, and portal files:
WORKDIR="$(mktemp -d /tmp/jul-f1-quickstart.XXXXXX)"
cp -r examples/sharing/f1-standalone/* "$WORKDIR/"
python3 "$WORKDIR/generate_lab_assets.py" --out "$WORKDIR"

# 2. Validate and lint with a full-profile jul binary:
export JUL_ADMIN_TOKEN="$(openssl rand -hex 32)"
export JUL_F1_AUDITOR_TOKEN="$(openssl rand -hex 32)"
export JUL_F1_VIEWER_TOKEN="$(openssl rand -hex 32)"
export JUL_F1_OPERATOR_TOKEN="$(openssl rand -hex 32)"
(cd "$WORKDIR" && jul check -config jul.toml && jul lint -config jul.toml)

# 3. Clean up the temporary working directory:
rm -rf "$WORKDIR"
```

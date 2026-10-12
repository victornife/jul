# Deployment journey execution

This runbook accompanies [#516](https://github.com/victornife/jul/issues/516)
and the [dated execution ledger](audit/2026-10-12-deployment-journeys.md).
Run the seven journeys in their selected order. Keep disposable platform
checks separate from the production-like requirements. The published target
is v2.2.0 source `b12e051139f652b400cff37d9f063f5637a7d550`.

The maintainer selected **GitHub CI** for execution. The dedicated
[sequential workflow](../.github/workflows/deployment-journeys-516.yml) uses
native disposable runners with controlled production-like conditions. Its
client has a separate network namespace but shares the runner kernel; its
private three-certificate chain has an explicitly trusted lab root. Windows
fixtures predate service installation and exercise real effective permissions.
No private host, public-CA issuance, enterprise ACL inventory or Internet/cloud
firewall deployment is implied. Site-specific deployment checks below are
operator procedures outside this bounded CI certification.

## Evidence for every journey

Record harness commit/clean state, package source/tag/profile and checksum,
native OS/architecture/runtime, service identity, relevant effective
permissions, client/network topology, trust configuration, named checks and
verdicts. Retain sanitized logs and their checksums. Never capture bearer
tokens, private keys, configuration secrets or authenticated raw readback in
public evidence. Preserve failed results and link corrections separately.

## 1. Linux systemd

The CI job uses an actual systemd host and an isolated client namespace linked
by a veth route. A scoped iptables chain applies only to the test interface.
The runner creates a root CA, intermediate and leaf for a lab hostname, serves
the full chain, and trusts only the root on the external namespace client.
Wrong CA, wrong hostname and a missing intermediate must fail verification;
firewall denial must reject a previously working TLS path and recover when
restored. Both privileged and read-only checks retain the shipped hardening.

For a later site-specific check, identify the authorized host/client, TLS
hostname and trust locations, and approved network paths before installation.

### Published package and unit preflight

On a dedicated empty systemd host with passwordless sudo, Python 3.13+ and
GitHub CLI, download the native full archive, its individual checksum and the
aggregate checksums. For amd64:

```sh
gh release download v2.2.0 --repo victornife/jul \
  --pattern '*linux_amd64_full.tar.gz*' \
  --pattern SHA256SUMS --dir systemd-package
python3 scripts/systemd-deployment-e2e.py \
  --package-directory systemd-package --tag v2.2.0 \
  --source b12e051139f652b400cff37d9f063f5637a7d550 \
  --report systemd-package.json
```

For arm64, change the download pattern to `linux_arm64`; the verifier checks
the actual native architecture. The script validates archive checksums,
binary identity, full profile, bundled SPDX and signed provenance/SBOM through
the existing release verifier. Do not substitute a locally rebuilt binary.

The script refuses pre-existing Jul service paths/users, temporarily installs
both shipped units and a pinned probe guest, and removes only its test
resources. It verifies editable recovery, read-only refusal, actual interpreter
mode and guest execution without disabling `MemoryDenyWriteExecute`.
Its default temporary TLS and single-host probes remain **disposable** evidence.
The `--ci-network` option adds the explicitly bounded routed/trust/firewall
lab checks and records them as `controlled-ci-lab`; it does not claim private
production deployment.
The source-build mode is for CI regression checks and is recorded separately.

### Trusted-chain and network execution

After preflight, follow the shipped [systemd deployment instructions](deployment.md#systemd-linux)
with the downloaded full package, approved TLS files and the pinned
`testdata/plugins/v1-current-header-inject.wasm` probe. Declare it as a
middleware plugin, pin its SHA-256, give the bounded interpreter probe
`timeout = "5s"`, and attach it to the tested server. Keep the unprivileged
service identity, directory protections and both units' hardening settings.

The independently connected client must verify the TLS hostname and trusted
chain, expected static/proxy response and `X-Plugin: header-inject`. Use
normal system trust or the approved CA; do not disable certificate verification.
Confirm the allowed HTTPS path and denied admin/network path against the
actual firewall policy. Record the client/host topology and observed results.
An address contacted from the server itself is not an independent-client test.

Read `GET /api/plugins` through the authenticated running service and verify
`engine = interpreter` and the loaded module digest. Record the effective
unit property, not the engine of an unsandboxed `jul doctor` process. Exercise
initial adoption, Apply/history/rollback and restart persistence for the
editable unit, then read-only Apply refusal with no file change. Capture the
actual service logs and verdicts before marking row 1 complete.

## 2. Windows service and pre-existing ACLs

Use an authorized Windows lab and the published native full package. Inventory
existing service/config/state ACLs before installation. Exercise reviewed
secure and deliberately permissive **pre-existing** roots with inherited and
explicit entries, existing config/history files and an ordinary-user probe.
Record service effective access and ordinary-user read/write results before
and after installation, managed Apply/rollback and restart/recovery.

The installer preserves pre-existing/inherited entries; it does not make an
insecure existing root confidential. A permissive fixture should demonstrate
that boundary. `new-secure-data-dir.ps1` must refuse an existing root. Select
and document any operator remediation rather than silently replacing a real
directory's ACLs. Preserve unrelated files and ACL entries. Keep this separate
from the existing fresh-root CI result.

## 3. Docker external HTTPS and persistence

The CI implementation builds from the frozen v2.2.0 Git archive using the
documented full profile and pinned Dockerfile images. A separate routed client
namespace reaches the bridge container through published gateway ports. The
lab config supplies its CA through `SSL_CERT_FILE`, so the unchanged shell-free
exec healthcheck can verify admin TLS using the certificate's loopback SAN.
Reports retain source-archive and image hashes separately: the shipped
Dockerfile embeds version but does not embed a source commit, and this derived
image is not a published/attested GHCR artifact. Recreation checks preserve
the original history ID and access-log prefix; the cache-volume check uses a
marker and does not repeat HTTP cache correctness certification.

Build the documented image/profile from frozen v2.2.0 source; record source,
builder identity, image digest and binary capabilities. There is no published
GHCR image implied by this check. Use persistent config/history/cache/log
mounts and the approved certificate/key mount. Configure listener bindings and
the image health check consistently with the selected admin TLS policy.

From an independent routed client, verify TLS, expected static/proxy/WASM
behavior as configured, intended admin reachability and missing-credential
refusal. Confirm readiness/health, managed Apply/rollback and state persistence
after container recreation, not only restart. Preserve evidence of the actual
network path; loopback port publication alone does not complete this row.

## 4. Windows service AF_UNIX upstream

Under the actual Jul virtual service account, proxy to an HTTP/1.1 AF_UNIX
backend with a reviewed socket location. Check response/status/body/Host,
socket accessibility, missing/unavailable backend behavior, recovery and
appropriate identity permissions. Use the documented named-upstream form in
[Unix HTTP upstreams](unix-http-upstreams.md); TLS-over-Unix, h2c-over-Unix
and direct `http://unix:` spellings remain unsupported. Existing native
protocol tests do not replace this service-deployment evidence.

## 5. Windows registry MIME

With the published package running as the service identity, exercise native
MIME lookup for a controlled registry-mapped ordinary extension and the six
deterministic streaming mappings. Compare direct/precompressed responses and
an explicit scoped MIME table, which bypasses host lookup. Record relevant
registry state and Content-Type/Content-Encoding/Vary results; restore only
the lab registry entries this journey created. Keep it separate from owner
tests for deterministic or explicit mappings.

## 6. macOS archive first run

Reconcile the completed darwin/amd64 and darwin/arm64 native jobs linked in the
ledger, each with lean/full actual archives. These satisfy the original listed
archive first-run requirement. They do not certify every macOS version or a
new managed-service installation. Darwin packages require macOS 13+.

## 7. Cross-platform archive downloads

Reconcile the completed six-host/twelve-profile verification and attached
release report, retaining source/workflow identities, checksums, attestations,
native startup and v2.1.0 upgrade/rollback. Future releases keep the existing
native download gate. Do not rerun or relabel historical checks solely to make
this ledger look new.

## Completion

Update the ledger, relevant guides, #516 and #62 after each actual journey.
Close #516 only after all remaining requirements have source-bound evidence
and any findings are corrected or accurately recorded. Preparatory tests,
manual procedures and unavailable environments are not successful deployment
results. Feature maturity and post-GA soak status remain separate.

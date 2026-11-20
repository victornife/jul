# Jul.IA Operational and Security Alerting Recipe (`#628-A/B` + `#632`)

This optional, version-pinned recipe combines Milestone A operational alerts
with the existing Milestone B security rules: **Prometheus `v3.11.2`** and
**Alertmanager `v0.32.0`**. Jul remains a standalone single-binary gateway. The
monitor evaluates bounded signals on authenticated `/metrics` and delivers
notifications without keeping the Console open. Users with an existing
Prometheus/Alertmanager can reuse the two rule files and adapt the one expected
target selector; do not install a second monitoring stack.

## Rule-to-signal matrix

PromQL metric names, types and labels are governed by
[`docs/metrics-contract.json`](../../../docs/metrics-contract.json). Alert
expressions retain `job` and `instance` so target grouping and inhibition stay
isolated. Values below are operator examples, not Jul SLOs.

| Alert / scenario | Producer, applicability and signal | Severity; trigger / resolve | Operator action and blind spots |
| --- | --- | --- | --- |
| `JulTargetUnavailable` | Prometheus `up` for each declared `jul-admin-metrics` target. | Critical; scrape stays failed for 1m; resolves after successful scrapes. | Check Jul process, `/metrics`, token and loopback/TLS. Distinct from target disappearing from discovery. Dependent operational/security rules ignore a target while its scrape is failing, so stale symptoms do not fan out; other targets remain independent. |
| `JulExpectedTargetMissing` | `absent(up{job="jul-admin-metrics",instance="127.0.0.1:9090"})`, the recipe's one expected target. | Critical; absent for 5m; resolves when its series returns. | Check expected-target config and discovery. Add an equivalent exact-instance rule for each target managed in an existing stack. |
| `JulCertificateExpiryWarning` / `JulCertificateExpiryCritical` | `jul_tls_cert_expiry_seconds{domain}` from the actual observed leaf certificate; only where Jul publishes that signal. | Warning below 30d for 10m; critical below 7d (including expired) for 10m. Warning excludes the critical band. Renewed expiry resolves the relevant alert. | Inspect served leaf and renewal. Static/unsupported certificates and a missing series are unknown, not healthy; a synthetic gauge only tests the rule and delivery path. |
| `JulPoolNoEligibleBackend` | `jul_upstream_backends_eligible{pool}`; applies to emitted serving pools. | Critical at exactly 0 for 1m; resolves above 0. | Inspect backend health, circuit state, capacity and discovery. An absent/retired pool or missing metric does not mean zero. |
| `JulStorageLow` / `JulStorageCritical` | Positive `jul_storage_bytes{category,kind="available|total"}` for configured Jul-owned paths. | Warning at 5–10% for 10m; critical below 5% for 5m. Exclusive bands avoid duplicate warning/critical notices. | Free/expand the category's filesystem. Missing values or nonpositive total are unknown; this is Jul-owned path capacity, not host-wide disk monitoring. |
| `JulStorageWriteFailing` | `jul_storage_write_health{sink,state}` from the disk-cache, file access-log and durable-audit owners. A configured sink starts `unknown`, then reports the latest observed write result. | Critical when `state="failing"` persists 30s; resolves only after an owner-observed successful write. | Inspect that sink and filesystem. Cumulative old failures, low capacity, governor drops and audit maintenance-only failures do not imply this alert. A quiet sink stays unknown. |
| `JulStorageWriteStatusUnknown` | Same producer; configured sinks with no write result yet. Unconfigured sinks have no series. | Warning when `state="unknown"` lasts 15m; resolves after an observed write. | Generate expected sink activity and verify its result. Unknown is deliberately not treated as success. |

| Alert | Producer Metric(s) | Owner Issue | Severity | Window / Threshold |
| --- | --- | --- | --- | --- |
| `JulWAFProtectionWeakened` | `jul_waf_protection_changes_total{transition="weakened"}`, `jul_waf_serving_routes{state}` | `#630` | `warning` | `for: 30s` on weakened transition or `0` block routes while other routes exist |
| `JulSecurityPostureErrorPresent` | `jul_security_posture_findings{area,severity="error"}` | `#633` | `critical` | `for: 1m` when any area reports an `error` finding |
| `JulSecurityPostureWarningSustained` | `jul_security_posture_findings{area,severity="warning"}` | `#633` | `warning` | `for: 5m` when any area reports a sustained `warning` |
| `JulSecurityEvidenceUnavailableOrDegraded` | `jul_security_evidence_state{producer,state=~"stale\|degraded\|unavailable"}` | `#633` | `warning` | `for: 1m` when required evidence is stale, degraded, or unavailable |
| `JulWAFRejectionSymptomElevated` | `jul_waf_transactions_total{mode,outcome}` | `#632` | `warning` | `for: 1m` when block-mode interruptions/rejections exceed `5%` of inspected transactions AND total inspected rate `>= 0.05 req/s` |

### Why `JulWAFRejectionSymptomElevated` alerts on a symptom, not "confirmed false positives"

A spike in `jul_waf_transactions_total{mode="block",outcome="interrupted_request"}`
is an **observed rejection fact**, not proof of a false positive: it can be
caused by an active attack, a recent WAF policy/paranoia change, or an
application regression. Conversely, traffic thresholds cannot promise detection
of a single wrongly blocked request on a low-traffic route. When this alert
fires:

1. Query `GET /api/v1/waf/events` (`jul waf events`) or open the Console
   **Investigate outcomes** drawer.
2. Check whether the rejections share a specific `rule_id` and sanitized target
   (e.g. `ARGS:profile_bio`) on legitimate traffic.
3. If legitimate traffic is affected, create a narrow `#631` exception, verify
   it with `jul waf evaluate` (`#630`), and promote it through preview/diff/apply.

## Transport & Token Setup (Loopback-or-TLS Gate)

Jul's admin server enforces a strict **loopback-or-TLS transport gate** on
`/metrics` alongside `metrics:read` Bearer token authentication:

1. **Shared-loopback arrangement (default in [`docker-compose.yml`](./docker-compose.yml)):**
   - `prometheus`, `alertmanager`, and the local `receiver` run with `network_mode: host`
     and bind strictly to `127.0.0.1:9091`, `127.0.0.1:9093`, and `127.0.0.1:19095`.
   - Prometheus scrapes `http://127.0.0.1:9090/metrics` from `127.0.0.1`, which satisfies
     Jul's loopback transport check without exposing Jul's admin listener on a non-loopback
     interface.
   - Create `examples/alerting/security/secrets/metrics-bearer-token.local` with a
     dedicated `metrics:read` token (not an admin token). The Prometheus image
     runs as UID/GID 65534; make the file owner-readable by that container user
     and not world-readable (for example `sudo chgrp 65534 ... && chmod 0440 ...`).
     Adjust the group for user-namespace remapping. The `.local` file is ignored
     by Git; never replace the tracked `metrics-bearer-token.example`.
     Create it without putting the token in shell history:
     ```bash
     umask 077
     install -m 0600 /dev/null examples/alerting/security/secrets/metrics-bearer-token.local
     "${EDITOR:-vi}" examples/alerting/security/secrets/metrics-bearer-token.local
     sudo chgrp 65534 examples/alerting/security/secrets/metrics-bearer-token.local
     chmod 0440 examples/alerting/security/secrets/metrics-bearer-token.local
     ```
2. **TLS arrangement (for bridge networks or remote monitoring hosts):**
   - Configure `[admin.tls]` on Jul (`enabled = true`, `cert = "/etc/jul/admin.crt"`, `key = "/etc/jul/admin.key"`), mount the CA certificate
     into `examples/alerting/security/secrets/jul-admin-ca.pem`, and uncomment
     `scheme: "https"` + `tls_config.ca_file` in [`prometheus.yml`](./prometheus.yml).
   - The existing security-rule `runbook_url` values assume the default local HTTP
     admin listener; change them to HTTPS and a reachable operator URL when using
     TLS or delivering notifications off-host.

## Operations: Start, Validate, Test, Stop, and Remove

By default, the only persistent operator-created secret is the protected
metrics token file. Optional TLS CA and external webhook secret files are also
operator-managed. Prometheus TSDB data is in the disposable container filesystem
(24h retention); receiver notifications are container logs and are not durable.
No volume points at Jul data or application storage.

The local notification payload contains the bounded target identity, alert name,
severity, and operational labels such as configured certificate domain, pool,
storage category, or sink; it does not include credentials, request data, or
filesystem paths. The local Compose endpoints are explicit loopback URLs, so
payload links do not disclose the host's internal hostname. Protect receiver
logs as operational data. External delivery is disabled until an operator
deliberately enables the external receiver.

- **Validate rules, rule behavior, Alertmanager configuration and Compose:**
  ```bash
  docker run --rm -v "$PWD/examples/alerting/security:/rules:ro" --workdir /rules \
    --entrypoint promtool prom/prometheus:v3.11.2 check rules security-alerts.rules.yml operational-alerts.rules.yml
  docker run --rm -v "$PWD/examples/alerting/security:/rules:ro" --workdir /rules \
    --entrypoint promtool prom/prometheus:v3.11.2 test rules security-alerts.test.yml
  docker run --rm -v "$PWD/examples/alerting/security:/etc/alertmanager:ro" \
    --entrypoint amtool prom/alertmanager:v0.32.0 check-config /etc/alertmanager/alertmanager.yml
  docker compose -f examples/alerting/security/docker-compose.yml config --quiet
  ```
- **Exercise real Jul → Prometheus → Alertmanager → local receiver:**
  ```bash
  GOMAXPROCS=2 go test -p 2 -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" \
    -v -count=1 -run '^TestSecurityAlerting(PromtoolRules|EndToEndLocalReceiver)$' ./internal/app/...
  ```
- **Start and inspect the optional stack:**
  ```bash
  docker compose -f examples/alerting/security/docker-compose.yml up -d
  docker compose -f examples/alerting/security/docker-compose.yml ps
  docker compose -f examples/alerting/security/docker-compose.yml logs -f receiver
  ```
- **Stop/remove only this stack:**
  ```bash
  docker compose -f examples/alerting/security/docker-compose.yml down
  ```
  Remove only the local scrape credential when it is no longer needed:
  `rm -- examples/alerting/security/secrets/metrics-bearer-token.local`.

For a release-package walkthrough on a staging host, start the recipe and watch
the receiver:
```bash
docker compose -f examples/alerting/security/docker-compose.yml up -d
docker compose -f examples/alerting/security/docker-compose.yml logs -f receiver
```
Using the staging host's normal service manager, stop the Jul release process
and confirm a `JulTargetUnavailable` firing payload after its 1m hold; restart
Jul and confirm the same alert's resolved payload after successful scrapes.
Then remove only the monitoring stack with the `down` command above. The tagged
Go test stops and rebinds Jul's authenticated metrics listener, so Prometheus
observes a refused scrape and then recovery; this exercises target loss/recovery,
not the host service manager. Use the staging walkthrough to verify the actual
release process stop/restart. The test also exercises a real Jul active-health
pool losing/regaining eligibility, actual durable-audit rotation
failure/recovery, receiver unavailability, and target-scoped inhibition.

The certificate-expiry gauge in that integration and the rule-fixture storage
threshold data are synthetic. They verify PromQL/evaluation and notification
delivery only, not certificate parsing or low-storage producer behavior. The
expiry metric contract and its producer are covered separately by Jul's tests.

For an external generic webhook, create
`examples/alerting/security/secrets/external_webhook_url` without placing the
URL in shell history:
```bash
umask 077
install -m 0600 /dev/null examples/alerting/security/secrets/external_webhook_url
"${EDITOR:-vi}" examples/alerting/security/secrets/external_webhook_url
sudo chgrp 65534 examples/alerting/security/secrets/external_webhook_url
chmod 0440 examples/alerting/security/secrets/external_webhook_url
```
Adjust the group for user-namespace remapping. Mount the secrets directory at
`/etc/alertmanager/secrets/`, switch the route to the commented
`external-secops-webhook` receiver, and uncomment its `url_file`. No external
destination is enabled by default.

**Footprint observation:** on Linux/arm64 with Docker `29.6.2`, after 115 seconds
at the 15s cadence while the configured Jul target was unavailable and one
outage notification was delivered, `docker stats` reported Prometheus 24.24 MiB,
Alertmanager 12.3 MiB, and the receiver 12.12 MiB (about 49 MiB combined); the
highest sampled CPU was 0.04%. Jul itself was not included. This is a short
single-host observation, not a production capacity guarantee; measure on the
intended host and workload. With a reused monitoring stack, the additional Jul
footprint is just the authenticated scrape and rule evaluation; no Jul-side
polling daemon is added.

**Recipe owner:** Victor Niharra (@victornife), with transfer to the Jul
maintainers when ownership changes. Revalidate on Jul metric-contract changes,
rule changes, Prometheus/Alertmanager image upgrades, scrape-boundary changes,
and release walkthroughs.

## Operational Limitations & Blind Spots

- **Same-host failure domain:** Running Prometheus/Alertmanager on the same host
  as Jul shares host power, kernel, and network connectivity. The outage alert
  cannot notify if the host itself is unavailable; use an independent monitor
  for host-outage coverage.
- **Receiver loss & backpressure:** Check
  `docker compose -f examples/alerting/security/docker-compose.yml ps` and
  `docker compose -f examples/alerting/security/docker-compose.yml logs --tail=100 alertmanager receiver`;
  restore the receiver and verify Alertmanager's local status at
  `http://127.0.0.1:9093`.
  Delivery is not guaranteed while the receiver is unavailable, and this
  disposable setup does not persist notification logs or Alertmanager state.
  Receiver failure never blocks Jul request handling or weakens WAF enforcement.
- **Revalidation trigger:** Re-run the listed native checks and both
  `TestSecurityAlertingPromtoolRules` and
  `TestSecurityAlertingEndToEndLocalReceiver` whenever a consumed metric
  contract, either rule file, Alertmanager config or pinned tool changes.

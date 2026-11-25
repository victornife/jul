# Jul.IA Operational and Security Alerting Recipe (`#628-A/B` + `#632`)

This optional, version-pinned recipe combines Milestone A operational alerts
with the existing Milestone B security rules: **Prometheus `v3.11.2`** and
**Alertmanager `v0.32.0`**. Jul remains a standalone single-binary gateway. The
monitor evaluates bounded signals on authenticated `/metrics` and delivers
notifications without keeping the Console open. Users with an existing
Prometheus/Alertmanager can reuse the two rule files and adapt the one expected
target selector; do not install a second monitoring stack.

An optional outside-in application probe uses Blackbox Exporter `v0.27.0`
and its own Prometheus process/profile. It checks one operator-configured HTTPS
application URL through DNS, verified TLS, the Jul route, and the upstream.
Normal monitoring remains usable without the profile; the probe does not query
Jul's admin or metrics endpoints.

## Rule-to-signal matrix

PromQL metric names, types and labels are governed by
[`docs/metrics-contract.json`](../../../docs/metrics-contract.json). Alert
expressions retain `job` and `instance` so target grouping and inhibition stay
isolated. Values below are operator examples, not Jul SLOs.
Dependent operational and security alerts require `up == 1` for the
same `job` and `instance`. A `resolved` notification means the rule condition
cleared, not necessarily that the underlying fault recovered: a failed scrape,
a disappeared target, or missing producer data can also clear a symptom.

| Alert / scenario | Producer, applicability and signal | Severity; trigger / resolve | Operator action and blind spots |
| --- | --- | --- | --- |
| `JulTargetUnavailable` | Prometheus `up` for each declared `jul-admin-metrics` target. | Critical; scrape stays failed for 1m; resolves after successful scrapes. | Check Jul process, `/metrics`, token and loopback/TLS. Distinct from target disappearing from discovery. Dependent operational/security rules ignore a target while its scrape is failing, so stale symptoms do not fan out; other targets remain independent. |
| `JulExpectedTargetMissing` | `absent(up{job="jul-admin-metrics",instance="127.0.0.1:9090"})`, the recipe's one expected target. | Critical; absent for 5m; resolves when its series returns. | Check expected-target config and discovery. Add an equivalent exact-instance rule for each target managed in an existing stack. |
| `JulApplicationProbeFailed` | Optional Blackbox result for the explicitly configured `jul-app` HTTPS URL; requires exporter scrape `up=1`. | Critical; status/body/TLS probe fails for 1m; clears only after a new successful probe. | Check DNS, verified TLS, Jul route and upstream. It is one synthetic URL, not general application correctness. |
| `JulApplicationProbeExporterUnavailable` / `JulApplicationProbeMissing` | `up` for `jul-application-probe`; scrape failure differs from no expected target series. | Critical; `up=0` for 1m or expected `up` absent for 5m. | Check exporter process/listener/scrape and expected target config. Neither means the application recovered. |
| `JulApplicationProbeCertificateExpiring` / `JulApplicationProbeCertificateExpiryCritical` | Blackbox Exporter's `probe_ssl_earliest_cert_expiry`, only while that same target probe succeeds. | Warning below 30d; critical below 7d for 10m. | Earliest observed chain `NotAfter` may be an intermediate; missing data is unknown. Confirm the served chain and renewal path. |
| `JulCertificateExpiryWarning` / `JulCertificateExpiryCritical` | `jul_tls_cert_expiry_seconds{domain}` from the actual observed leaf certificate; only where Jul publishes that signal. | Warning below 30d for 10m; critical below 7d (including expired) for 10m. Warning excludes the critical band. Renewed expiry resolves the relevant alert. | Inspect served leaf and renewal. Static/unsupported certificates and a missing series are unknown, not healthy; a synthetic gauge only tests the rule and delivery path. |
| `JulPoolNoEligibleBackend` | `jul_upstream_backends_eligible{pool}`; applies to emitted serving pools. | Critical at exactly 0 for 1m; resolves above 0. | Inspect backend health, circuit state, capacity and discovery. An absent/retired pool or missing metric does not mean zero. |
| `JulStorageLow` / `JulStorageCritical` | Positive `jul_storage_bytes{category,kind="available|total"}` for configured Jul-owned paths. | Warning at 5–10% for 10m; critical below 5% for 5m. Exclusive bands avoid duplicate warning/critical notices. | Free/expand the category's filesystem. Missing values or nonpositive total are unknown; this is Jul-owned path capacity, not host-wide disk monitoring. |
| `JulStorageWriteFailing` | `jul_storage_write_health{sink,state}` from the disk-cache, file access-log and durable-audit owners. Cache health covers disk persistence, including store, cleanup and recovery failures. | Critical when `state="failing"` persists 30s. The alert clears on any loss of that condition, including an unknown or disabled sink or loss of a successful scrape. | Inspect the sink and filesystem. Confirm recovery with `state="ok"` on a successful scrape: the owner observed a successful access-log or durable audit write, or a cache store with no outstanding persistence failure. Historical failure counts, low capacity, governor drops and audit maintenance-only failures do not imply this alert. |
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

## Optional outside-in application probe

The probe's stable target identity is `jul-app`; it is not the URL. Edit the
single `targets` entry in
[`application-probe-prometheus.yml`](./application-probe-prometheus.yml) to the
exact HTTPS route that should be exercised. Configure that public application
route to return HTTP `200` and a small deterministic body containing
`JUL_OPS_PROBE_OK` (for example a tiny readiness resource served by the
application). Do not use a redirect, login page, authenticated URL, query
credential, or a target whose response body contains secrets. The exporter
rejects redirects, every status other than `200`, and responses without the
marker. TLS chain and hostname verification are explicitly enabled.

For a disposable local proxy exercise, run
[`probe-fixture.py`](./probe-fixture.py), which binds only to loopback and
returns the marker at `/ops-probe`, then route a Jul HTTPS listener's exact
`/ops-probe` location to `http://127.0.0.1:18080`. Use an operator-trusted test
CA for that listener and explicitly mount that CA into the temporary
Blackbox Exporter configuration. The fixture proves only HTTP routing and
marker matching; use a real publicly trusted certificate to validate the
production trust path.

Minimal Jul listener configuration for the fixture:

```toml
[[servers]]
listen = "127.0.0.1:18443"
server_names = ["localhost"]
  [servers.tls]
  cert = "/tmp/jul-app-probe/server.crt"
  key = "/tmp/jul-app-probe/server.key"
  [[servers.locations]]
  match = { type = "exact", path = "/ops-probe" }
  proxy_pass = "http://127.0.0.1:18080"
```

Use a test certificate with `DNS:localhost` in its SAN, configure the local
Blackbox module's `tls_config.ca_file` to the issuing test CA, and keep
`insecure_skip_verify = false`. Initialize a temporary effective-version key
with `jul version-key init --file /tmp/jul-app-probe/effective-version.key`,
then start Jul with the top-level `-config` and
`-effective-version-key-file` flags before `serve`. Run Jul, the fixture, and
the exporter in separate terminals. Query the loopback exporter with:

```bash
curl -fsS --get \
  --data-urlencode 'module=jul_https_application' \
  --data-urlencode 'target=https://localhost:18443/ops-probe' \
  http://127.0.0.1:9115/probe
```

Require `probe_success 1` and `probe_http_status_code 200`. Stop only the
fixture with Ctrl-C and repeat the request; it must report `probe_success 0`.
Restart the fixture and repeat until the result returns to `probe_success 1`.
This verifies the actual Jul TLS listener, exact route, upstream, hostname
verification, and body marker. It does not exercise Prometheus alert timing or
Alertmanager delivery; the checked-in `promtool test rules` fixtures cover the
alert state transitions separately.

The exporter binds to `127.0.0.1:9115`; the optional probe Prometheus UI binds
to `127.0.0.1:9092`. Both use host networking, consistent with the existing
loopback-only monitoring listeners. The default stack and its Prometheus at
`127.0.0.1:9091` are unchanged when `application-probe` is not enabled.
Alertmanager remains the existing private local receiver at `127.0.0.1:9093`.
No probe credentials or target URL are emitted as alert labels.

For a probe from a **separate host**, run the exporter and its probe Prometheus
together on that probe host (or on one private monitoring network). Use the
application's real DNS name and URL there: this observes that host's resolver,
egress/firewall path, trust store, SNI/hostname, Jul listener, route, and
upstream reachability. It does not prove reachability from every client network
or verify a browser's authentication path. Keep the exporter listener on
loopback/private monitoring interfaces; do not publish its unauthenticated
`/probe` endpoint to the public internet. If the receiver is elsewhere, use a
private, authenticated/TLS Alertmanager path. The default same-host recipe
shares Jul's host, power, and network failure domain; it cannot report a total
host outage.

Start the full optional recipe with:

```bash
docker compose -f examples/alerting/security/docker-compose.yml \
  --profile application-probe up -d
docker compose -f examples/alerting/security/docker-compose.yml \
  --profile application-probe ps
curl -fsS 'http://127.0.0.1:9092/api/v1/targets'
curl -fsS 'http://127.0.0.1:9115/probe?module=jul_https_application&target=https%3A%2F%2Fapp.example.test%2Fops-probe'
```

Replace the example URL in both the scrape config and the manual curl with the
same real operator-owned URL. In the probe output, require `probe_success 1`
and inspect `probe_http_status_code 200`; the Prometheus series use only
`instance="jul-app"` and do not expose the configured URL. The target DNS
lookup and TLS handshake happen in the Blackbox Exporter process, not in Jul or
Prometheus.

The rules distinguish an application response failure (`probe_success == 0`
while exporter scrape `up == 1`), an exporter/scrape failure (`up == 0`), a
missing expected target (no `up` series), and a scrape that lacks its expected
probe result. Only a **new successful probe** clears the application-failure
condition; exporter loss or missing data is not interpreted as application
recovery. A missing `probe_ssl_earliest_cert_expiry` series is unknown, not
healthy, and does not alert as a healthy certificate.

Blackbox Exporter's `probe_ssl_earliest_cert_expiry` is the earliest `NotAfter`
observed across certificates in the TLS connection, which can be an
intermediate rather than only Jul's leaf. Its alert is evaluated only while a
probe succeeds; it complements rather than replaces Jul's
`jul_tls_cert_expiry_seconds` signal. The actual probe performs verified TLS;
rule fixtures alone do not validate certificate parsing or trust.

The outside-in probe confirms one synthetic response path, not application
correctness, data freshness, every route, user authorization, or all upstream
dependencies. A fixture endpoint must be deterministic and side-effect-free.
Use a distinct `probe_target` and target URL for each independently operated
application, and add matching exact-instance expected-target rules; do not
replace the stable target identity with a hostname or arbitrary request label.

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
  promtool check rules examples/alerting/security/application-probe.rules.yml
  promtool test rules examples/alerting/security/application-probe.test.yml
  docker run --rm -v "$PWD/examples/alerting/security/blackbox.yml:/etc/blackbox_exporter/blackbox.yml:ro" \
    prom/blackbox-exporter:v0.27.0 --config.file=/etc/blackbox_exporter/blackbox.yml --config.check
  docker compose -f examples/alerting/security/docker-compose.yml config --quiet
  docker compose -f examples/alerting/security/docker-compose.yml \
    --profile application-probe config --quiet
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
  If only the optional probe is being stopped while the default stack remains up,
  use `docker compose -f examples/alerting/security/docker-compose.yml --profile application-probe stop application-probe-prometheus blackbox-exporter`.
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

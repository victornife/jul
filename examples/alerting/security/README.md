# Jul.IA External Security Alerting Recipe (`#628-B` + `#632`)

This directory provides a version-pinned, optional external security-alerting
recipe using **Prometheus `v3.11.2`** and **Alertmanager `v0.32.0`**. Jul.IA
remains a single-binary gateway that exposes bounded, low-cardinality signals on
`/metrics` (`metrics:read`); the external monitor evaluates time-series rules
and delivers notifications without keeping the Console open.

## Producer Metrics & Cardinality Contract

All rules in [`security-alerts.rules.yml`](./security-alerts.rules.yml) consume
bounded metrics from [`docs/metrics-contract.json`](../../../docs/metrics-contract.json):

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
   - Write a `metrics:read` RBAC principal token (or admin token) into
     `examples/alerting/security/secrets/metrics-bearer-token` (`chmod 0644` when running
     rootless/nobody containers).
2. **TLS arrangement (for bridge networks or remote monitoring hosts):**
   - Configure `[admin.tls]` on Jul (`enabled = true`, `cert = "/etc/jul/admin.crt"`, `key = "/etc/jul/admin.key"`), mount the CA certificate
     into `examples/alerting/security/secrets/jul-admin-ca.pem`, and uncomment
     `scheme: "https"` + `tls_config.ca_file` in [`prometheus.yml`](./prometheus.yml).

## Operations: Start, Test, Stop, and Remove

- **Validate rule syntax and time-series unit tests:**
  ```bash
  docker run --rm -v "$PWD/examples/alerting/security:/rules:ro" --workdir /rules \
    --entrypoint promtool prom/prometheus:v3.11.2 test rules security-alerts.test.yml
  ```
- **Run the disposable end-to-end Jul → Monitor → Local Receiver verification:**
  ```bash
  go test -tags "brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf" \
    -v -run "TestSecurityAlerting" ./internal/app/...
  ```
- **Start the optional stack:**
  ```bash
  docker compose -f examples/alerting/security/docker-compose.yml up -d
  ```
- **Stop and remove the stack:**
  ```bash
  docker compose -f examples/alerting/security/docker-compose.yml down -v
  ```

## Operational Limitations & Blind Spots

- **Same-host failure domain:** Running Prometheus/Alertmanager on the same host
  as Jul shares host power, kernel, and network connectivity; use an external
  monitor or secondary heartbeat (`up == 0`) for host-outage detection.
- **Receiver loss & backpressure:** Alertmanager delivery retries are bounded by
  Alertmanager's queue; an unreachable webhook receiver never blocks Jul request
  handling or weakens WAF enforcement.
- **Revalidation trigger:** Re-run `TestSecurityAlertingPromtoolRules` and
  `TestSecurityAlertingEndToEndLocalReceiver` whenever any metric in
  `docs/metrics-contract.json` or alert rule in `security-alerts.rules.yml`
  changes.

# Jul.IA — Soak Procedures (Local Windows)

> Version 1.30 · Updated 2026-07-03
>
> Step-by-step soak guide for three target durations. All procedures target a
> Windows/amd64 development workstation with Go 1.26+ and PowerShell.

## Prerequisites

- [ ] Go 1.26+ installed (`go version`)
- [ ] Jul repository cloned locally (`c:\Users\victornf\...\http_server`)
- [ ] PowerShell session open in repository root
- [ ] No other processes bound to ports likely used by soak tests (e.g. 8080–8099). The tests use `httptest.NewServer` which binds to random high ports.
- [ ] Machine can be left running unattended for the target duration.

## Common environment

```powershell
# Set once per PowerShell session
$env:FULL_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
$env:SOAK_WORKERS="16"   # see per-duration guidance below
```

> **Windows note:** `SOAK_WORKERS=32` can exhaust ephemeral ports on the client side (the test dials repeatedly from the same machine). Even at 16 workers, the proxy soak fails on Windows within ~2 minutes (2026-07-03/04). **The proxy soak is only viable on Windows for smoke durations (≤20s).** For longer proxy validation, use the Linux CI release gate or a real binary burn-in (see procedure C below). The UDP-churn soak works reliably on Windows at 16 workers.

---

## Procedure A — 5‑minute release gate

**Goal:** Reproduce the CI release gate locally. Proves no rapid goroutine or heap leak under sustained concurrent load.

**When to run:** Before every version tag, after any reload-timeout or connection-pool change, after any middleware or handler change.

### Commands

```powershell
# 1. Proxy soak (5 min)
$env:SOAK_DURATION="5m"
$env:SOAK_WORKERS="16"
go test -tags "soak $env:FULL_TAGS" -run '^TestSoak$' -count=1 -timeout=0 -v ./internal/handler/ | Tee-Object -FilePath soak-proxy-5m.log

# 2. UDP churn soak (5 min)
go test -tags "soak stream $env:FULL_TAGS" -run '^TestSoakUDPChurn$' -count=1 -timeout=0 -v ./internal/stream/ | Tee-Object -FilePath soak-udp-5m.log
```

### What to observe

The test output will show lines like:

```
soak: duration=5m0s workers=16 requests=XXXXXX errors=0
soak: goroutines XX -> YY, heap XXXXXX -> YYYYYY bytes
```

| Metric | Pass criterion |
|--------|---------------|
| `errors=0` | Must be exactly zero |
| Goroutine growth | ≤ `4*workers+32` (so ≤ 96 for 16 workers) |
| Heap growth | ≤ 64 MiB |

### Artifact

- `soak-proxy-5m.log`
- `soak-udp-5m.log`

---

## Procedure B — 1‑hour stability run

**Goal:** Detect slow leaks that a 5‑minute gate misses. Measures GC pressure, connection-pool health, and bounded heap growth over a longer window.

**When to run:** After any memory-allocator change (cache, buffer pool, large-object retention), before a minor release (v1.x.0), after any stream or UDP session cleanup change.

### Commands

```powershell
# Proxy soak (1 hr)
$env:SOAK_DURATION="1h"
$env:SOAK_WORKERS="16"
go test -tags "soak $env:FULL_TAGS" -run '^TestSoak$' -count=1 -timeout=0 -v ./internal/handler/ | Tee-Object -FilePath soak-proxy-1h.log

# UDP churn soak (1 hr)
go test -tags "soak stream $env:FULL_TAGS" -run '^TestSoakUDPChurn$' -count=1 -timeout=0 -v ./internal/stream/ | Tee-Object -FilePath soak-udp-1h.log
```

### What to observe

| Metric | Pass criterion |
|--------|---------------|
| `errors=0` | Exactly zero |
| Goroutine growth | Same bounded gate (≤ 96) — if it leaks slowly it will still be caught |
| Heap growth | ≤ 64 MiB — a slow leak will be caught because the budget is absolute, not per-minute |

> **Time expectation:** Each scenario runs for the full wall-clock duration. Two scenarios back-to-back = 2 hours.

### Artifacts

- `soak-proxy-1h.log`
- `soak-udp-1h.log`

---

## Procedure C — 24‑hour burn‑in

**Goal:** Validate true long-term stability on Windows. This is the closest you can get to a production-like burn-in without dedicated staging hardware.

**When to run:** Before a major release (v2.0.0), after any runtime upgrade (Go version bump), after any architectural change affecting goroutine lifecycle, after any plugin ABI or stream proxy change.

### Setup

1. **Ensure the machine will not sleep:**
   ```powershell
   powercfg /change standby-timeout-ac 0
   powercfg /change monitor-timeout-ac 30
   ```

2. **Create a logging directory:**
   ```powershell
   New-Item -ItemType Directory -Force -Path "soak-artifacts\2026-07-04"
   cd soak-artifacts\2026-07-04
   ```

3. **Capture a baseline sample** (before soak starts):
   ```powershell
   # We capture the Go runtime stats from the test itself, but you can also snapshot the host:
   Get-Process | Where-Object {$_.ProcessName -like "go*"} | Select-Object Name, Id, WorkingSet | Out-File baseline-processes.txt
   systeminfo | findstr /B /C:"OS Name" /C:"Total Physical Memory" | Out-File baseline-system.txt
   ```

### Commands

```powershell
$env:SOAK_DURATION="24h"
$env:SOAK_WORKERS="8"   # lower to reduce client-side port pressure over 24h

# Proxy soak (24 hrs)
go test -tags "soak $env:FULL_TAGS" -run '^TestSoak$' -count=1 -timeout=0 -v ./internal/handler/ | Tee-Object -FilePath soak-proxy-24h.log

# UDP churn soak (24 hrs)
go test -tags "soak stream $env:FULL_TAGS" -run '^TestSoakUDPChurn$' -count=1 -timeout=0 -v ./internal/stream/ | Tee-Object -FilePath soak-udp-24h.log
```

> **Important:** Run each scenario in a separate PowerShell window. If one fails, the other is not blocked. Do not run simultaneously — each scenario is CPU-intensive and will interfere with the other.

### What to observe

Same gates as 1‑hour, but over 24 hours the signal is much stronger:

| Metric | Interpretation |
|--------|---------------|
| `errors=0` | Any non-zero error is a regression |
| Goroutine growth ≤ 96 | Catches slow per-request goroutine leaks |
| Heap growth ≤ 64 MiB | Caches slow object retention leaks |
| Wall-clock runtime | If the test exits early (panic, timeout), it's a critical failure |

### Post-run capture

After both scenarios finish, collect:

```powershell
# Copy logs
copy soak-proxy-24h.log soak-udp-24h.log ..\

# Capture system state
systeminfo | findstr /B /C:"OS Name" /C:"Total Physical Memory" | Out-File post-system.txt

# Zip artifacts
Compress-Archive -Path *.log,*.txt -DestinationPath soak-2026-07-04.zip
```

### Artifacts

- `soak-proxy-24h.log`
- `soak-udp-24h.log`
- `baseline-*.txt`, `post-*.txt` (optional)
- `soak-2026-07-04.zip` (upload to release notes or GitHub Actions artifact)

---

## Interpreting a failure

| Failure mode | Likely cause | Action |
|-------------|-------------|--------|
| `errors > 0` | Handler panic, connection reset, backend failure | Check log for stack trace; run with `-v` |
| Goroutine growth > 96 | Goroutine leak in handler, middleware, or connection pool | Capture `goroutine` profile: `curl http://localhost:9090/debug/pprof/goroutine` if admin is running |
| Heap growth > 64 MiB | Object retention (cache, buffer pool, session table) | Capture `heap` profile: `curl http://localhost:9090/debug/pprof/heap` |
| Test panics / exits early | Critical bug (nil pointer, index out of range) | Full stack trace in log; treat as release blocker |
| `WSASocket` error (Windows) | Ephemeral port exhaustion | Reduce `SOAK_WORKERS` to 8 or 4; this is a test client limitation, not a server leak |

---

## Health check during a long soak

If you want to verify the server is still alive mid-soak, the soak tests themselves do not expose an admin port. For a **true 24-hour production burn-in**, you would instead:

1. Build and run the real binary:
   ```powershell
   go build -tags "$env:FULL_TAGS" -o jul.exe ./cmd/jul
   .\jul.exe -config testdata/static.toml
   ```

2. Drive traffic with an external load generator (`wrk2`, `k6`, or a custom PowerShell script) in a loop.

3. Poll health every 5 minutes:
   ```powershell
   while ($true) { (Measure-Command { curl -s http://localhost:8082/ }).TotalMilliseconds; Start-Sleep -Seconds 300 }
   ```

4. Capture pprof snapshots at T+0, T+12h, T+24h.

The in-tree soak tests (`TestSoak`, `TestSoakUDPChurn`) use `httptest.NewServer` and are **self-contained** — they are designed for CI gates, not for monitoring a live process. For a monitored 24-hour burn-in, use the real binary + external traffic.

---

## Quick reference

| Duration | Workers | Scenario | Command prefix | Time | Use case |
|----------|---------|----------|----------------|------|----------|
| 5 min | 16 | **udp-churn only** | `SOAK_DURATION=5m` | 5 min | Release gate, local validation |
| 20 s | 16–24 | proxy (smoke) | `SOAK_DURATION=20s` | 20 s | Quick proxy sanity check |
| 1 hour | 16 | udp-churn only | `SOAK_DURATION=1h` | 1 hour | Medium validation |
| 24 hours | 16 | udp-churn only | `SOAK_DURATION=24h` | 24 hours | Major burn-in |

> **Windows limitation:** The `proxy` scenario exhausts ephemeral TCP ports on the test client within ~2 minutes at 16 workers. It is **only viable for smoke durations (≤20s)** on Windows. For full proxy soak coverage, see the Linux CI release gate or use a real binary burn-in (procedure below).

---

## Track 2 — Real binary burn-in (for production-like soak)

The in-tree soak tests are self-contained CI gates. For a **true production burn-in** that validates the full stack (config parser, admin API, TLS, reload, middleware chain):

### 1. Build (with console tag)

```powershell
$env:FULL_TAGS="brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf"
go build -tags "$env:FULL_TAGS" -o jul.exe ./cmd/jul
```

> **Always include the `console` tag.** The web console on the admin listener gives you live traffic, latency, error-rate, and feature-status visibility during the soak. Without it, the admin root serves a static page and the dashboard is unavailable.

### 2. Create a production-like `burn-in.toml`

Use static + proxy routes, health checks, rate limiting, and the admin API.
The admin listener **must** be enabled and carry a token so the console is reachable:

```toml
[global]
log_level = "info"

[admin]
enabled = true
listen  = "127.0.0.1:9090"
token   = "change-me"

[[servers]]
listen = "127.0.0.1:8080"
server_names = ["localhost"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api/" }
  proxy_pass = "http://127.0.0.1:8081"

  [[servers.locations]]
  match = { type = "prefix", path = "/static/" }
  root = "testdata/www"
```

### 3. Start the backend

Spin up a simple backend server (or use `python -m http.server 8081`).

### 4. Start Jul

```powershell
.\jul.exe -config burn-in.toml
```

### 5. Drive traffic with `wrk2` or `k6`

```powershell
# In another PowerShell window:
wrk -t4 -c100 -d24h --latency http://127.0.0.1:8080/static/
```

Or with k6:

```powershell
k6 run --duration 24h --vus 50 burn-in.js
```

### 6. Monitor

```powershell
# Health check every 5 minutes
while ($true) {
    $r = curl -s http://127.0.0.1:9090/api/healthz
    $t = (Measure-Command { curl -s http://127.0.0.1:8080/ }).TotalMilliseconds
    Write-Host "$(Get-Date -Format 'HH:mm:ss') health=$r latency=${t}ms"
    Start-Sleep -Seconds 300
}
```

> **Prefer the web console:** Open `http://127.0.0.1:9090/` in a browser and enter the admin token. The **Overview** panel shows real-time req/s, error rate (5xx), latency (avg/p50/p95/p99), in-flight requests, active connections, cache hit ratio, and 2-minute trend sparklines — far richer than CLI polling. Keep a browser tab open during the soak for at-a-glance health checks.

### 7. Capture pprof snapshots

```powershell
# T+0, T+12h, T+24h
curl -s http://127.0.0.1:9090/debug/pprof/goroutine -o goroutine-T0.out
curl -s http://127.0.0.1:9090/debug/pprof/heap -o heap-T0.out
curl -s http://127.0.0.1:9090/debug/pprof/profile -o cpu-T0.out
```

### 8. Assert

- Zero HTTP 5xx errors
- Goroutine count flat (+/- 10%)
- Heap growth < 10 MiB over 24h post-GC
- p99 latency stable (±20%)
- Jul process did not restart

---

## Related documents

- [soak-evidence.md](soak-evidence.md) — dated run log and artifact links
- [ADR 0005](adr/0005-soak-post-ga-gate.md) — why soak is a post-GA gate
- [scripts/soak.sh](../scripts/soak.sh) — bash harness (Linux CI)
- [status.md](status.md#soak-tracking-post-ga-gate) — per-feature soak status

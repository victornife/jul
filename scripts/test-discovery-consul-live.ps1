# Live integration validation for service discovery (Consul lane)
# Usage: .\scripts\test-discovery-consul-live.ps1 [-CI]
#
# The Consul lane needs only a running Docker daemon and the Go toolchain, so it
# is the lane wired into CI (.github/workflows/discovery-live.yml). Pass -CI to
# skip the developer-only Kubernetes-context probe (there is no cluster on the
# CI runner) and to emit a machine-readable CI marker alongside the evidence.
param(
    [switch]$CI
)

$ErrorActionPreference = "Stop"

function Invoke-Step {
    param(
        [string]$Name,
        [scriptblock]$Action
    )
    Write-Host "=== $Name ==="
    & $Action
}

$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $root

$artifacts = Join-Path $root "tmp\issue24"
New-Item -ItemType Directory -Force -Path $artifacts | Out-Null

$julPath = Join-Path $root "jul.exe"
Invoke-Step "Building jul.exe with consul+kubernetes tags" {
    go build -tags "consul kubernetes" -o jul.exe ./cmd/jul
}

Invoke-Step "Preflight checks" {
    docker version | Out-Null
    # The Consul lane does not use Kubernetes; the context probe is developer
    # convenience only. Skip it under -CI (no cluster on the runner) and keep it
    # best-effort locally so a machine without a kube context can still run it.
    $ctx = "skipped-ci"
    if (-not $CI) {
        try { $ctx = (kubectl config current-context) } catch { $ctx = "unavailable" }
    }
    "k8s-context=$ctx" | Set-Content -Encoding ascii -Path (Join-Path $artifacts "k8s-context.txt")
    docker version | Out-File -FilePath (Join-Path $artifacts "docker-version.txt") -Encoding ascii
    go version | Out-File -FilePath (Join-Path $artifacts "go-version.txt") -Encoding ascii
    if ($CI) {
        "ci_mode=1" | Set-Content -Encoding ascii -Path (Join-Path $artifacts "consul-ci-mode.txt")
    }
}

Invoke-Step "Starting backends and Consul" {
    docker rm -f issue24-be1 issue24-be2 issue24-consul 2>$null | Out-Null
    docker run -d --name issue24-be1 -p 18081:5678 hashicorp/http-echo -text be1 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "failed to start issue24-be1" }
    docker run -d --name issue24-be2 -p 18082:5678 hashicorp/http-echo -text be2 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "failed to start issue24-be2" }
    docker run -d --name issue24-consul -p 8500:8500 hashicorp/consul:1.20 agent -dev -client "0.0.0.0" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "failed to start issue24-consul" }
}

Invoke-Step "Waiting for backend containers readiness" {
    $ok1 = $false
    $ok2 = $false
    for ($i = 0; $i -lt 30; $i++) {
        try {
            $r1 = (Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:18081/").Content.Trim()
            if ($r1 -eq "be1") { $ok1 = $true }
        } catch {}
        try {
            $r2 = (Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:18082/").Content.Trim()
            if ($r2 -eq "be2") { $ok2 = $true }
        } catch {}
        if ($ok1 -and $ok2) { break }
        Start-Sleep -Milliseconds 300
    }
    if (-not $ok1 -or -not $ok2) {
        throw "Backend containers did not become ready (be1=$ok1, be2=$ok2)"
    }
}

Invoke-Step "Waiting for Consul API readiness" {
    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        try {
            $null = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:8500/v1/status/leader"
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    if (-not $ready) {
        throw "Consul API did not become ready on :8500"
    }
}

Invoke-Step "Registering services in Consul" {
    $svc1 = @{
        Name    = "web"
        ID      = "web1"
        Address = "127.0.0.1"
        Port    = 18081
        Weights = @{ Passing = 10; Warning = 1 }
    } | ConvertTo-Json -Depth 5
    $svc2 = @{
        Name    = "web"
        ID      = "web2"
        Address = "127.0.0.1"
        Port    = 18082
        Weights = @{ Passing = 10; Warning = 1 }
    } | ConvertTo-Json -Depth 5

    Invoke-RestMethod -Method Put -Uri "http://127.0.0.1:8500/v1/agent/service/register" -ContentType "application/json" -Body $svc1 | Out-Null
    Invoke-RestMethod -Method Put -Uri "http://127.0.0.1:8500/v1/agent/service/register" -ContentType "application/json" -Body $svc2 | Out-Null
}

$cfgPath = Join-Path $artifacts "consul-live.toml"
@"
[[servers]]
listen = "127.0.0.1:19080"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://web"

[[upstreams]]
name = "web"
strategy = "round_robin"

  [upstreams.discovery]
  type = "consul"
  refresh = "2s"

    [upstreams.discovery.consul]
    address = "http://127.0.0.1:8500"
    service = "web"
    passing_only = false
"@ | Set-Content -Encoding ascii -Path $cfgPath

Invoke-Step "Jul check" {
    & $julPath check -config $cfgPath
}

$outLog = Join-Path $artifacts "consul-jul.out.log"
$errLog = Join-Path $artifacts "consul-jul.err.log"
$before = Join-Path $artifacts "consul-before.txt"
$after = Join-Path $artifacts "consul-after.txt"
$proc = $null

try {
    Invoke-Step "Starting Jul with Consul discovery config" {
        $proc = Start-Process -FilePath $julPath -ArgumentList "--config `"$cfgPath`"" -PassThru -NoNewWindow -RedirectStandardOutput $outLog -RedirectStandardError $errLog
    }

    Invoke-Step "Waiting for Jul listener readiness" {
        $ready = $false
        for ($i = 0; $i -lt 30; $i++) {
            try {
                $null = Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:19080/"
                $ready = $true
                break
            } catch {
                Start-Sleep -Milliseconds 300
            }
        }
        if (-not $ready) {
            throw "Jul did not start listener on :19080"
        }
    }

    Invoke-Step "Collecting pre-change responses" {
        $bothObserved = $false
        for ($i = 0; $i -lt 30; $i++) {
            $window = @()
            for ($j = 0; $j -lt 6; $j++) {
                try {
                    $window += (Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:19080/").Content.Trim()
                } catch {
                    $window += "REQUEST_ERROR"
                }
            }
            if (($window -contains "be1") -and ($window -contains "be2")) {
                $bothObserved = $true
                break
            }
            Start-Sleep -Milliseconds 300
        }
        if (-not $bothObserved) {
            throw "Did not observe both be1 and be2 before deregistration"
        }

        $results = @()
        for ($i = 0; $i -lt 20; $i++) {
            try {
                $results += (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:19080/").Content.Trim()
            } catch {
                $results += "REQUEST_ERROR"
            }
        }
        $results | Set-Content -Encoding ascii -Path $before
    }

    Invoke-Step "Deregistering one Consul instance" {
        Invoke-RestMethod -Method Put -Uri "http://127.0.0.1:8500/v1/agent/service/deregister/web2" | Out-Null
    }

    Invoke-Step "Waiting for discovery convergence (be2 removed)" {
        $converged = $false
        for ($i = 0; $i -lt 30; $i++) {
            $sample = @()
            for ($j = 0; $j -lt 6; $j++) {
                try {
                    $sample += (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:19080/").Content.Trim()
                } catch {
                    $sample += "REQUEST_ERROR"
                }
            }
            if (($sample | Where-Object { $_ -eq "be2" }).Count -eq 0 -and ($sample | Where-Object { $_ -eq "be1" }).Count -gt 0) {
                $converged = $true
                break
            }
            Start-Sleep -Milliseconds 400
        }
        if (-not $converged) {
            throw "Discovery did not converge away from be2 within timeout"
        }
    }

    Invoke-Step "Collecting post-change responses" {
        $results = @()
        for ($i = 0; $i -lt 20; $i++) {
            try {
                $results += (Invoke-WebRequest -UseBasicParsing "http://127.0.0.1:19080/").Content.Trim()
            } catch {
                $results += "REQUEST_ERROR"
            }
        }
        $results | Set-Content -Encoding ascii -Path $after
    }

    Invoke-Step "Evaluating Consul lane assertions" {
        $beforeData = Get-Content $before
        $afterData = Get-Content $after

        $hasBothBefore = ($beforeData -contains "be1") -and ($beforeData -contains "be2")
        $onlyBe1After = ($afterData | Where-Object { $_ -ne "be1" }).Count -eq 0

        $summary = @(
            "consul_lane=PASS"
            "before_has_be1=$($beforeData -contains 'be1')"
            "before_has_be2=$($beforeData -contains 'be2')"
            "after_only_be1=$onlyBe1After"
        )

        if (-not $hasBothBefore -or -not $onlyBe1After) {
            $summary[0] = "consul_lane=FAIL"
            $summary += "reason=assertions_failed"
            $summary | Set-Content -Encoding ascii -Path (Join-Path $artifacts "consul-summary.txt")
            throw "Consul assertions failed. See consul-before.txt and consul-after.txt"
        }

        $summary | Set-Content -Encoding ascii -Path (Join-Path $artifacts "consul-summary.txt")
    }
}
finally {
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force
    }
    # Capture container logs as evidence BEFORE teardown so a CI failure has
    # actionable diagnostics (the containers are removed on the next line).
    foreach ($c in @("issue24-consul", "issue24-be1", "issue24-be2")) {
        try {
            docker logs $c 2>&1 | Set-Content -Encoding ascii -Path (Join-Path $artifacts "docker-$c.log")
        } catch {}
    }
    docker rm -f issue24-consul issue24-be1 issue24-be2 2>$null | Out-Null
}

Write-Host "=== Consul live lane PASSED ==="

# Track 2 — Real binary burn-in load generator (PowerShell)
# Usage: .\scripts\burn-in-load.ps1 -DurationMinutes 5
param(
    [int]$DurationMinutes = 5,
    [int]$Workers = 50,
    [string]$BaseUrl = "http://127.0.0.1:8080",
    [string]$HealthUrl = "http://127.0.0.1:8082/healthz",
    [string]$AdminUrl = "http://127.0.0.1:9090"
)

$duration = [TimeSpan]::FromMinutes($DurationMinutes)
$endTime = [DateTime]::UtcNow + $duration
$staticPath = "/static/"
$apiPath = "/api/"

Write-Host "Burn-in load test starting..."
Write-Host "Duration       : $DurationMinutes minutes"
Write-Host "Workers        : $Workers"
Write-Host "Target         : $BaseUrl"
Write-Host "Health check   : $HealthUrl"
Write-Host "Admin / pprof  : $AdminUrl"
Write-Host "End time       : $($endTime.ToString('HH:mm:ss')) UTC"
Write-Host ""

# Health check before starting
Write-Host "Pre-flight health check..."
try {
    $h = Invoke-WebRequest -Uri $HealthUrl -UseBasicParsing -TimeoutSec 5
    Write-Host "Health OK: $($h.StatusCode) $($h.Content)" -ForegroundColor Green
} catch {
    Write-Host "Health check FAILED: $_" -ForegroundColor Red
    exit 1
}

# Capture T+0 pprof snapshots
$pprofDir = "burn-in-artifacts"
if (-not (Test-Path $pprofDir)) { New-Item -ItemType Directory -Path $pprofDir | Out-Null }
Write-Host "Capturing T+0 pprof snapshots..."
try { curl -s "$AdminUrl/debug/pprof/goroutine" -o "$pprofDir/goroutine-T0.out" } catch {}
try { curl -s "$AdminUrl/debug/pprof/heap" -o "$pprofDir/heap-T0.out" } catch {}
try { curl -s "$AdminUrl/debug/pprof/profile?seconds=5" -o "$pprofDir/cpu-T0.out" } catch {}

$workerScript = {
    param($EndTime, $BaseUrl, $StaticPath, $ApiPath)
    $results = @()
    while ([DateTime]::UtcNow -lt $EndTime) {
        $path = if ((Get-Random -Maximum 2) -eq 0) { $StaticPath } else { $ApiPath }
        $url = "$BaseUrl$path"
        $sw = [System.Diagnostics.Stopwatch]::StartNew()
        try {
            $r = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 10 -ErrorAction Stop
            $sw.Stop()
            if ($r.StatusCode -ge 500) {
                $results += -1
            } else {
                $results += $sw.ElapsedMilliseconds
            }
        } catch {
            $sw.Stop()
            $results += -1
        }
    }
    return $results
}

$jobs = @()
for ($i = 0; $i -lt $Workers; $i++) {
    $jobs += Start-Job -ScriptBlock $workerScript -ArgumentList $endTime, $BaseUrl, $staticPath, $apiPath
}

# Progress / health poll every 30 seconds
$pollInterval = [TimeSpan]::FromSeconds(30)
$nextPoll = [DateTime]::UtcNow + $pollInterval
$running = $true
while ($running) {
    Start-Sleep -Milliseconds 500
    $running = $false
    foreach ($job in $jobs) {
        if ($job.State -eq 'Running') { $running = $true; break }
    }
    if (-not $running) { break }

    if ([DateTime]::UtcNow -ge $nextPoll) {
        try {
            $h = Invoke-WebRequest -Uri $HealthUrl -UseBasicParsing -TimeoutSec 5
            Write-Host "$(Get-Date -Format 'HH:mm:ss') health=$($h.StatusCode)" -ForegroundColor Cyan
        } catch {
            Write-Host "$(Get-Date -Format 'HH:mm:ss') health=FAIL $($_)" -ForegroundColor Red
        }
        $nextPoll = [DateTime]::UtcNow + $pollInterval
    }
}

Write-Host ""
Write-Host "Load test complete. Collecting results..."

$allResults = @()
foreach ($job in $jobs) {
    $jobResults = Receive-Job -Job $job -Wait
    if ($jobResults) { $allResults += $jobResults }
    Remove-Job -Job $job
}

# Summary
$total = $allResults.Count
$errors = ($allResults | Where-Object { $_ -lt 0 }).Count
$ok = $allResults | Where-Object { $_ -ge 0 }

if ($ok.Count -gt 0) {
    $min = ($ok | Measure-Object -Minimum).Minimum
    $max = ($ok | Measure-Object -Maximum).Maximum
    $avg = ($ok | Measure-Object -Average).Average
    $sorted = $ok | Sort-Object
    $p50 = $sorted[[int]($sorted.Count * 0.5)]
    $p95 = $sorted[[int]($sorted.Count * 0.95)]
    $p99 = $sorted[[int]($sorted.Count * 0.99)]
} else {
    $min = $max = $avg = $p50 = $p95 = $p99 = $null
}

Write-Host ""
Write-Host "========== BURN-IN RESULTS =========="
Write-Host "Duration       : $DurationMinutes min"
Write-Host "Total requests : $total"
Write-Host "Errors (5xx)   : $errors"
Write-Host "Error rate     : $([math]::Round($errors / [math]::Max($total, 1) * 100, 2))%"
if ($ok.Count -gt 0) {
    Write-Host "Latency (ms)   : min=$min avg=$([math]::Round($avg,1)) max=$max p50=$p50 p95=$p95 p99=$p99"
} else {
    Write-Host "Latency (ms)   : N/A (all requests errored)"
}
Write-Host "====================================="

# Capture T+end pprof snapshots
Write-Host "Capturing T+end pprof snapshots..."
try { curl -s "$AdminUrl/debug/pprof/goroutine" -o "$pprofDir/goroutine-Tend.out" } catch {}
try { curl -s "$AdminUrl/debug/pprof/heap" -o "$pprofDir/heap-Tend.out" } catch {}
Write-Host "Artifacts saved to $pprofDir/"

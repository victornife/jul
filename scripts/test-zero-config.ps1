# Test zero-config mode + jul lint (feature #5) — validation script
# Usage: .\scripts\test-zero-config.ps1

$ErrorActionPreference = "Stop"
$proj = "c:\Users\victornf\OneDrive - Inditex\Escritorio\Proyectos SW\http_server"

Write-Host "=== 1. jul run --serve (zero-config file server) ==="
# Ensure port is free
Get-NetTCPConnection -LocalPort 18080 -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 200

$job = Start-Job -ScriptBlock {
    Set-Location "c:\Users\victornf\OneDrive - Inditex\Escritorio\Proyectos SW\http_server"
    .\jul.exe run --serve testdata\www --listen 127.0.0.1:18080
}
Start-Sleep -Seconds 4

try {
    $bound = $false
    for ($i = 0; $i -lt 15; $i++) {
        try {
            $resp = Invoke-WebRequest -Uri "http://127.0.0.1:18080/" -UseBasicParsing -ErrorAction Stop -TimeoutSec 2
            if ($resp.StatusCode -eq 200) {
                Write-Host "OK  : zero-config serve returns 200"
                $bound = $true
                break
            }
        } catch {
            Start-Sleep -Milliseconds 300
        }
    }
    if (-not $bound) {
        Write-Host "FAIL: zero-config serve did not respond on 127.0.0.1:18080"
        exit 1
    }
} finally {
    Stop-Job $job -ErrorAction SilentlyContinue
    Remove-Job $job -ErrorAction SilentlyContinue
}

Write-Host "=== 2. jul lint on burn-in-phase2a.toml (with secrets refs) ==="
$env:JUL_ADMIN_TOKEN = "burnintoken"
& "${proj}\jul.exe" lint -config "${proj}\burn-in-phase2a.toml"
if ($LASTEXITCODE -ne 0) {
    Write-Host "FAIL: jul lint on burn-in-phase2a.toml failed"
    exit 1
}
Write-Host "OK  : jul lint passed (no literal secrets flagged)"

Write-Host "=== 3. jul lint -strict on a config WITH literal secret ==="
$badToml = @"
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "hardcoded-secret"
"@
$badPath = "${proj}\tmp\bad-secret.toml"
New-Item -ItemType Directory -Force -Path "${proj}\tmp" | Out-Null
Set-Content -Path $badPath -Value $badToml -Encoding UTF8 -NoNewline

& "${proj}\jul.exe" lint -strict -config $badPath
$lintCode = $LASTEXITCODE
# Expect non-zero because strict + warnings
if ($lintCode -eq 0) {
    Write-Host "FAIL: strict lint should have flagged literal admin token"
    exit 1
}
Write-Host "OK  : strict lint correctly flagged literal secret (exit $lintCode)"

Write-Host "=== Zero-config + jul lint test PASSED ==="

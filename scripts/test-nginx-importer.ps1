# Test NGINX importer (feature #6) — one-shot validation
# Requires binary built with -tags importer
# Usage: .\scripts\test-nginx-importer.ps1

$ErrorActionPreference = "Stop"
$outFile = "tmp\nginx-imported.toml"

# Ensure tmp dir exists
New-Item -ItemType Directory -Force -Path "tmp" | Out-Null

# 1) Run importer
Write-Host "=== Running jul import nginx ..."
& .\jul.exe import nginx -o $outFile examples\migrate\nginx.conf
if ($LASTEXITCODE -ne 0) {
    Write-Host "FAIL: jul import nginx exited with code $LASTEXITCODE"
    exit 1
}

# 2) Verify output exists
if (-not (Test-Path $outFile)) {
    Write-Host "FAIL: output file $outFile not created"
    exit 1
}

# 3) Validate generated TOML with jul lint
Write-Host "=== Validating generated TOML ..."
& .\jul.exe lint -config $outFile
if ($LASTEXITCODE -ne 0) {
    Write-Host "FAIL: lint of imported TOML failed"
    exit 1
}

# 4) Sanity-check known content is present
$content = Get-Content $outFile -Raw
$checks = @(
    @{ Pattern = 'listen = ''\:80''';      Desc = "HTTP listener" },
    @{ Pattern = 'listen = ''\:443''';     Desc = "HTTPS listener" },
    @{ Pattern = 'proxy_pass = ''http://app'''; Desc = "upstream proxy" },
    @{ Pattern = "least_conn";            Desc = "least_conn strategy" }
)

$allOk = $true
foreach ($c in $checks) {
    if ($content -match $c.Pattern) {
        Write-Host "OK  : $($c.Desc)"
    } else {
        Write-Host "FAIL: $($c.Desc) not found in output"
        $allOk = $false
    }
}

if (-not $allOk) {
    exit 1
}

Write-Host "=== NGINX importer test PASSED ==="

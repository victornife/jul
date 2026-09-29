# Windows assessment and conversion check for the documented migration fixture.
# From any directory: .\scripts\test-nginx-importer.ps1 -BinaryPath .\jul-importer.exe
param([string]$BinaryPath)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $BinaryPath) { $BinaryPath = Join-Path $root 'jul.exe' }
$jul = (Resolve-Path $BinaryPath).Path
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('jul-import-' + [guid]::NewGuid().ToString('N'))
$source = Join-Path $root 'examples\migrate\nginx.conf'
$assessment = Join-Path $scratch 'assessment.json'
$conversion = Join-Path $scratch 'conversion.json'
$candidate = Join-Path $scratch 'candidate.toml'

try {
    New-Item -ItemType Directory -Force -Path $scratch | Out-Null
    & $jul import nginx --assess --report $assessment $source
    if ($LASTEXITCODE -ne 3) { throw "assessment exited $LASTEXITCODE; expected manual_action_required (3)" }
    $a = Get-Content -Path $assessment -Raw | ConvertFrom-Json
    if ($a.status -ne 'manual_action_required') { throw "assessment status: $($a.status)" }

    & $jul import nginx -o $candidate --report $conversion $source
    if ($LASTEXITCODE -ne 3) { throw "conversion exited $LASTEXITCODE; expected manual_action_required (3)" }
    $c = Get-Content -Path $conversion -Raw | ConvertFrom-Json
    if ($c.status -ne 'manual_action_required') { throw "conversion status: $($c.status)" }
    if (-not (Test-Path $candidate)) { throw 'candidate TOML was not written' }

    & $jul lint -config $candidate
    if ($LASTEXITCODE -ne 0) { throw "candidate lint exited $LASTEXITCODE" }
    $content = Get-Content -Path $candidate -Raw
    foreach ($pattern in @("listen = ':80'", "listen = ':443'", "proxy_pass = 'http://app'", "strategy = 'least_conn'", 'TODO line 52: proxy_set_header')) {
        if (-not $content.Contains($pattern)) { throw "candidate is missing expected $pattern" }
    }
    Write-Host 'Windows NGINX migration assessment and candidate check passed (manual action remains required)'
} finally {
    Remove-Item -Path $scratch -Recurse -Force -ErrorAction SilentlyContinue
}

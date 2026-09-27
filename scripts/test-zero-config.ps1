# Windows first-run check for the documented zero-config and lint paths.
# From any directory: .\scripts\test-zero-config.ps1 -BinaryPath .\jul.exe
param([string]$BinaryPath)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $BinaryPath) { $BinaryPath = Join-Path $root 'jul.exe' }
$jul = (Resolve-Path $BinaryPath).Path
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('jul-zero-config-' + [guid]::NewGuid().ToString('N'))
$site = Join-Path $scratch 'public'
$server = $null
$priorToken = $env:JUL_ADMIN_TOKEN

function Assert-ExitCode([int]$Expected, [string]$Action) {
    if ($LASTEXITCODE -ne $Expected) {
        throw "$Action exited $LASTEXITCODE; expected $Expected"
    }
}

try {
    New-Item -ItemType Directory -Force -Path $site | Out-Null
    Set-Content -Path (Join-Path $site 'index.html') -Value 'Jul Windows first run' -NoNewline
    # Own only the process we started. A port conflict is an error, never a
    # reason to force-stop an unrelated listener on the operator's machine.
    $server = Start-Process -FilePath $jul -ArgumentList @('run', '--serve', "`"$site`"", '--listen', '127.0.0.1:18080') -PassThru -NoNewWindow
    $served = $false
    for ($i = 0; $i -lt 30; $i++) {
        if ($server.HasExited) { throw "zero-config Jul exited early with $($server.ExitCode)" }
        try {
            $response = Invoke-WebRequest -Uri 'http://127.0.0.1:18080/' -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200 -and $response.Content -match 'Jul Windows first run') {
                $served = $true
                break
            }
        } catch { Start-Sleep -Milliseconds 250 }
    }
    if (-not $served) { throw 'zero-config Jul did not serve the expected page' }

    $env:JUL_ADMIN_TOKEN = 'test-only-token'
    & $jul lint -config (Join-Path $root 'burn-in-phase2a.toml')
    Assert-ExitCode 0 'lint with secret reference'

    $badPath = Join-Path $scratch 'bad-secret.toml'
    @'
[global]
log_level = "info"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "literal-test-only-secret"
'@ | Set-Content -Path $badPath
    & $jul lint -strict -quiet -config $badPath
    Assert-ExitCode 1 'strict quiet lint with literal test secret'
    Write-Host 'Windows zero-config and lint journey passed'
} finally {
    if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force }
    Remove-Item -Path $scratch -Recurse -Force -ErrorAction SilentlyContinue
    if ($null -eq $priorToken) {
        Remove-Item Env:JUL_ADMIN_TOKEN -ErrorAction SilentlyContinue
    } else {
        $env:JUL_ADMIN_TOKEN = $priorToken
    }
}

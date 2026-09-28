# Exercise the shipped Windows service installer and managed recovery on an
# elevated, disposable CI VM. Refuse to modify a pre-existing `jul` service.
param(
    [string] $BinaryPath = '',
    [string] $InstallerPath = '',
    [string] $ProvisionPath = ''
)

$ErrorActionPreference = 'Stop'
if (-not $InstallerPath) { $InstallerPath = Join-Path $PSScriptRoot '..\deploy\windows\install-service.ps1' }
if (-not $ProvisionPath) { $ProvisionPath = Join-Path $PSScriptRoot '..\deploy\windows\new-secure-data-dir.ps1' }
$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Windows service E2E requires an elevated runner.'
}
if (Get-Service -Name 'jul' -ErrorAction SilentlyContinue) {
    throw 'Refusing to replace an existing jul service.'
}

function Get-FreePort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return $listener.LocalEndpoint.Port }
    finally { $listener.Stop() }
}

function Assert-Status($Response, [int] $Expected) {
    if ([int]$Response.StatusCode -ne $Expected) {
        throw "HTTP $($Response.StatusCode), expected $Expected : $($Response.Content)"
    }
}

function Invoke-Admin([string] $Method, [string] $Path, [string] $Body = '', [string] $ContentType = '') {
    $params = @{
        Uri = "$script:adminURL$Path"
        Method = $Method
        Headers = @{ Authorization = "Bearer $script:token" }
        SkipHttpErrorCheck = $true
        TimeoutSec = 5
    }
    if ($ContentType) { $params.ContentType = $ContentType }
    if ($Method -eq 'POST') { $params.Body = $Body }
    return Invoke-WebRequest @params
}

function Get-Config {
    $response = Invoke-Admin 'GET' '/api/config'
    Assert-Status $response 200
    return $response.Content | ConvertFrom-Json
}

function Wait-Jul {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $service = Get-Service -Name 'jul' -ErrorAction Stop
        if ($service.Status -eq 'Stopped') {
            throw 'jul service stopped before the HTTP listener became ready.'
        }
        try {
            $traffic = Invoke-WebRequest -Uri $script:trafficURL -SkipHttpErrorCheck -TimeoutSec 2
            $ready = Invoke-WebRequest -Uri "$script:adminURL/readyz" -SkipHttpErrorCheck -TimeoutSec 2
            if ([int]$traffic.StatusCode -eq 200 -and [int]$ready.StatusCode -eq 200) {
                if ($traffic.Content -notmatch 'Jul Windows service E2E') { throw 'Unexpected static site content.' }
                return
            }
        } catch [System.Net.Http.HttpRequestException] { }
        Start-Sleep -Seconds 1
    }
    throw 'jul service did not serve the static site and readiness probe within 60 seconds.'
}

$root = Join-Path $env:ProgramData ('jul-service-e2e-' + [guid]::NewGuid().ToString('N'))
$configDir = Join-Path $root 'config'
$configPath = Join-Path $configDir 'server.toml'
$dataDir = Join-Path $root 'data'
$siteDir = Join-Path $root 'site'
$binary = Join-Path $root 'jul.exe'
$serviceCreated = $false
$rootCreated = $false
$probeUser = 'julprobe' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$probeCreated = $false
$probePath = Join-Path $env:PUBLIC ($probeUser + '.ps1')
try {
    & $ProvisionPath -Path $root
    $rootCreated = $true
    New-Item -ItemType Directory -Path $configDir, $siteDir -Force | Out-Null
    Set-Content -Path (Join-Path $siteDir 'index.html') -Value 'Jul Windows service E2E' -Encoding utf8NoBOM
    if ($BinaryPath) {
        if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) { throw "Packaged binary missing: $BinaryPath" }
        Copy-Item -LiteralPath $BinaryPath -Destination $binary
    } else {
        & go build -tags console -o $binary ./cmd/jul
        if ($LASTEXITCODE -ne 0) { throw "Go build failed (exit $LASTEXITCODE)." }
    }

    $trafficPort = Get-FreePort
    $adminPort = Get-FreePort
    while ($adminPort -eq $trafficPort) { $adminPort = Get-FreePort }
    $script:trafficURL = "http://127.0.0.1:$trafficPort/"
    $script:adminURL = "http://127.0.0.1:$adminPort"
    $script:token = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(36))
    $site = $siteDir.Replace('\', '/')
    $history = (Join-Path $dataDir 'history').Replace('\', '/')
    $config = @"
[global]
config_authority = "managed"
log_level = "info"

[[servers]]
listen = "127.0.0.1:$trafficPort"
server_names = ["localhost"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "$site"
  index = ["index.html"]

[admin]
enabled = true
listen = "127.0.0.1:$adminPort"
token = "$script:token"
history_dir = "$history"
"@
    Set-Content -Path $configPath -Value $config -Encoding utf8NoBOM
    & $binary check --config $configPath
    if ($LASTEXITCODE -ne 0) { throw "Config check failed (exit $LASTEXITCODE)." }

    & $InstallerPath `
        -BinaryPath $binary -ConfigPath $configPath -DataDir $dataDir
    if (-not (Get-Service -Name 'jul' -ErrorAction SilentlyContinue)) {
        throw 'Installer returned without registering jul service.'
    }
    $serviceCreated = $true
    $installed = Get-CimInstance Win32_Service -Filter "Name='jul'"
    if ($installed.StartName -ne 'NT SERVICE\jul') {
        throw "Unexpected service account: $($installed.StartName)"
    }

    # This CI fixture keeps its executable and static site under the protected
    # root. The service needs read/traverse there in addition to its installer
    # grants on config and writable data; ordinary users must still be denied.
    & icacls.exe $root /grant 'NT SERVICE\jul:(OI)(CI)(RX)' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Granting service read access to fixture root failed (exit $LASTEXITCODE)." }

    $password = ConvertTo-SecureString ('J!7' + [guid]::NewGuid().ToString('N')) -AsPlainText -Force
    New-LocalUser -Name $probeUser -Password $password -PasswordNeverExpires | Out-Null
    $probeCreated = $true
    $probe = @'
param([string] $ConfigPath, [string] $HistoryDir)
try { [IO.File]::ReadAllText($ConfigPath) | Out-Null; exit 21 }
catch [UnauthorizedAccessException] { }
try { [IO.File]::WriteAllText((Join-Path $HistoryDir 'ordinary-user-write'), 'x'); exit 22 }
catch [UnauthorizedAccessException] { }
exit 0
'@
    Set-Content -Path $probePath -Value $probe -Encoding utf8NoBOM
    $credential = [PSCredential]::new("$env:COMPUTERNAME\$probeUser", $password)
    $probeArgs = "-NoProfile -NonInteractive -File `"$probePath`" `"$configPath`" `"$(Join-Path $dataDir 'history')`""
    $ordinary = Start-Process -FilePath (Get-Command pwsh).Source -ArgumentList $probeArgs `
        -Credential $credential -Wait -PassThru
    if ($ordinary.ExitCode -ne 0) {
        throw "Ordinary-user ACL probe exited $($ordinary.ExitCode) (21=config readable, 22=history writable)."
    }

    Start-Service -Name 'jul'
    Wait-Jul
    $unauthorized = Invoke-WebRequest -Uri "$script:adminURL/api/config" -SkipHttpErrorCheck -TimeoutSec 5
    Assert-Status $unauthorized 401

    $previewResponse = Invoke-Admin 'GET' '/api/config/adopt-external/preview'
    Assert-Status $previewResponse 200
    $preview = $previewResponse.Content | ConvertFrom-Json
    if (-not $preview.ok -or $preview.origin -ne 'no_baseline' -or -not $preview.observed_digest) {
        throw 'Fresh managed service did not offer initial adoption.'
    }
    $adoption = @{
        observed_digest = $preview.observed_digest
        base_version = $preview.base_version
        mode = 'hot'
        confirm = $true
    } | ConvertTo-Json -Compress
    $adoptResponse = Invoke-Admin 'POST' '/api/config/adopt-external' $adoption 'application/json'
    Assert-Status $adoptResponse 200

    $before = Get-Config
    if (-not $before.base_version -or -not $before.raw.Contains('log_level = "info"')) {
        throw 'Initial persisted config or base version was missing.'
    }
    $candidate = $before.raw.Replace('log_level = "info"', 'log_level = "debug"')
    $apply = Invoke-Admin 'POST' "/api/config/apply?base_version=$([uri]::EscapeDataString($before.base_version))" `
        $candidate 'application/toml'
    Assert-Status $apply 200
    if (-not (Get-Config).raw.Contains('log_level = "debug"')) { throw 'Apply did not persist debug level.' }

    $historyResponse = Invoke-Admin 'GET' '/api/config/history'
    Assert-Status $historyResponse 200
    $entries = @($historyResponse.Content | ConvertFrom-Json)
    if ($entries.Count -lt 1 -or -not $entries[0].id) { throw 'Apply did not create a history snapshot.' }
    $rollbackBody = @{ id = $entries[0].id } | ConvertTo-Json -Compress
    for ($attempt = 0; $attempt -lt 5; $attempt++) {
        $rollback = Invoke-Admin 'POST' '/api/config/rollback' $rollbackBody 'application/json'
        if ([int]$rollback.StatusCode -ne 409) { break }
        Start-Sleep -Milliseconds 500
    }
    if ([int]$rollback.StatusCode -notin @(200, 204)) {
        throw "Rollback failed ($($rollback.StatusCode)): $($rollback.Content)"
    }
    if (-not (Get-Config).raw.Contains('log_level = "info"')) { throw 'Rollback did not restore info level.' }
    Restart-Service -Name 'jul'
    Wait-Jul
    if (-not (Get-Config).raw.Contains('log_level = "info"')) { throw 'Rollback was lost on restart.' }
    Write-Host 'PASS: protected Windows service, ordinary-user denial, adoption, Apply, rollback and restart'
} finally {
    if ($serviceCreated -or (Get-Service -Name 'jul' -ErrorAction SilentlyContinue)) {
        Stop-Service -Name 'jul' -Force -ErrorAction SilentlyContinue
        & sc.exe delete jul | Out-Null
        if ($LASTEXITCODE -ne 0) { Write-Warning "Could not delete test jul service (exit $LASTEXITCODE)." }
        for ($attempt = 0; $attempt -lt 15; $attempt++) {
            if (-not (Get-Service -Name 'jul' -ErrorAction SilentlyContinue)) { break }
            Start-Sleep -Seconds 1
        }
    }
    if ($probeCreated) { Remove-LocalUser -Name $probeUser -ErrorAction SilentlyContinue }
    if (Test-Path $probePath) { Remove-Item -Path $probePath -Force }
    if ($rootCreated -and (Test-Path $root)) {
        Remove-Item -Path $root -Recurse -Force
    }
}

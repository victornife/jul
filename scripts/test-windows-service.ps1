# Exercise the shipped Windows service installer and managed recovery on an
# elevated, disposable CI VM. Refuse to modify a pre-existing `jul` service.
param(
    [string] $BinaryPath = '',
    [string] $InstallerPath = '',
    [string] $ProvisionPath = '',
    [ValidateSet('Fresh', 'ExistingAcl', 'Unix', 'Mime')] [string] $Journey = 'Fresh',
    [string] $ReportPath = ''
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

function Assert-Native([string] $Operation) {
    if ($LASTEXITCODE -ne 0) { throw "$Operation failed (exit $LASTEXITCODE)." }
}

function Get-AclEntries([string] $Path) {
    return @((Get-Acl -LiteralPath $Path).Access | ForEach-Object {
        '{0}|{1}|{2}|{3}|{4}|{5}' -f $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value,
            $_.AccessControlType, $_.FileSystemRights, $_.InheritanceFlags, $_.PropagationFlags, $_.IsInherited
    })
}

function Start-UnixBackend {
    foreach ($path in @($script:socketPath, $script:backendReady)) {
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
    }
    $arguments = @('"' + (Join-Path $PSScriptRoot 'windows-deployment-backend.py') + '"',
                  '--unix', '"' + $script:socketPath + '"', '--ready-file', '"' + $script:backendReady + '"')
    $process = Start-Process -FilePath (Get-Command python).Source -ArgumentList $arguments -PassThru `
        -RedirectStandardError (Join-Path $root 'backend-errors.log')
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        if ($process.HasExited) { throw 'AF_UNIX backend exited before readiness.' }
        if (Test-Path -LiteralPath $script:backendReady) { return $process }
        Start-Sleep -Milliseconds 200
    }
    Stop-Process -Id $process.Id -Force
    throw 'AF_UNIX backend did not become ready.'
}

function Invoke-OrdinaryProbe([switch] $Readable, [string] $Config, [string] $History, [string] $Marker = '') {
    $probeArgs = "-NoProfile -NonInteractive -File `"$probePath`" -ConfigPath `"$Config`" -HistoryDir `"$History`""
    if ($Readable) { $probeArgs += ' -ExpectReadable' }
    if ($Marker) { $probeArgs += " -MarkerPath `"$Marker`"" }
    $ordinary = Start-Process -FilePath (Get-Command pwsh).Source -ArgumentList $probeArgs `
        -Credential $credential -Wait -PassThru
    if ($ordinary.ExitCode -ne 0) { throw "Ordinary-user effective-access probe failed (exit $($ordinary.ExitCode))." }
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
        # A transient connection error or per-attempt timeout can use several
        # PowerShell exception types. Only the HTTP calls are retried; an
        # unexpected successful response below remains a hard failure.
        try {
            $traffic = Invoke-WebRequest -Uri $script:trafficURL -SkipHttpErrorCheck -TimeoutSec 2
            $ready = Invoke-WebRequest -Uri "$script:adminURL/readyz" -SkipHttpErrorCheck -TimeoutSec 2
        } catch {
            Start-Sleep -Seconds 1
            continue
        }
        if ([int]$traffic.StatusCode -eq 200 -and [int]$ready.StatusCode -eq 200) {
            if ($traffic.Content -notmatch 'Jul Windows service E2E') { throw 'Unexpected static site content.' }
            return
        }
        Start-Sleep -Seconds 1
    }
    throw 'jul service did not serve the static site and readiness probe after 60 attempts.'
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
$permissiveRoot = ''
$backendProcess = $null
$mimeExtension = '.j516' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$registryPath = "Registry::HKEY_LOCAL_MACHINE\SOFTWARE\Classes\$mimeExtension"
$registryCreated = $false
$tsPath = 'Registry::HKEY_LOCAL_MACHINE\SOFTWARE\Classes\.ts'
$tsChanged = $false
$report = [ordered]@{ journey = $Journey; evidence_class = 'controlled-ci-lab';
    harness_source = (& git rev-parse HEAD); checks = [ordered]@{};
    host = @{ os = 'windows'; arch = $env:PROCESSOR_ARCHITECTURE; version = [Environment]::OSVersion.Version.ToString() };
    limits = @('Disposable native Windows runner and synthetic fixtures, not an enterprise ACL/registry inventory') }
try {
    if ($Journey -eq 'ExistingAcl') {
        New-Item -ItemType Directory -Path $root -ErrorAction Stop | Out-Null
        & icacls.exe $root /inheritance:r | Out-Null
        Assert-Native 'Protecting pre-existing root'
        & icacls.exe $root /grant:r '*S-1-5-18:(OI)(CI)(F)' '*S-1-5-32-544:(OI)(CI)(F)' '*S-1-5-11:(RX)' | Out-Null
        Assert-Native 'Seeding existing root ACLs'
    } else {
        & $ProvisionPath -Path $root
    }
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
    if ($Journey -ne 'Fresh') {
        if (-not $BinaryPath) { throw 'Deployment journeys require the verified published package.' }
        $identity = (& $binary version -json) | ConvertFrom-Json
        Assert-Native 'Reading package identity'
        if ($identity.version -ne 'v2.2.0' -or $identity.commit -ne 'b12e051139f652b400cff37d9f063f5637a7d550' -or $identity.dirty) {
            throw 'Service journey did not receive the unchanged v2.2.0 package.'
        }
        $report.package = @{ metadata = $identity; binary_sha256 = (Get-FileHash -Algorithm SHA256 $binary).Hash.ToLowerInvariant() }
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
    if ($Journey -eq 'ExistingAcl') {
        New-Item -ItemType Directory -Path $dataDir, (Join-Path $dataDir 'history'), (Join-Path $dataDir 'cache'), (Join-Path $dataDir 'logs') -Force | Out-Null
        $marker = Join-Path $root 'preserved.txt'
        Set-Content -LiteralPath $marker -Value 'preserved' -Encoding utf8NoBOM
        & icacls.exe $marker /grant '*S-1-5-11:(R)' | Out-Null
        Assert-Native 'Seeding unrelated marker ACL'
        $sddl = (Get-Acl -LiteralPath $root).Sddl
        $refused = $false
        try { & $ProvisionPath -Path $root }
        catch { $refused = $_.Exception.Message -match 'Refusing to change an existing directory' }
        if (-not $refused -or (Get-Acl -LiteralPath $root).Sddl -ne $sddl) { throw 'Existing-root provision guard changed ACLs or did not refuse.' }
        $report.checks.existing_root_provision_refusal = 'passed'
    }
    if ($Journey -eq 'Unix') {
        New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
        $script:socketPath = Join-Path $dataDir 'backend.sock'
        $script:backendReady = Join-Path $dataDir 'backend-ready.json'
        $socketAddress = $script:socketPath.Replace('\', '/')
        $config += @"

[[upstreams]]
name = "lab-unix"
servers = ["unix:$socketAddress"]

[[servers.locations]]
match = {type = "prefix", path = "/proxy/"}
proxy_pass = "http://lab-unix"
"@
    }
    if ($Journey -eq 'Mime') {
        if (Test-Path -LiteralPath $registryPath) { throw 'Registry fixture already exists.' }
        New-Item -Path $registryPath -Force | Out-Null
        $registryCreated = $true
        New-ItemProperty -Path $registryPath -Name 'Content Type' -Value 'application/x-jul516-native' -PropertyType String -Force | Out-Null
        $tsExisted = Test-Path -LiteralPath $tsPath
        if (-not $tsExisted) { New-Item -Path $tsPath -Force | Out-Null }
        $tsKey = Get-Item -LiteralPath $tsPath
        $tsValueExisted = $tsKey.GetValueNames() -contains 'Content Type'
        if ($tsValueExisted) { $tsValue = $tsKey.GetValue('Content Type'); $tsKind = $tsKey.GetValueKind('Content Type') }
        $tsChanged = $true
        New-ItemProperty -Path $tsPath -Name 'Content Type' -Value 'application/x-jul516-conflict' -PropertyType String -Force | Out-Null
        & python (Join-Path $PSScriptRoot 'windows-deployment-mime.py') prepare --extension $mimeExtension --directory $siteDir
        Assert-Native 'Preparing MIME fixtures'
        $config += @"

[compression]
enabled = true
precompressed = true
encoders = ["gzip"]

[[servers.locations]]
match = {type = "prefix", path = "/native/"}
root = "$site"

[[servers.locations]]
match = {type = "prefix", path = "/explicit/"}
root = "$site"
mime = {types = {"$mimeExtension" = "application/x-jul516-explicit"}, default_type = "application/octet-stream"}
"@
    }
    Set-Content -Path $configPath -Value $config -Encoding utf8NoBOM
    & $binary check --config $configPath
    if ($LASTEXITCODE -ne 0) { throw "Config check failed (exit $LASTEXITCODE)." }

    $beforeAcls = @{}
    if ($Journey -eq 'ExistingAcl') {
        foreach ($path in @($root, $configDir, $configPath, $dataDir, (Join-Path $dataDir 'history'))) {
            $beforeAcls[$path] = @{ owner = (Get-Acl -LiteralPath $path).Owner; entries = @(Get-AclEntries $path) }
        }
    }
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
    $report.checks.virtual_service_identity = 'passed'
    if ($Journey -eq 'ExistingAcl') {
        $report.acl_snapshots = @()
        foreach ($path in $beforeAcls.Keys) {
            $after = @(Get-AclEntries $path)
            if ((Get-Acl -LiteralPath $path).Owner -ne $beforeAcls[$path].owner) { throw 'Installer changed an existing owner.' }
            foreach ($entry in $beforeAcls[$path].entries) {
                if ($entry -notin $after) { throw 'Installer removed a pre-existing ACL entry.' }
            }
            $report.acl_snapshots += @{ fixture_path = 'secure' + $path.Substring($root.Length);
                before = $beforeAcls[$path].entries; after = $after; owner_preserved = $true }
        }
        if ((Get-Content -LiteralPath $marker -Raw).Trim() -ne 'preserved') { throw 'Installer changed unrelated data.' }
        $report.checks.existing_secure_acl_preservation = 'passed'
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
param([string] $ConfigPath, [string] $HistoryDir, [switch] $ExpectReadable, [string] $MarkerPath = '')
$readable = $false
try { [IO.File]::ReadAllText($ConfigPath) | Out-Null; $readable = $true }
catch [UnauthorizedAccessException] { }
if ($readable -ne $ExpectReadable.IsPresent) { exit 21 }
try { [IO.File]::WriteAllText((Join-Path $HistoryDir 'ordinary-user-write'), 'x'); exit 22 }
catch [UnauthorizedAccessException] { }
if ($MarkerPath -and [IO.File]::ReadAllText($MarkerPath).Trim() -ne 'preserved') { exit 23 }
exit 0
'@
    Set-Content -Path $probePath -Value $probe -Encoding utf8NoBOM
    $credential = [PSCredential]::new("$env:COMPUTERNAME\$probeUser", $password)
    $probeMarker = if ($Journey -eq 'ExistingAcl') { $marker } else { '' }
    Invoke-OrdinaryProbe -Config $configPath -History (Join-Path $dataDir 'history') -Marker $probeMarker
    $report.checks.ordinary_user_denied_secure_config_and_history = 'passed'

    if ($Journey -eq 'Unix') { $backendProcess = Start-UnixBackend }

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
    $report.checks.managed_apply_rollback_restart = 'passed'
    if ($Journey -eq 'ExistingAcl') {
        Stop-Service -Name 'jul'
        $permissiveRoot = Join-Path $env:ProgramData ('jul-permissive-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $permissiveRoot -ErrorAction Stop | Out-Null
        & icacls.exe $permissiveRoot /inheritance:r | Out-Null
        Assert-Native 'Protecting permissive fixture inheritance'
        & icacls.exe $permissiveRoot /grant:r '*S-1-5-18:(OI)(CI)(F)' '*S-1-5-32-544:(OI)(CI)(F)' '*S-1-5-11:(OI)(CI)(RX)' | Out-Null
        Assert-Native 'Seeding permissive fixture ACL'
        $permissiveConfig = Join-Path $permissiveRoot 'server.toml'
        $permissiveData = Join-Path $permissiveRoot 'data'
        $permissiveBinary = Join-Path $permissiveRoot 'jul.exe'
        $permissiveSite = Join-Path $permissiveRoot 'site'
        New-Item -ItemType Directory -Path $permissiveSite, (Join-Path $permissiveData 'history') -Force | Out-Null
        Copy-Item -LiteralPath $binary -Destination $permissiveBinary
        Copy-Item -LiteralPath (Join-Path $siteDir 'index.html') -Destination (Join-Path $permissiveSite 'index.html')
        $permissiveRaw = $config.Replace($site, $permissiveSite.Replace('\', '/')).Replace($history, (Join-Path $permissiveData 'history').Replace('\', '/'))
        Set-Content -LiteralPath $permissiveConfig -Value $permissiveRaw -Encoding utf8NoBOM
        Invoke-OrdinaryProbe -Readable -Config $permissiveConfig -History (Join-Path $permissiveData 'history')
        $oldEntries = @(Get-AclEntries $permissiveConfig)
        & $InstallerPath -BinaryPath $permissiveBinary -ConfigPath $permissiveConfig -DataDir $permissiveData
        foreach ($entry in $oldEntries) {
            if ($entry -notin @(Get-AclEntries $permissiveConfig)) { throw 'Installer removed permissive existing entries.' }
        }
        Invoke-OrdinaryProbe -Readable -Config $permissiveConfig -History (Join-Path $permissiveData 'history')
        Start-Service -Name 'jul'
        Wait-Jul
        $report.checks.permissive_acl_retained_and_readable = 'passed'
        Write-Host 'PASS: pre-existing secure and permissive ACLs, effective ordinary-user access, preserved entries and service availability'
    }
    if ($Journey -eq 'Unix') {
        $response = Invoke-WebRequest -Uri ($script:trafficURL + 'proxy/') -Headers @{Host = 'jul516-upstream.test'} -SkipHttpErrorCheck -TimeoutSec 5
        Assert-Status $response 200
        if ($response.Content.Trim() -ne 'Jul CI Unix backend' -or $response.Headers['X-Backend-Host'] -ne 'jul516-upstream.test') { throw 'Unix proxy did not preserve response/Host.' }
        Stop-Process -Id $backendProcess.Id -Force
        $backendProcess = $null
        $failure = Invoke-WebRequest -Uri ($script:trafficURL + 'proxy/') -SkipHttpErrorCheck -TimeoutSec 5
        if ([int]$failure.StatusCode -notin @(502, 503)) { throw 'Unix backend failure did not produce the documented gateway error.' }
        if ($failure.Content.Contains($script:socketPath)) { throw 'Socket path leaked in client error.' }
        $backendProcess = Start-UnixBackend
        $recovered = $false
        for ($attempt = 0; $attempt -lt 30; $attempt++) {
            $response = Invoke-WebRequest -Uri ($script:trafficURL + 'proxy/') -SkipHttpErrorCheck -TimeoutSec 3
            if ([int]$response.StatusCode -eq 200) { $recovered = $true; break }
            Start-Sleep -Milliseconds 500
        }
        if (-not $recovered) { throw 'Unix backend did not recover.' }
        $report.checks.af_unix_service_proxy_host_failure_recovery = 'passed'
        Write-Host 'PASS: AF_UNIX HTTP under the virtual service account, Host preservation and backend failure/recovery'
    }
    if ($Journey -eq 'Mime') {
        $mimeReport = Join-Path $root 'mime-results.json'
        & python (Join-Path $PSScriptRoot 'windows-deployment-mime.py') verify --extension $mimeExtension --url $script:trafficURL --report $mimeReport
        Assert-Native 'Verifying service MIME behavior'
        $report.mime = Get-Content -LiteralPath $mimeReport -Raw | ConvertFrom-Json
        $report.checks.registry_mime_precedence_and_precompressed = 'passed'
    }
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
    if ($backendProcess -and -not $backendProcess.HasExited) { Stop-Process -Id $backendProcess.Id -Force }
    if ($registryCreated) { Remove-Item -LiteralPath $registryPath -Force }
    if ($tsChanged) {
        if ($tsValueExisted) { (Get-Item -LiteralPath $tsPath).SetValue('Content Type', $tsValue, $tsKind) }
        elseif ($tsExisted) { Remove-ItemProperty -LiteralPath $tsPath -Name 'Content Type' }
        else { Remove-Item -LiteralPath $tsPath -Force }
    }
    if (Test-Path $probePath) { Remove-Item -Path $probePath -Force }
    if ($rootCreated -and (Test-Path $root)) {
        Remove-Item -Path $root -Recurse -Force
    }
    if ($permissiveRoot -and (Test-Path -LiteralPath $permissiveRoot)) { Remove-Item -LiteralPath $permissiveRoot -Recurse -Force }
}
if ($ReportPath) {
    $report | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $ReportPath -Encoding utf8NoBOM
}

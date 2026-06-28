# Installs Jul as a Windows service (least-privilege, editable deployment).
# Run from an elevated PowerShell prompt.
#
# Usage:
#   .\install-service.ps1 -BinaryPath 'C:\Program Files\jul\jul.exe' `
#                         -ConfigPath 'C:\ProgramData\jul\server.toml'
#
# The service runs under the per-service virtual account "NT SERVICE\jul", the
# Windows analogue of the systemd unit's unprivileged service user. The data
# directory (config, history, cache, logs) is created and ACL'd so that virtual
# account - and administrators - can write it, while ordinary users cannot.

param(
    [Parameter(Mandatory = $true)] [string] $BinaryPath,
    [Parameter(Mandatory = $true)] [string] $ConfigPath,
    [string] $ServiceName = 'jul',
    [string] $DisplayName = 'Jul HTTP edge server',
    # Root for writable runtime state. Mirrors the systemd layout:
    #   <DataDir>\history  config history snapshots
    #   <DataDir>\cache    HTTP disk cache + ACME certificate cache
    #   <DataDir>\logs     access-log "file" sink output
    [string] $DataDir = 'C:\ProgramData\jul'
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $BinaryPath)) {
    throw "Binary not found: $BinaryPath"
}
if (-not (Test-Path $ConfigPath)) {
    throw "Config not found: $ConfigPath"
}

$serviceAccount = "NT SERVICE\$ServiceName"

# Writable runtime directories, created up front so ACLs can be applied before
# the service starts.
$writableDirs = @(
    $DataDir,
    (Join-Path $DataDir 'history'),
    (Join-Path $DataDir 'cache'),
    (Join-Path $DataDir 'logs')
)
foreach ($dir in $writableDirs) {
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
}

# The service Execute() handler reads --config from the image path arguments.
$binLine = '"{0}" --config "{1}"' -f $BinaryPath, $ConfigPath

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Write-Host "Service '$ServiceName' already exists; updating binary path."
    sc.exe config $ServiceName binPath= $binLine | Out-Null
} else {
    New-Service -Name $ServiceName -DisplayName $DisplayName `
        -BinaryPathName $binLine -StartupType Automatic | Out-Null
    Write-Host "Service '$ServiceName' created."
}

# Run under the per-service virtual account (no password). The account's SID,
# "NT SERVICE\<ServiceName>", exists once the service is registered, so the ACL
# grants below resolve.
sc.exe config $ServiceName obj= $serviceAccount password= "" | Out-Null

# Grant the service account read on the config (it must read it), and modify on
# the writable state directories. The admin console rewrites the config
# atomically via temp-file + rename, which needs write on the *directory* that
# holds it - granted below.
icacls $ConfigPath /grant "$($serviceAccount):(R)" | Out-Null
foreach ($dir in $writableDirs) {
    # (OI)(CI) so the grant is inherited by files and subdirectories; (M) =
    # modify (read/write/delete) but not full control.
    icacls $dir /grant "$($serviceAccount):(OI)(CI)(M)" | Out-Null
}
$configDir = Split-Path -Parent $ConfigPath
icacls $configDir /grant "$($serviceAccount):(OI)(CI)(M)" | Out-Null

# Restart on failure (recover after 5s, twice, then every 60s).
sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/60000 | Out-Null

Write-Host "Done."
Write-Host "  Service account: $serviceAccount"
Write-Host "  Data directory:  $DataDir (history\, cache\, logs\)"
Write-Host "Point acme.cache_dir / the disk cache at '$DataDir\cache', the"
Write-Host "access-log file sink at '$DataDir\logs', and history at '$DataDir\history'."
Write-Host "Start with: Start-Service $ServiceName"

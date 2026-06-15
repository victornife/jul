# Installs Jul as a Windows service.
# Run from an elevated PowerShell prompt.
#
# Usage:
#   .\install-service.ps1 -BinaryPath 'C:\Program Files\jul\jul.exe' `
#                         -ConfigPath 'C:\Program Files\jul\server.toml'

param(
    [Parameter(Mandatory = $true)] [string] $BinaryPath,
    [Parameter(Mandatory = $true)] [string] $ConfigPath,
    [string] $ServiceName = 'jul',
    [string] $DisplayName = 'Jul HTTP edge server'
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $BinaryPath)) {
    throw "Binary not found: $BinaryPath"
}
if (-not (Test-Path $ConfigPath)) {
    throw "Config not found: $ConfigPath"
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

# Restart on failure (recover after 5s, twice, then every 60s).
sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/60000 | Out-Null

Write-Host "Done. Start with: Start-Service $ServiceName"

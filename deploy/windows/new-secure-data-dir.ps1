# Create a dedicated, empty Windows state/config root before placing secrets in
# it. Run elevated; the service installer later grants NT SERVICE\jul access.
param(
    [Parameter(Mandatory = $true)] [string] $Path
)

$ErrorActionPreference = 'Stop'
$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Creating a protected Jul directory requires an elevated PowerShell prompt.'
}
$fullPath = [IO.Path]::GetFullPath($Path)
if (Test-Path -LiteralPath $fullPath) {
    throw "Refusing to change an existing directory: $fullPath"
}
$parent = Split-Path -Parent $fullPath
if (-not (Test-Path -LiteralPath $parent -PathType Container)) {
    throw "Create and review the parent directory first: $parent"
}
New-Item -ItemType Directory -Path $fullPath -ErrorAction Stop | Out-Null

# On a fresh, empty directory remove inherited ACEs, then explicitly grant
# SYSTEM and the built-in Administrators group full control. SID notation is
# locale-independent. Never recurse over an operator's existing directory.
& icacls.exe $fullPath /inheritance:r | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Removing inherited ACLs failed for $fullPath (exit $LASTEXITCODE)" }
& icacls.exe $fullPath /grant:r '*S-1-5-18:(OI)(CI)(F)' '*S-1-5-32-544:(OI)(CI)(F)' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Granting SYSTEM/Administrators failed for $fullPath (exit $LASTEXITCODE)" }
Write-Host "Protected empty Jul directory: $fullPath"
Write-Host 'Place the config inside it, then run install-service.ps1 to grant the service account.'

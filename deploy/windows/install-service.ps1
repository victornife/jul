# Installs Jul as a Windows service (least-privilege, editable deployment).
# Run from an elevated PowerShell prompt.
#
# Usage:
#   .\install-service.ps1 -BinaryPath 'C:\Program Files\jul\jul.exe' `
#                         -ConfigPath 'C:\ProgramData\jul\server.toml' `
#                         -InitializeVersionKey  # first installation only
#
# The service runs under the per-service virtual account "NT SERVICE\jul", the
# Windows analogue of the systemd unit's unprivileged service user. The data
# directory (config, history, cache, logs) receives grants for the virtual
# account. Existing and inherited ACL entries are retained; review effective
# access for ordinary users and administrators before storing secrets there.

param(
    [Parameter(Mandatory = $true)] [string] $BinaryPath,
    [Parameter(Mandatory = $true)] [string] $ConfigPath,
    [string] $ServiceName = 'jul',
    [string] $DisplayName = 'Jul HTTP edge server',
    # Root for writable runtime state. Mirrors the systemd layout:
    #   <DataDir>\history  config history snapshots
    #   <DataDir>\cache    HTTP disk cache + ACME certificate cache
    #   <DataDir>\logs     access-log "file" sink output
    [string] $DataDir = 'C:\ProgramData\jul',
    [string] $VersionKeyFile = '',
    [switch] $InitializeVersionKey
)

$ErrorActionPreference = 'Stop'

# PowerShell's ErrorActionPreference does not turn nonzero native-tool exits
# into exceptions. Stop on failed service registration, ACL or recovery setup.
function Assert-NativeSuccess([string] $Operation) {
    if ($LASTEXITCODE -ne 0) {
        throw "$Operation failed (exit $LASTEXITCODE)"
    }
}

if (-not (Test-Path $BinaryPath)) {
    throw "Binary not found: $BinaryPath"
}
if (-not (Test-Path $ConfigPath)) {
    throw "Config not found: $ConfigPath"
}

$serviceAccount = "NT SERVICE\$ServiceName"
if (-not $VersionKeyFile) { $VersionKeyFile = Join-Path ($DataDir + "-version-private") "effective-version.key" }
$VersionKeyFile = [IO.Path]::GetFullPath($VersionKeyFile)

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
$binLine = '"{0}" --config "{1}" --effective-version-key-file "{2}"' -f $BinaryPath, $ConfigPath, $VersionKeyFile

if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    Write-Host "Service '$ServiceName' already exists; updating binary path."
    sc.exe config $ServiceName binPath= $binLine | Out-Null
    Assert-NativeSuccess 'Updating service binary path'
} else {
    New-Service -Name $ServiceName -DisplayName $DisplayName `
        -BinaryPathName $binLine -StartupType Automatic | Out-Null
    Write-Host "Service '$ServiceName' created."
}

# Run under the per-service virtual account (no password). The account's SID,
# "NT SERVICE\<ServiceName>", exists once the service is registered, so the ACL
# grants below resolve.
# The virtual account has no administrator-supplied password. Passing a blank
# password= argument through PowerShell can be interpreted by sc.exe as an
# invalid account/password combination (1057); omit it entirely.
sc.exe config $ServiceName obj= $serviceAccount | Out-Null
Assert-NativeSuccess 'Setting service account'

# Explicit first-install/upgrade provisioning precedes service Modify grants.
# Missing established state must never be silently recreated on service restart.
if (-not (Test-Path -LiteralPath $VersionKeyFile)) {
    if (-not $InitializeVersionKey) {
        throw "Version key missing at $VersionKeyFile. Restore its protected backup, or explicitly pass -InitializeVersionKey for a first installation/migration."
    }
    & $BinaryPath version-key init --file $VersionKeyFile
    Assert-NativeSuccess 'Provisioning effective-version key'
}
$serviceSid = ([Security.Principal.NTAccount]::new($serviceAccount)).Translate([Security.Principal.SecurityIdentifier])
$adminsSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
$systemSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
$currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$keyDirectory = Split-Path -Parent $VersionKeyFile
foreach ($path in @($keyDirectory, $VersionKeyFile)) {
    $item = Get-Item -LiteralPath $path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Refusing version-key reparse point: $path" }
    $oldAcl = Get-Acl -LiteralPath $path
    $trusted = @($currentSid, $serviceSid.Value, $adminsSid.Value, $systemSid.Value)
    $owner = $oldAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -notin $trusted) { throw "Refusing untrusted existing version-key owner: $path" }
    foreach ($rule in $oldAcl.Access) {
        if ($rule.AccessControlType -eq 'Allow' -and $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -notin $trusted) {
            throw "Refusing previously exposed version-key permissions: $path"
        }
    }
    if ($item.PSIsContainer) {
        $acl = [Security.AccessControl.DirectorySecurity]::new()
        $inheritance = [Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit'
        $serviceRights = [Security.AccessControl.FileSystemRights]::ReadAndExecute
    } else {
        $acl = [Security.AccessControl.FileSecurity]::new()
        $inheritance = [Security.AccessControl.InheritanceFlags]::None
        $serviceRights = [Security.AccessControl.FileSystemRights]::Read
    }
    $acl.SetOwner($adminsSid)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @($systemSid, $adminsSid)) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, 'FullControl', $inheritance, 'None', 'Allow'))
    }
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($serviceSid, $serviceRights, $inheritance, 'None', 'Allow'))
    Set-Acl -LiteralPath $path -AclObject $acl
}
# Existing bytes are never replaced or repaired. The service loads and validates
# them as its actual identity; corrupt state fails startup.

# Grant the service account read on the config (it must read it), and modify on
# the writable state directories. The admin console rewrites the config
# atomically via temp-file + rename, which needs write on the *directory* that
# holds it - granted below.
icacls $ConfigPath /grant "$($serviceAccount):(R)" | Out-Null
Assert-NativeSuccess 'Granting config read access'
foreach ($dir in $writableDirs) {
    # (OI)(CI) so the grant is inherited by files and subdirectories; (M) =
    # modify (read/write/delete) but not full control.
    icacls $dir /grant "$($serviceAccount):(OI)(CI)(M)" | Out-Null
    Assert-NativeSuccess "Granting writable access to $dir"
}
$configDir = Split-Path -Parent $ConfigPath
icacls $configDir /grant "$($serviceAccount):(OI)(CI)(M)" | Out-Null
Assert-NativeSuccess 'Granting config-directory write access'

# Restart on failure (recover after 5s, twice, then every 60s).
sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/60000 | Out-Null
Assert-NativeSuccess 'Configuring service recovery'

Write-Host "Done."
Write-Host "  Service account: $serviceAccount"
Write-Host "  Data directory:  $DataDir (history\, cache\, logs\)"
Write-Host "Point acme.cache_dir / the disk cache at '$DataDir\cache', the"
Write-Host "access-log file sink at '$DataDir\logs', and history at '$DataDir\history'."
Write-Host "Start with: Start-Service $ServiceName"

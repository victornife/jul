<#
.SYNOPSIS
    Cross-compiles Jul.IA for Windows and Linux and packages each build with
    its config and deploy assets into dist/ (plus .zip / .tar.gz archives).

.DESCRIPTION
    Go produces a single static, dependency-free binary per platform, so the
    target machine needs nothing installed — just the binary and a config file.

    Run from the repository root:
        ./scripts/build-release.ps1
        ./scripts/build-release.ps1 -Version 1.2.3

.PARAMETER Version
    Version string stamped into the binary (main.version). Defaults to 0.1.0-dev.

.PARAMETER Targets
    Which os/arch pairs to build. Defaults to the common desktop/server set.
#>
[CmdletBinding()]
param(
    [string]$Version = "0.1.0-dev",
    [string[]]$Targets = @(
        "windows/amd64",
        "windows/arm64",
        "linux/amd64",
        "linux/arm64",
        "darwin/amd64",
        "darwin/arm64"
    )
)

$ErrorActionPreference = "Stop"

# Resolve the repository root (parent of this script's folder).
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $distRoot = Join-Path $root "dist"
    if (Test-Path $distRoot) { Remove-Item $distRoot -Recurse -Force }
    New-Item -ItemType Directory -Path $distRoot | Out-Null

    $ldflags = "-s -w -X main.version=$Version"

    foreach ($target in $Targets) {
        $os, $arch = $target.Split("/")
        $name = "jul-$Version-$os-$arch"
        $stage = Join-Path $distRoot $name
        New-Item -ItemType Directory -Path $stage | Out-Null

        $binName = if ($os -eq "windows") { "jul.exe" } else { "jul" }
        $binPath = Join-Path $stage $binName

        Write-Host "Building $target -> $binPath" -ForegroundColor Cyan

        # CGO is disabled so the binary is fully static and portable.
        $env:GOOS = $os
        $env:GOARCH = $arch
        $env:CGO_ENABLED = "0"
        go build -ldflags $ldflags -o $binPath ./cmd/jul
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $target" }

        # Bundle a sample config and the matching deploy assets.
        Copy-Item (Join-Path $root "server.toml") (Join-Path $stage "server.toml")
        if ($os -eq "windows") {
            Copy-Item (Join-Path $root "deploy/windows/install-service.ps1") $stage -ErrorAction SilentlyContinue
        } else {
            Copy-Item (Join-Path $root "deploy/systemd/jul.service") $stage -ErrorAction SilentlyContinue
        }

        # Create an archive: .zip for Windows, .tar.gz for Linux.
        if ($os -eq "windows") {
            $archive = Join-Path $distRoot "$name.zip"
            Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $archive -Force
        } else {
            $archive = Join-Path $distRoot "$name.tar.gz"
            tar -czf $archive -C $stage .
        }
        Write-Host "Packaged $archive" -ForegroundColor Green
    }

    Write-Host "`nDone. Artifacts in $distRoot" -ForegroundColor Green
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}

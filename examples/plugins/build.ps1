<#
 Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 SPDX-License-Identifier: agpl
#>

# Builds the example plugins. Requires Go 1.26+ with the wasip1/wasm target.
#
#   ./build.ps1                 refresh the committed fixtures in testdata\plugins
#                               (the jul-abi/v2 guests and v1-current-header-inject);
#                               the historical v1 fixtures are never overwritten.
#   ./build.ps1 -Out DIR -All   build every example into DIR under its own name.
param([string]$Out = "", [switch]$All)
$ErrorActionPreference = "Stop"

Push-Location $PSScriptRoot
try {
    if ($Out -eq "") { $Out = Join-Path $PSScriptRoot "..\..\testdata\plugins" }
    New-Item -ItemType Directory -Force -Path $Out | Out-Null

    $env:GOOS = "wasip1"
    $env:GOARCH = "wasm"
    $v2 = @("v2-status-header", "v2-redact", "testguest-v2")
    if ($All) {
        $builds = @("header-inject", "request-block", "kv-counter", "egress-check", "testguest-panic", "testguest-loop") + $v2 | ForEach-Object { @{ Pkg = $_; Name = $_ } }
    } else {
        $builds = @(@{ Pkg = "header-inject"; Name = "v1-current-header-inject" }) + ($v2 | ForEach-Object { @{ Pkg = $_; Name = $_ } })
    }
    foreach ($b in $builds) {
        Write-Host "building $($b.Pkg) -> $($b.Name).wasm"
        go build -trimpath -buildvcs=false -buildmode=c-shared -o (Join-Path $Out "$($b.Name).wasm") "./$($b.Pkg)"
        if ($LASTEXITCODE -ne 0) { throw "build failed: $($b.Pkg)" }
    }
    Write-Host "done -> $Out"
}
finally {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}

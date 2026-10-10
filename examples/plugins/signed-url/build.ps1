<#
 Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 SPDX-License-Identifier: agpl
#>
$ErrorActionPreference = "Stop"
$previousOS, $previousArch = $env:GOOS, $env:GOARCH
Push-Location $PSScriptRoot
try {
    $env:GOOS = "wasip1"
    $env:GOARCH = "wasm"
    go build -trimpath -buildvcs=false -buildmode=c-shared -o signed-url.wasm .
    if ($LASTEXITCODE -ne 0) { throw "signed-url build failed" }
} finally {
    $env:GOOS, $env:GOARCH = $previousOS, $previousArch
    Pop-Location
}

# Builds the example plugins to ..\..\testdata\plugins\<name>.wasm.
# Requires Go 1.26+ with the wasip1/wasm target.
$ErrorActionPreference = "Stop"

Push-Location $PSScriptRoot
try {
    $out = Join-Path $PSScriptRoot "..\..\testdata\plugins"
    New-Item -ItemType Directory -Force -Path $out | Out-Null

    $env:GOOS = "wasip1"
    $env:GOARCH = "wasm"
    foreach ($p in "header-inject", "request-block", "kv-counter", "egress-check", "testguest-panic", "testguest-loop") {
        Write-Host "building $p"
        go build -buildmode=c-shared -o (Join-Path $out "$p.wasm") "./$p"
        if ($LASTEXITCODE -ne 0) { throw "build failed: $p" }
    }
    Write-Host "done -> $out"
}
finally {
    Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}

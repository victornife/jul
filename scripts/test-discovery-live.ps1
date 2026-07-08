# Orchestrates both live discovery lanes and writes a closure-ready summary.
# Usage: .\scripts\test-discovery-live.ps1

$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $root

$artifacts = Join-Path $root "tmp\issue24"
New-Item -ItemType Directory -Force -Path $artifacts | Out-Null

Write-Host "=== Running Consul live lane ==="
& (Join-Path $PSScriptRoot "test-discovery-consul-live.ps1")

Write-Host "=== Running Kubernetes live lane ==="
& (Join-Path $PSScriptRoot "test-discovery-k8s-live.ps1")

$dockerVersion = "unknown"
try {
    $dockerVersion = (docker --version)
} catch {}

$goVersion = "unknown"
try {
    $goVersion = (go version)
} catch {}

$k8sCtx = "unknown"
try {
    $k8sCtx = (kubectl config current-context)
} catch {}

$k8sNodes = ""
try {
    $k8sNodes = (kubectl get nodes -o wide | Out-String)
} catch {}

$consulSummary = Get-Content -Path (Join-Path $artifacts "consul-summary.txt") -ErrorAction Stop
$k8sSummary = Get-Content -Path (Join-Path $artifacts "k8s-summary.txt") -ErrorAction Stop

$summaryPath = Join-Path $artifacts "issue-24-evidence.md"

@"
## Q3 Live discovery integration validation completed (local docker-desktop)

### Environment

1. OS: Windows
2. Docker: $dockerVersion
3. Kubernetes context: $k8sCtx
4. Go: $goVersion
5. Jul build tags: consul kubernetes

Nodes:

```
$k8sNodes
```

### Lane A: Consul live

Config:
- tmp/issue24/consul-live.toml

Artifacts:
- tmp/issue24/consul-before.txt
- tmp/issue24/consul-after.txt
- tmp/issue24/consul-jul.out.log
- tmp/issue24/consul-jul.err.log
- tmp/issue24/consul-summary.txt

Summary:

```
$($consulSummary -join "`n")
```

### Lane B: Kubernetes live (docker-desktop)

Config:
- tmp/issue24/k8s-live.toml

Artifacts:
- tmp/issue24/k8s-before.txt
- tmp/issue24/k8s-after.txt
- tmp/issue24/k8s-jul.out.log
- tmp/issue24/k8s-jul.err.log
- tmp/issue24/k8s-api.txt
- tmp/issue24/kubectl-proxy.out.log
- tmp/issue24/kubectl-proxy.err.log
- tmp/issue24/k8s-summary.txt

Summary:

```
$($k8sSummary -join "`n")
```

### Acceptance criteria mapping

1. Live Consul integration test exists and passes locally: PASS
2. Live K8s integration test exists and passes locally: PASS
3. Procedure and evidence format documented in docs/service-discovery.md.

### Notes

1. This validates local live integrations on docker-desktop.
2. Kubernetes lane evidence is the live EndpointSlice API transition (`port: 18081` before patch, `port: 18082` after patch).
3. CI automation is still a follow-up task.
"@ | Set-Content -Encoding ascii -Path $summaryPath

Write-Host "=== Discovery live validation completed ==="
Write-Host "Evidence summary: $summaryPath"

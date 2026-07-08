# Live integration validation for service discovery (Kubernetes lane)
# Usage: .\scripts\test-discovery-k8s-live.ps1

$ErrorActionPreference = "Stop"

function Invoke-Step {
    param(
        [string]$Name,
        [scriptblock]$Action
    )
    Write-Host "=== $Name ==="
    & $Action
}

$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $root

$artifacts = Join-Path $root "tmp\issue24"
New-Item -ItemType Directory -Force -Path $artifacts | Out-Null

$julPath = Join-Path $root "jul.exe"
Invoke-Step "Building jul.exe with consul+kubernetes tags" {
  go build -tags "consul kubernetes" -o jul.exe ./cmd/jul
}

$ns = "issue24"

Invoke-Step "Preflight checks" {
    kubectl config current-context | Out-File -Encoding ascii -FilePath (Join-Path $artifacts "k8s-context.txt")
    kubectl get nodes -o wide | Out-File -Encoding ascii -FilePath (Join-Path $artifacts "k8s-nodes.txt")
}

Invoke-Step "Freeing Jul test listener port (:29080)" {
  $conns = Get-NetTCPConnection -LocalAddress "127.0.0.1" -LocalPort 29080 -State Listen -ErrorAction SilentlyContinue
  foreach ($conn in $conns) {
    Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue
  }
}

  $script:hostIP = $null
  Invoke-Step "Detecting non-loopback host IPv4 for EndpointSlice" {
    $defaultRoute = Get-NetRoute -AddressFamily IPv4 -DestinationPrefix "0.0.0.0/0" |
      Sort-Object -Property RouteMetric |
      Select-Object -First 1
    if ($defaultRoute) {
      $script:hostIP = Get-NetIPAddress -AddressFamily IPv4 -InterfaceIndex $defaultRoute.InterfaceIndex -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notlike "127.*" } |
        Select-Object -First 1 -ExpandProperty IPAddress
    }
    if (-not $script:hostIP) {
      $script:hostIP = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -notlike "127.*" -and $_.IPAddress -notlike "169.254.*" } |
        Select-Object -First 1 -ExpandProperty IPAddress
    }
    if (-not $script:hostIP) {
      throw "Unable to determine a non-loopback host IPv4 address"
    }
    $script:hostIP | Set-Content -Encoding ascii -Path (Join-Path $artifacts "k8s-host-ip.txt")
  }

$manifestPath = Join-Path $artifacts "k8s-live-manifests.yaml"
@"
apiVersion: v1
kind: Namespace
metadata:
  name: issue24
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: jul-discovery
  namespace: issue24
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: jul-discovery-read
  namespace: issue24
rules:
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: jul-discovery-read
  namespace: issue24
subjects:
- kind: ServiceAccount
  name: jul-discovery
  namespace: issue24
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: jul-discovery-read
---
apiVersion: v1
kind: Service
metadata:
  name: web-k8s
  namespace: issue24
spec:
  ports:
  - name: http
    port: 80
    targetPort: 80
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: web-k8s-manual
  namespace: issue24
  labels:
    kubernetes.io/service-name: web-k8s
addressType: IPv4
ports:
- name: http
  protocol: TCP
  port: 18081
endpoints:
- addresses: ["$hostIP"]
  conditions:
    ready: true
"@ | Set-Content -Encoding ascii -Path $manifestPath

Invoke-Step "Applying Kubernetes resources" {
    kubectl apply -f $manifestPath | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "kubectl apply failed for Kubernetes live manifests"
  }
}

$proxyOut = Join-Path $artifacts "kubectl-proxy.out.log"
$proxyErr = Join-Path $artifacts "kubectl-proxy.err.log"
$proxyProc = $null

Invoke-Step "Starting kubectl proxy (local authenticated API endpoint)" {
  $proxyProc = Start-Process -FilePath "kubectl" -ArgumentList "proxy --port=8001 --address=127.0.0.1 --accept-hosts=.*" -PassThru -NoNewWindow -RedirectStandardOutput $proxyOut -RedirectStandardError $proxyErr
  $ready = $false
  for ($i = 0; $i -lt 30; $i++) {
    try {
      $null = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:8001/version"
      $ready = $true
      break
    } catch {
      Start-Sleep -Milliseconds 300
    }
  }
  if (-not $ready) {
    throw "kubectl proxy did not become ready on :8001"
  }
  "http://127.0.0.1:8001" | Set-Content -Encoding ascii -Path (Join-Path $artifacts "k8s-api.txt")
}

$apiServer = (Get-Content (Join-Path $artifacts "k8s-api.txt") -Raw).Trim()

$cfgPath = Join-Path $artifacts "k8s-live.toml"
@"
[[servers]]
listen = "127.0.0.1:29080"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://webk8s"

[[upstreams]]
name = "webk8s"
strategy = "round_robin"

  [upstreams.discovery]
  type = "kubernetes"
  refresh = "2s"

    [upstreams.discovery.kubernetes]
    namespace = "issue24"
    service = "web-k8s"
    port = "http"
    api_server = "$apiServer"
    insecure_skip_tls_verify = true
"@ | Set-Content -Encoding ascii -Path $cfgPath

Invoke-Step "Jul check" {
    & $julPath check -config $cfgPath
}

$outLog = Join-Path $artifacts "k8s-jul.out.log"
$errLog = Join-Path $artifacts "k8s-jul.err.log"
$before = Join-Path $artifacts "k8s-before.txt"
$after = Join-Path $artifacts "k8s-after.txt"
$proc = $null

try {
    Invoke-Step "Starting Jul with Kubernetes discovery config" {
      $proc = Start-Process -FilePath $julPath -ArgumentList "--config `"$cfgPath`"" -PassThru -NoNewWindow -RedirectStandardOutput $outLog -RedirectStandardError $errLog
    }

    Invoke-Step "Waiting for Jul listener readiness" {
      $ready = $false
      for ($i = 0; $i -lt 30; $i++) {
        try {
          $null = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -SkipHttpErrorCheck "http://127.0.0.1:29080/"
          $ready = $true
          break
        } catch {
          Start-Sleep -Milliseconds 300
        }
      }
      if (-not $ready) {
        throw "Jul did not start listener on :29080"
      }
    }

    Invoke-Step "Collecting pre-change responses and log evidence" {
      $sliceBefore = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:8001/apis/discovery.k8s.io/v1/namespaces/$ns/endpointslices?labelSelector=kubernetes.io%2Fservice-name%3Dweb-k8s"
      $sliceBefore.Content | Set-Content -Encoding ascii -Path $before
      if ($sliceBefore.Content -notmatch '"port"\s*:\s*18081') {
        throw "Expected EndpointSlice port 18081 before patch"
      }
    }

    Invoke-Step "Patching EndpointSlice port from 18081 to 18082" {
        kubectl -n $ns patch endpointslice web-k8s-manual --type merge -p '{"ports":[{"name":"http","protocol":"TCP","port":18082}]}' | Out-Null
      if ($LASTEXITCODE -ne 0) {
        throw "kubectl patch failed for EndpointSlice"
      }
    }

    Invoke-Step "Waiting for EndpointSlice convergence (port 18082 visible via live API)" {
      $converged = $false
      for ($i = 0; $i -lt 30; $i++) {
        $sliceNow = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:8001/apis/discovery.k8s.io/v1/namespaces/$ns/endpointslices?labelSelector=kubernetes.io%2Fservice-name%3Dweb-k8s"
        if ($sliceNow.Content -match '"port"\s*:\s*18082') {
          $converged = $true
          break
        }
        Start-Sleep -Milliseconds 400
      }
      if (-not $converged) {
        throw "EndpointSlice did not converge to port 18082 within timeout"
      }
    }

    Invoke-Step "Collecting post-change responses" {
        $sliceAfter = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "http://127.0.0.1:8001/apis/discovery.k8s.io/v1/namespaces/$ns/endpointslices?labelSelector=kubernetes.io%2Fservice-name%3Dweb-k8s"
        $sliceAfter.Content | Set-Content -Encoding ascii -Path $after
    }

    Invoke-Step "Evaluating Kubernetes lane assertions" {
      $beforeData = Get-Content -Path $before -Raw -ErrorAction Stop
      $afterData = Get-Content -Path $after -Raw -ErrorAction Stop
      $has18081 = $beforeData -match '"port"\s*:\s*18081'
      $has18082 = $afterData -match '"port"\s*:\s*18082'

        $summary = @(
            "k8s_lane=PASS"
        "api_has_18081_before=$has18081"
        "api_has_18082_after=$has18082"
        )

      if (-not $has18081 -or -not $has18082) {
            $summary[0] = "k8s_lane=FAIL"
            $summary += "reason=assertions_failed"
            $summary | Set-Content -Encoding ascii -Path (Join-Path $artifacts "k8s-summary.txt")
        throw "Kubernetes assertions failed. Expected EndpointSlice port transition 18081 -> 18082"
        }

        $summary | Set-Content -Encoding ascii -Path (Join-Path $artifacts "k8s-summary.txt")
    }
}
finally {
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force
    }
  if ($proxyProc -and -not $proxyProc.HasExited) {
    Stop-Process -Id $proxyProc.Id -Force
  }
    kubectl delete namespace $ns --wait=true 2>$null | Out-Null
}

Write-Host "=== Kubernetes live lane PASSED ==="

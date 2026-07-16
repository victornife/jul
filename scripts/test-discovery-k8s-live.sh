#!/usr/bin/env bash
# K8s EndpointSlice live discovery convergence test — Linux/kind lane.
#
# Mirrors scripts/test-discovery-k8s-live.ps1 in spirit but runs on Ubuntu
# via a kind cluster, so it can run in GitHub Actions without Docker Desktop
# or Windows-only networking cmdlets.
#
# What it proves (analogous to the Consul lane):
#   1. Jul discovers endpoints via the Kubernetes EndpointSlice API.
#   2. When an EndpointSlice port is patched (18081 → 18082), the K8s API
#      reflects the change AND jul's admin upstream pool converges live.
#
# Requirements:
#   - kubectl on PATH
#   - kind cluster already created and kubeconfig set (done by the CI workflow)
#   - Jul binary built with -tags consul,kubernetes at $JUL_BIN
#   - Admin listener enabled (the script starts jul with an inline config)
#
# Usage:
#   JUL_BIN=./jul bash scripts/test-discovery-k8s-live.sh
#   CI=true JUL_BIN=./jul bash scripts/test-discovery-k8s-live.sh
set -euo pipefail

JUL_BIN="${JUL_BIN:-./jul}"
NS="issue24"
ARTIFACTS="${ARTIFACTS:-tmp/issue24-k8s}"
HOST_IP="127.0.0.1"
PROXY_PORT=8001
JUL_PORT=29081
ADMIN_PORT=29090
ADMIN_TOKEN="k8s-lane-test-token"

mkdir -p "$ARTIFACTS"

step() { printf '\n=== %s ===\n' "$1"; }
fail() { echo "FAIL: $1"; exit 1; }

cleanup() {
  if [ -n "${JUL_PID:-}" ] && kill -0 "$JUL_PID" 2>/dev/null; then
    kill "$JUL_PID" 2>/dev/null || true
  fi
  if [ -n "${PROXY_PID:-}" ] && kill -0 "$PROXY_PID" 2>/dev/null; then
    kill "$PROXY_PID" 2>/dev/null || true
  fi
  kubectl delete namespace "$NS" --wait=false --ignore-not-found=true 2>/dev/null || true
}
trap cleanup EXIT

step "Applying Kubernetes resources (namespace, RBAC, Service, EndpointSlice)"
kubectl apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $NS
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: jul-discovery
  namespace: $NS
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: jul-discovery-read
  namespace: $NS
rules:
- apiGroups: ["discovery.k8s.io"]
  resources: ["endpointslices"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: jul-discovery-read
  namespace: $NS
subjects:
- kind: ServiceAccount
  name: jul-discovery
  namespace: $NS
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: jul-discovery-read
---
apiVersion: v1
kind: Service
metadata:
  name: web-k8s
  namespace: $NS
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
  namespace: $NS
  labels:
    kubernetes.io/service-name: web-k8s
addressType: IPv4
ports:
- name: http
  protocol: TCP
  port: 18081
endpoints:
- addresses: ["$HOST_IP"]
  conditions:
    ready: true
EOF

step "Creating ServiceAccount token for jul"
SA_TOKEN=$(kubectl create token jul-discovery -n "$NS" --duration=10m)
echo "token_len=${#SA_TOKEN}"

step "Starting kubectl proxy (local authenticated API endpoint)"
kubectl proxy --port="$PROXY_PORT" --address=127.0.0.1 --accept-hosts='.+' \
  > "$ARTIFACTS/kubectl-proxy.out.log" 2>&1 &
PROXY_PID=$!

# Wait for the proxy to become ready.
for i in $(seq 1 30); do
  if curl -s -o /dev/null "http://127.0.0.1:$PROXY_PORT/version"; then
    echo "kubectl proxy ready"
    break
  fi
  sleep 0.3
  [ "$i" -eq 30 ] && fail "kubectl proxy did not become ready on :$PROXY_PORT"
done
API_SERVER="http://127.0.0.1:$PROXY_PORT"

step "Writing jul K8s discovery config"
CFG="$ARTIFACTS/k8s-live.toml"
cat > "$CFG" <<TOML
[[servers]]
listen = "127.0.0.1:$JUL_PORT"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://webk8s"

[admin]
enabled = true
listen = "127.0.0.1:$ADMIN_PORT"
token = "$ADMIN_TOKEN"

[[upstreams]]
name = "webk8s"
strategy = "round_robin"

  [upstreams.discovery]
  type = "kubernetes"
  refresh = "2s"

    [upstreams.discovery.kubernetes]
    namespace = "$NS"
    service = "web-k8s"
    port = "http"
    api_server = "$API_SERVER"
    token = "$SA_TOKEN"
    insecure_skip_tls_verify = true
TOML

step "Jul check"
"$JUL_BIN" check -config "$CFG"

step "Starting jul with Kubernetes discovery config"
"$JUL_BIN" serve -config "$CFG" \
  > "$ARTIFACTS/k8s-jul.out.log" 2>&1 &
JUL_PID=$!

# Wait for the admin listener to become ready.
for i in $(seq 1 30); do
  if curl -s -o /dev/null "http://127.0.0.1:$ADMIN_PORT/healthz"; then
    echo "jul admin listener ready"
    break
  fi
  sleep 0.3
  [ "$i" -eq 30 ] && fail "jul did not start admin listener on :$ADMIN_PORT"
done

step "Collecting pre-change state from Kubernetes API"
SLICE_BEFORE=$(curl -s \
  "http://127.0.0.1:$PROXY_PORT/apis/discovery.k8s.io/v1/namespaces/$NS/endpointslices?labelSelector=kubernetes.io%2Fservice-name%3Dweb-k8s")
echo "$SLICE_BEFORE" > "$ARTIFACTS/k8s-before.txt"
echo "$SLICE_BEFORE" | grep -q '"port":18081' \
  || fail "Expected EndpointSlice port 18081 before patch (got: $(echo "$SLICE_BEFORE" | grep -o '"port":[0-9]*'))"

step "Waiting for jul to discover initial backends (port 18081)"
for i in $(seq 1 30); do
  POOL=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://127.0.0.1:$ADMIN_PORT/api/apps")
  if echo "$POOL" | grep -q '"18081"'; then
    echo "jul pool converged to 18081"
    break
  fi
  sleep 0.5
  [ "$i" -eq 30 ] && fail "jul pool did not reflect port 18081 within 15s. Pool: $POOL"
done

step "Patching EndpointSlice port from 18081 to 18082"
kubectl -n "$NS" patch endpointslice web-k8s-manual \
  --type merge -p '{"ports":[{"name":"http","protocol":"TCP","port":18082}]}'

step "Waiting for EndpointSlice convergence in K8s API (port 18082)"
for i in $(seq 1 30); do
  SLICE_NOW=$(curl -s \
    "http://127.0.0.1:$PROXY_PORT/apis/discovery.k8s.io/v1/namespaces/$NS/endpointslices?labelSelector=kubernetes.io%2Fservice-name%3Dweb-k8s")
  if echo "$SLICE_NOW" | grep -q '"port":18082'; then
    echo "K8s API reflects port 18082"
    break
  fi
  sleep 0.4
  [ "$i" -eq 30 ] && fail "EndpointSlice did not converge to port 18082 within timeout"
done

step "Waiting for jul pool to converge to port 18082"
for i in $(seq 1 30); do
  POOL=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
    "http://127.0.0.1:$ADMIN_PORT/api/apps")
  if echo "$POOL" | grep -q '"18082"'; then
    echo "jul pool converged to 18082"
    echo "$POOL" > "$ARTIFACTS/k8s-after.txt"
    break
  fi
  sleep 0.5
  [ "$i" -eq 30 ] && fail "jul pool did not converge to port 18082 within 15s. Pool: $POOL"
done

step "Assertions"
BEFORE_OK=false
AFTER_OK=false
echo "$SLICE_BEFORE" | grep -q '"port":18081' && BEFORE_OK=true
AFTER_DATA=$(cat "$ARTIFACTS/k8s-after.txt")
echo "$AFTER_DATA" | grep -q '"18082"' && AFTER_OK=true

{
  echo "k8s_lane=$([ "$BEFORE_OK" = true ] && [ "$AFTER_OK" = true ] && echo PASS || echo FAIL)"
  echo "api_has_18081_before=$BEFORE_OK"
  echo "pool_has_18082_after=$AFTER_OK"
} | tee "$ARTIFACTS/k8s-summary.txt"

if [ "$BEFORE_OK" = false ] || [ "$AFTER_OK" = false ]; then
  fail "K8s lane assertions failed (see $ARTIFACTS/k8s-summary.txt)"
fi

echo ""
echo "=== Kubernetes live lane PASSED ==="

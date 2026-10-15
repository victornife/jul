#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
set -euo pipefail
image=quay.io/keycloak/keycloak:26.5.7@sha256:45ae20191531eb608ddb0b775d012b40d3e4f942697f3214694887dd7c108d13
container=jul-oidc-keycloak-test
cleanup() {
 status=$?
 if [ "$status" -ne 0 ]; then
  docker logs "$container" >&2 || true
  docker inspect "$container" --format '{{json .State}}' >&2 || true
 fi
 docker rm -f "$container" >/dev/null 2>&1 || true
 exit "$status"
}
trap cleanup EXIT
docker pull "$image"
docker image inspect "$image" --format '{{json .RepoDigests}}'
docker run -d --name "$container" -p 127.0.0.1:9299:8080 \
 -v "$PWD/testdata/admin-oidc/keycloak-realm.json:/opt/keycloak/data/import/jul-oidc-test-realm.json:ro" \
 "$image" start-dev --import-realm
python3 - <<'PY'
import time,urllib.request
url='http://127.0.0.1:9299/realms/jul-oidc-test/.well-known/openid-configuration'
for attempt in range(90):
 try:
  with urllib.request.urlopen(url,timeout=2) as response:
   if response.status==200:break
 except Exception:pass
 time.sleep(1)
else:raise SystemExit('Keycloak did not become ready')
PY
JUL_KEYCLOAK_CONTAINER="$container" JUL_REQUIRE_KEYCLOAK=1 JUL_KEYCLOAK_ISSUER=http://127.0.0.1:9299/realms/jul-oidc-test \
 go test -race ./internal/adminoidc -run '^TestKeycloakCodePKCE$' -count=1 -v

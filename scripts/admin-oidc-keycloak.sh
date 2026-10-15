#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
set -euo pipefail
image=quay.io/keycloak/keycloak:26.5.7
container=jul-oidc-keycloak-test
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT
docker pull "$image"
docker image inspect "$image" --format '{{json .RepoDigests}}'
docker run --rm -d --name "$container" -p 127.0.0.1:9299:8080 \
 -v "$PWD/testdata/admin-oidc/keycloak-realm.json:/opt/keycloak/data/import/jul-realm.json:ro" \
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

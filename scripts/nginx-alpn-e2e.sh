#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: agpl
set -euo pipefail

# Called by the existing reference lane with its single pinned-image authority.
readonly image="${1:?pinned reference image required}"
readonly artifacts="${2:?artifact directory required}"
mkdir -p "${artifacts}"
readonly fixture="${PWD}/testdata/nginx-corpus/stream-alpn-boundary/nginx/reference.conf"
readonly name="jul-alpn-${RANDOM}-$$"
readonly network="${name}-net"
network_created=0
container_started=0
cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ "${container_started}" == 1 ]]; then
    docker logs "${name}" >"${artifacts}/nginx.log" 2>&1 || true
    docker inspect "${name}" >"${artifacts}/container-inspect.json" 2>/dev/null || true
    if [[ "${status}" != 0 ]]; then tail -n 60 "${artifacts}/nginx.log" >&2 || true; fi
    docker rm -f "${name}" >/dev/null 2>&1 || true
  fi
  if [[ "${network_created}" == 1 ]]; then docker network rm "${network}" >/dev/null 2>&1 || true; fi
  exit "${status}"
}
trap cleanup EXIT INT TERM
docker run --rm --network none --entrypoint nginx "${image}" -V 2>&1 | tee "${artifacts}/nginx-build.txt"
docker run --rm --network none --entrypoint nginx \
  -v "${fixture}:/etc/nginx/nginx.conf:ro" "${image}" -t 2>&1 | tee "${artifacts}/config-check.txt"
docker network create --internal "${network}" >/dev/null
network_created=1
docker run --detach --name "${name}" --network "${network}" \
  --read-only --user 101:101 --cap-drop ALL --security-opt no-new-privileges \
  --pids-limit 128 --memory 128m --cpus 1 \
  --tmpfs /var/cache/nginx:rw,noexec,nosuid,size=16m,uid=101,gid=101,mode=0700 \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m,uid=101,gid=101,mode=0700 \
  -v "${fixture}:/etc/nginx/nginx.conf:ro" --entrypoint /usr/sbin/nginx \
  "${image}" -g 'daemon off;' >"${artifacts}/container-id.txt"
container_started=1
container_ip="$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${name}")"
if [[ ! "${container_ip}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "unexpected internal container address" >&2
  exit 1
fi
endpoint() { printf '%s:%s\n' "${container_ip}" "$1"; }
JUL_ALPN_NGINX_ADDR="$(endpoint 18443)"
JUL_ALPN_BACKEND_A="$(endpoint 18001)"
JUL_ALPN_BACKEND_B="$(endpoint 18002)"
JUL_ALPN_BACKEND_F="$(endpoint 18003)"
export JUL_ALPN_NGINX_ADDR JUL_ALPN_BACKEND_A JUL_ALPN_BACKEND_B JUL_ALPN_BACKEND_F
python3 - <<'PY'
import os, socket, time
host, port = os.environ['JUL_ALPN_BACKEND_A'].rsplit(':', 1)
for attempt in range(100):
    try:
        with socket.create_connection((host, int(port)), timeout=1) as sock:
            assert sock.recv(1) == b'A'
        break
    except (OSError, AssertionError):
        if attempt == 99: raise
        time.sleep(0.1)
PY
go test -tags stream ./internal/stream -run '^TestALPNNGINXReference$' -count=1 -v | tee "${artifacts}/alpn-reference.txt"

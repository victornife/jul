#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: AGPL-3.0-only

set -euo pipefail

readonly NGINX_IMAGE="docker.io/library/nginx:1.28.3-alpine@sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236"
readonly FIXTURE_ID="core-multifile-return"
readonly FIXTURE_DIR="${PWD}/testdata/nginx-corpus/${FIXTURE_ID}"
readonly CONTAINER_PORT="18080"
readonly ARTIFACT_DIR="${NGINX_CORPUS_ARTIFACT_DIR:-${PWD}/tmp/nginx-migration-e2e}"
readonly REQUIRED="${REQUIRE_NGINX_E2E:-0}"

mkdir -p "${ARTIFACT_DIR}"

for dependency in docker python3; do
	if command -v "${dependency}" >/dev/null 2>&1; then
		continue
	fi
	message="not_executed: ${dependency} is unavailable; pinned NGINX reference lane skipped"
	echo "${message}" | tee "${ARTIFACT_DIR}/result.txt"
	if [[ "${REQUIRED}" == "1" ]]; then
		exit 1
	fi
	exit 0
done

container_name="jul-nginx-corpus-${RANDOM}-$$"
network_name="jul-nginx-corpus-net-${RANDOM}-$$"
proxy_port_file="$(mktemp)"
container_started=0
network_created=0
proxy_pid=0

cleanup() {
	status=$?
	trap - EXIT INT TERM
	if [[ "${proxy_pid}" != "0" ]]; then
		kill "${proxy_pid}" >/dev/null 2>&1 || true
		wait "${proxy_pid}" >/dev/null 2>&1 || true
	fi
	rm -f "${proxy_port_file}"
	if [[ "${container_started}" == "1" ]]; then
		docker logs "${container_name}" >"${ARTIFACT_DIR}/nginx.log" 2>&1 || true
		docker inspect "${container_name}" >"${ARTIFACT_DIR}/container-inspect.json" 2>/dev/null || true
		docker rm -f "${container_name}" >/dev/null 2>&1 || true
	fi
	if [[ "${network_created}" == "1" ]]; then
		docker network rm "${network_name}" >/dev/null 2>&1 || true
	fi
	if [[ "${status}" != "0" ]]; then
		echo "unexpected_difference: pinned NGINX reference lane failed" | tee "${ARTIFACT_DIR}/result.txt"
	fi
	exit "${status}"
}
trap cleanup EXIT INT TERM

if [[ ! -f "${FIXTURE_DIR}/manifest.json" || ! -f "${FIXTURE_DIR}/nginx/nginx.conf" ]]; then
	echo "fixture ${FIXTURE_ID} is incomplete" >&2
	exit 1
fi

docker pull "${NGINX_IMAGE}"
docker image inspect "${NGINX_IMAGE}" >"${ARTIFACT_DIR}/image-inspect.json"

docker network create --internal "${network_name}" >/dev/null
network_created=1

docker run --detach \
	--name "${container_name}" \
	--network "${network_name}" \
	--read-only \
	--user 101:101 \
	--cap-drop ALL \
	--security-opt no-new-privileges \
	--pids-limit 128 \
	--memory 128m \
	--cpus 1 \
	--tmpfs /var/cache/nginx:rw,noexec,nosuid,size=16m,uid=101,gid=101,mode=0700 \
	--tmpfs /var/run:rw,noexec,nosuid,size=1m,uid=101,gid=101,mode=0700 \
	--tmpfs /tmp:rw,noexec,nosuid,size=16m,uid=101,gid=101,mode=0700 \
	--volume "${FIXTURE_DIR}/nginx:/etc/nginx:ro" \
	--entrypoint /usr/sbin/nginx \
	"${NGINX_IMAGE}" \
	-g 'daemon off;' >/dev/null
container_started=1

container_ip="$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${container_name}")"
if [[ ! "${container_ip}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "unexpected internal container address" >&2
	exit 1
fi

python3 - "${container_ip}" "${CONTAINER_PORT}" "${proxy_port_file}" <<'PYBRIDGE' &
import socket
import sys
import threading

target = (sys.argv[1], int(sys.argv[2]))
port_file = sys.argv[3]
listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", 0))
listener.listen(32)
with open(port_file, "w", encoding="utf-8") as handle:
    handle.write(str(listener.getsockname()[1]))
    handle.flush()

def pump(source, destination):
    try:
        while True:
            data = source.recv(65536)
            if not data:
                try:
                    destination.shutdown(socket.SHUT_WR)
                except OSError:
                    pass
                return
            destination.sendall(data)
    except OSError:
        return

def bridge(client):
    try:
        upstream = socket.create_connection(target, timeout=5)
    except OSError:
        client.close()
        return
    threading.Thread(target=pump, args=(client, upstream), daemon=True).start()
    threading.Thread(target=pump, args=(upstream, client), daemon=True).start()

while True:
    client, _ = listener.accept()
    threading.Thread(target=bridge, args=(client,), daemon=True).start()
PYBRIDGE
proxy_pid=$!

for _ in $(seq 1 100); do
	if [[ -s "${proxy_port_file}" ]]; then
		break
	fi
	if ! kill -0 "${proxy_pid}" >/dev/null 2>&1; then
		echo "loopback proxy exited before publishing its port" >&2
		exit 1
	fi
	sleep 0.05
done
if [[ ! -s "${proxy_port_file}" ]]; then
	echo "loopback proxy did not publish a port" >&2
	exit 1
fi
host_port="$(cat "${proxy_port_file}")"
if [[ ! "${host_port}" =~ ^[0-9]+$ ]] || (( host_port < 1 || host_port > 65535 )); then
	echo "invalid loopback proxy port" >&2
	exit 1
fi

NGINX_CORPUS_FIXTURE_DIR="${FIXTURE_DIR}" \
NGINX_CORPUS_BASE_URL="http://127.0.0.1:${host_port}" \
	go test -tags importer ./internal/migrate/nginx/corpus \
		-run '^TestNGINXCorpusReferenceRuntime$' \
		-count=1 \
		-v

echo "expected_difference: ${FIXTURE_ID}/relative-redirect NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT" | tee "${ARTIFACT_DIR}/result.txt"

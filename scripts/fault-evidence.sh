#!/usr/bin/env bash
# Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
# SPDX-License-Identifier: AGPL-3.0-only
#
# Focused real-host fault evidence for #422. One profile per run, each on a
# fresh Jul process, each writing a self-contained evidence directory:
#
#   scripts/fault-evidence.sh dns    # isolated resolver in a user+net+mount namespace
#   scripts/fault-evidence.sh fd     # fresh process under a lowered RLIMIT_NOFILE
#   scripts/fault-evidence.sh cpu    # cgroup v2 CPUQuota via systemd-run --user --scope
#   scripts/fault-evidence.sh mem    # cgroup v2 MemoryHigh/MemoryMax via systemd-run --user --scope
#   scripts/fault-evidence.sh disk   # cache/log/data on a size-bounded tmpfs in a user+mount namespace
#
# Nothing here touches host-wide DNS, the host filesystem's free space, or the
# host's limits: DNS runs on 127.0.0.1:53 of a private network namespace, the
# disk profile fills only its own tmpfs, and limits apply to the Jul process
# (or its transient scope) alone.
#
# Every run records the exact Jul SHA, the config, the workload, the limits,
# a timestamped event log, per-second client results, a retained <=15s metrics
# series (scripts/soak-scrape.go) with a checksummed manifest, process/cgroup
# snapshots, and a final quiescence check.
#
# Environment:
#   FAULT_OUT      evidence directory (default soak-artifacts/<date>-fault-<profile>)
#   FAULT_SCALE    multiplies phase durations (default 1)
set -euo pipefail

PROFILE="${1:-}"
case "${PROFILE}" in
dns | fd | cpu | mem | disk) ;;
__inner_dns | __inner_disk) ;;
*)
	echo "usage: scripts/fault-evidence.sh dns|fd|cpu|mem|disk" >&2
	exit 2
	;;
esac

ROOT="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
cd "${ROOT}"
FULL_TAGS="${FULL_TAGS:-brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf}"
SCALE="${FAULT_SCALE:-1}"
BIN="${FAULT_BIN_DIR:-${ROOT}/tmp/fault-bin}"
ADMIN_TOKEN="fault-evidence-token-0123456789abcdef"
ADMIN="127.0.0.1:19901"
MAIN="127.0.0.1:19080"

secs() { awk -v s="$1" -v k="${SCALE}" 'BEGIN { printf "%d", s * k }'; }

build() {
	mkdir -p "${BIN}"
	go build -tags "${FULL_TAGS}" -o "${BIN}/jul" ./cmd/jul
	for tool in soak-scrape fault-load fault-backend fault-dnsd; do
		go build -o "${BIN}/${tool}" "scripts/${tool}.go"
	done
}

# ---- shared helpers (also used inside namespaces) ---------------------------

ev() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)" "$*" | tee -a "${OUT}/events.log"; }

metric() { # metric <name-regex>: current value(s) from /metrics
	curl -fsS --max-time 3 -H "Authorization: Bearer ${ADMIN_TOKEN}" "http://${ADMIN}/metrics" 2>/dev/null | grep -E "^($1)( |\{)" || true
}

snapshot() { # snapshot <label>
	local label="$1" f="${OUT}/snapshots/$1.txt"
	mkdir -p "${OUT}/snapshots"
	{
		echo "# ${label} $(date -u +%Y-%m-%dT%H:%M:%SZ)"
		if [[ -n "${JUL_PID:-}" && -d "/proc/${JUL_PID}" ]]; then
			echo "pid ${JUL_PID} alive"
			grep -E '^(VmRSS|VmHWM|Threads)' "/proc/${JUL_PID}/status" || true
			echo "fds $(find "/proc/${JUL_PID}/fd" -mindepth 1 -maxdepth 1 2>/dev/null | wc -l)"
			grep -E 'Max open files' "/proc/${JUL_PID}/limits" || true
			local cg
			cg="$(cut -d: -f3 "/proc/${JUL_PID}/cgroup" 2>/dev/null || true)"
			for k in cpu.max cpu.stat memory.max memory.high memory.current memory.peak memory.events; do
				[[ -r "/sys/fs/cgroup${cg}/${k}" ]] && echo "cgroup ${k}: $(tr '\n' ' ' <"/sys/fs/cgroup${cg}/${k}")"
			done
		else
			echo "pid ${JUL_PID:-?} NOT RUNNING"
		fi
		metric 'go_goroutines|process_open_fds|process_max_fds|process_resident_memory_bytes|go_memstats_heap_inuse_bytes|jul_listener_conns|jul_upstream_backends|jul_upstream_active_requests|jul_upstream_connections|jul_cache_bytes|jul_cache_entries|jul_cache_max_bytes|jul_cache_evictions_total|jul_discovery_errors_total|jul_reload_total'
	} >"${f}" 2>&1
	ev "snapshot ${label}: $(grep -E '^(fds|VmRSS|pid)' "${f}" | tr '\n' ' ')"
}

wait_http() { # wait_http <url> <seconds>
	local i
	for ((i = 0; i < $2 * 10; i++)); do
		curl -fsS -o /dev/null --max-time 1 -H "Authorization: Bearer ${ADMIN_TOKEN}" "$1" 2>/dev/null && return 0
		sleep 0.1
	done
	return 1
}

start_scrape() {
	SOAK_SCRAPE_BEARER="${ADMIN_TOKEN}" "${BIN}/soak-scrape" -url "http://${ADMIN}/metrics" -out "${OUT}/metrics" -interval 5s \
		-label "jul_sha=${JUL_SHA}" -label "profile=${PROFILE#__inner_}" -label "config=jul.toml" \
		-label "workload=${WORKLOAD:-see MANIFEST.md}" >"${OUT}/scrape.stderr" 2>&1 &
	SCRAPE_PID=$!
}

stop_scrape() {
	[[ -n "${SCRAPE_PID:-}" ]] || return 0
	kill -TERM "${SCRAPE_PID}" 2>/dev/null || true
	wait "${SCRAPE_PID}" 2>/dev/null || true
	"${BIN}/soak-scrape" -summarize "${OUT}/metrics" >"${OUT}/metrics-summary.md" || true
}

load() { # load <name> <seconds> <fault-load args...>
	local name="$1" dur="$2"
	shift 2
	"${BIN}/fault-load" -duration "${dur}s" -out "${OUT}/load-${name}.jsonl" "$@" 2>>"${OUT}/load.stderr"
	ev "load ${name}: $(tail -n1 "${OUT}/load-${name}.jsonl")"
}

start_jul() { # start_jul [prefix command...]
	"$@" "${BIN}/jul" -config "${OUT}/jul.toml" >>"${OUT}/jul.log" 2>&1 &
	JUL_PID=$!
	if ! wait_http "http://${ADMIN}/metrics" 20; then
		ev "Jul did not become ready"
		tail -n 40 "${OUT}/jul.log" >&2
		exit 1
	fi
	ev "jul started pid=${JUL_PID}"
}

stop_jul() {
	[[ -n "${JUL_PID:-}" ]] || return 0
	local t0 t1
	t0=$(date +%s%N)
	kill -TERM "${JUL_PID}" 2>/dev/null || true
	wait "${JUL_PID}" 2>/dev/null || true
	t1=$(date +%s%N)
	ev "jul stopped after SIGTERM in $(((t1 - t0) / 1000000)) ms"
	JUL_PID=""
}

quiescence() { # after load: compare with the idle baseline snapshot
	sleep "$(secs 15)"
	snapshot quiescent
}

write_config() { # write_config <extra TOML appended>
	cat >"${OUT}/jul.toml" <<EOF
# Generated by scripts/fault-evidence.sh (${PROFILE#__inner_}) for #422.
[global]
log_level = "warn"

[admin]
enabled = true
listen = "${ADMIN}"
token = "${ADMIN_TOKEN}"

[[servers]]
listen = "${MAIN}"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://app"
${1}
EOF
	"${BIN}/jul" check -config "${OUT}/jul.toml" >>"${OUT}/events.log"
}

manifest() { # manifest <limits> <workload> <result>
	cat >"${OUT}/MANIFEST.md" <<EOF
# Fault evidence: ${PROFILE#__inner_} (#422)

| Field | Value |
| --- | --- |
| Profile | \`${PROFILE#__inner_}\` |
| Date (UTC) | $(date -u +%Y-%m-%dT%H:%M:%SZ) |
| Jul SHA | \`${JUL_SHA}\` |
| Harness SHA | \`${HARNESS_SHA}\` |
| Build | \`go build -tags "${FULL_TAGS}"\`; $(go version) |
| Host | $(uname -srm); $(nproc) CPUs; $(awk '/MemTotal/ {printf "%d MiB", $2/1024}' /proc/meminfo) |
| Isolation | ${ISOLATION} |
| Limits | ${1} |
| Workload | ${2} |
| Config | [jul.toml](jul.toml) |
| Metrics | [metrics/samples.jsonl.gz](metrics/samples.jsonl.gz) (5s interval), [metrics/metrics-manifest.json](metrics/metrics-manifest.json), [summary](metrics-summary.md) |
| Client results | \`load-*.jsonl\` (one line per second) |
| Events | [events.log](events.log) |
| Snapshots | [snapshots/](snapshots/) |
| Reproduce | \`scripts/fault-evidence.sh ${PROFILE#__inner_}\` |

## Result

${3}
EOF
	(cd "${OUT}" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
}

prepare_out() {
	OUT="${FAULT_OUT:-${ROOT}/soak-artifacts/$(date -u +%Y-%m-%d)-fault-${PROFILE}}"
	if [[ -e "${OUT}" ]]; then
		echo "refusing to overwrite ${OUT}" >&2
		exit 1
	fi
	mkdir -p "${OUT}"
	export OUT
	JUL_SHA="$(git rev-parse HEAD)$(git diff --quiet HEAD -- . ':!soak-artifacts' || echo ' (dirty)')"
	HARNESS_SHA="$(git log -1 --format=%H -- scripts/fault-evidence.sh 2>/dev/null || echo unknown)"
	export JUL_SHA HARNESS_SHA
}

cleanup() {
	stop_scrape || true
	[[ -n "${LOAD_PID:-}" ]] && kill "${LOAD_PID}" 2>/dev/null
	[[ -n "${JUL_PID:-}" ]] && kill -KILL "${JUL_PID}" 2>/dev/null
	[[ -n "${BACKEND_PID:-}" ]] && kill "${BACKEND_PID}" 2>/dev/null
	[[ -n "${DNS_PID:-}" ]] && kill "${DNS_PID}" 2>/dev/null
	return 0
}

start_backends() { # start_backends addr...
	local args=()
	for a in "$@"; do args+=(-listen "$a"); done
	"${BIN}/fault-backend" "${args[@]}" >>"${OUT}/backend.log" 2>&1 &
	BACKEND_PID=$!
	for a in "$@"; do wait_http "http://${a}/" 10 || {
		echo "backend ${a} not ready" >&2
		exit 1
	}; done
}

# ---- profiles ----------------------------------------------------------------

profile_dns_inner() {
	ISOLATION="unshare --user --map-root-user --net --mount: private loopback, /etc/resolv.conf bind-mounted to nameserver 127.0.0.1 (fault-dnsd); the host resolver is never touched"
	ip link set lo up
	printf 'nameserver 127.0.0.1\noptions timeout:1 attempts:1\n' >"${OUT}/resolv.conf"
	mount --bind "${OUT}/resolv.conf" /etc/resolv.conf
	local state="${OUT}/dns.state"
	echo "ok 127.0.0.2,127.0.0.3" >"${state}"
	"${BIN}/fault-dnsd" -listen 127.0.0.1:53 -name jul-decoy.test -state "${state}" -log "${OUT}/dns-queries.log" -ttl 5 >>"${OUT}/dnsd.log" 2>&1 &
	DNS_PID=$!
	start_backends 127.0.0.2:19181 127.0.0.3:19181 127.0.0.4:19181
	sleep 0.3
	WORKLOAD="fault-load 16 workers keep-alive GET / through a dns-discovered upstream (refresh 5s)"
	write_config '
[[upstreams]]
name = "app"
strategy = "round_robin"
  [upstreams.discovery]
  type = "dns"
  target = "jul-decoy.test:19181"
  refresh = "5s"
'
	start_jul
	start_scrape
	local phase=$(secs 30)
	"${BIN}/fault-load" -url "http://${MAIN}/" -workers 16 -duration 0 -out "${OUT}/load-continuous.jsonl" 2>>"${OUT}/load.stderr" &
	LOAD_PID=$!
	ev "phase healthy: dns answers 127.0.0.2,127.0.0.3"
	sleep "${phase}"
	snapshot healthy
	echo "ok 127.0.0.2,127.0.0.3,127.0.0.4" >"${state}"
	ev "phase membership-grow: dns answers 127.0.0.2-4"
	local t0=$SECONDS
	until metric jul_upstream_backends | grep -q ' 3$'; do sleep 0.5; ((SECONDS - t0 > 60)) && break; done
	ev "membership-grow observed after $((SECONDS - t0))s (jul_upstream_backends=$(metric jul_upstream_backends | awk '{print $2}'))"
	sleep "${phase}"
	for mode in servfail drop nxdomain; do
		echo "${mode}" >"${state}"
		ev "phase failure-${mode}: resolver returns ${mode}"
		sleep "${phase}"
		snapshot "failure-${mode}"
		ev "during ${mode}: jul_discovery_errors_total=$(metric jul_discovery_errors_total | awk '{print $2}') jul_upstream_backends=$(metric jul_upstream_backends | awk '{print $2}')"
	done
	echo "ok 127.0.0.2" >"${state}"
	ev "phase recovery: dns answers 127.0.0.2 only"
	t0=$SECONDS
	until metric jul_upstream_backends | grep -q ' 1$'; do sleep 0.5; ((SECONDS - t0 > 60)) && break; done
	ev "recovery observed after $((SECONDS - t0))s (jul_upstream_backends=$(metric jul_upstream_backends | awk '{print $2}'))"
	sleep "${phase}"
	kill -TERM "${LOAD_PID}"
	wait "${LOAD_PID}" || true
	LOAD_PID=""
	ev "load stopped: $(tail -n1 "${OUT}/load-continuous.jsonl")"
	quiescence
	stop_scrape
	stop_jul
	ev "dns queries total $(grep -c . "${OUT}/dns-queries.log" || true)"
	awk '{print $3}' "${OUT}/dns-queries.log" | sort | uniq -c | while read -r n m; do ev "dns queries in mode ${m}: ${n}"; done
	ev "last-good log lines $(grep -c 'keeping last-good backends' "${OUT}/jul.log" || true)"
	grep -oE 'error="[^"]*"' "${OUT}/jul.log" | sed -E 's/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+/<addr>/g' | sort | uniq -c | sort -rn >"${OUT}/discovery-errors.txt" || true
	manifest "none on Jul; resolver failure injected by fault-dnsd modes servfail/drop/nxdomain for $(secs 30)s each" \
		"${WORKLOAD}" "See events.log, discovery-errors.txt, dns-queries.log and metrics-summary.md; analysis in docs/soak-evidence.md."
}

profile_fd() {
	ISOLATION="fresh Jul process under prlimit --nofile=256:256; backends and load generator unconstrained"
	start_backends 127.0.0.1:19181 127.0.0.1:19182
	WORKLOAD="baseline 16 workers; pressure: 400 held idle TCP connections + 64 workers with fresh connections per request; recovery 16 workers"
	write_config '
[[upstreams]]
name = "app"
strategy = "round_robin"
servers = ["127.0.0.1:19181", "127.0.0.1:19182"]
'
	start_jul prlimit --nofile=256:256 --
	ev "limits: $(grep 'Max open files' "/proc/${JUL_PID}/limits")"
	start_scrape
	snapshot idle
	load baseline "$(secs 20)" -url "http://${MAIN}/" -workers 16
	snapshot after-baseline
	ev "phase pressure: holding 400 idle connections and 64 fresh-connection workers"
	"${BIN}/fault-load" -url "http://${MAIN}/" -workers 64 -fresh -hold 400 -duration "$(secs 40)s" -out "${OUT}/load-pressure.jsonl" 2>>"${OUT}/load.stderr" &
	LOAD_PID=$!
	sleep "$(secs 20)"
	snapshot under-pressure
	wait "${LOAD_PID}" || true
	LOAD_PID=""
	ev "load pressure: $(tail -n1 "${OUT}/load-pressure.jsonl")"
	load recovery "$(secs 20)" -url "http://${MAIN}/" -workers 16
	quiescence
	stop_scrape
	ev "jul log lines mentioning EMFILE: $(grep -ciE 'too many open files' "${OUT}/jul.log" || true)"
	grep -iE 'too many open files' "${OUT}/jul.log" | sed -E 's/[0-9]{4}-[0-9-]+T[^ ]+//; s/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+/<addr>/g' | cut -c1-160 | sort | uniq -c | sort -rn | head -20 >"${OUT}/emfile-log-kinds.txt" || true
	stop_jul
	manifest "RLIMIT_NOFILE 256 (soft and hard) on the Jul process only" "${WORKLOAD}" "See events.log, emfile-log-kinds.txt, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md."
}

scope_prop() { systemctl --user show "${SCOPE_UNIT}" -p "$1" --value 2>/dev/null; }

profile_cpu() {
	ISOLATION="transient systemd --user scope (cgroup v2) holding only the Jul process"
	start_backends 127.0.0.1:19181 127.0.0.1:19182
	WORKLOAD="64 workers keep-alive GET /slow?ms=5 (unconstrained baseline, then CPUQuota=20%, then quota lifted live); SIGHUP reload under quota; SIGTERM shutdown timed"
	write_config '
[[upstreams]]
name = "app"
strategy = "round_robin"
servers = ["127.0.0.1:19181", "127.0.0.1:19182"]
  [upstreams.health_check]
  enabled = true
  type = "http"
  path = "/healthz"
  interval = "2s"
  timeout = "1s"
'
	SCOPE_UNIT="jul-fault-cpu-$$.scope"
	start_jul systemd-run --user --scope --quiet --unit "${SCOPE_UNIT}" -p CPUAccounting=yes --
	start_scrape
	snapshot idle
	load unconstrained "$(secs 30)" -url "http://${MAIN}/slow?ms=5" -workers 64
	systemctl --user set-property --runtime "${SCOPE_UNIT}" CPUQuota=20%
	ev "applied CPUQuota=20% ($(scope_prop CPUQuotaPerSecUSec))"
	"${BIN}/fault-load" -url "http://${MAIN}/slow?ms=5" -workers 64 -duration "$(secs 60)s" -out "${OUT}/load-quota20.jsonl" 2>>"${OUT}/load.stderr" &
	LOAD_PID=$!
	sleep "$(secs 20)"
	snapshot under-quota
	local r0 t0 t1
	r0="$(metric 'jul_reload_total' | awk '{s+=$2} END {print s+0}')"
	t0=$(date +%s%N)
	kill -HUP "${JUL_PID}"
	until [[ "$(metric 'jul_reload_total' | awk '{s+=$2} END {print s+0}')" != "${r0}" ]]; do sleep 0.1; (($(date +%s%N) - t0 > 30000000000)) && break; done
	t1=$(date +%s%N)
	ev "SIGHUP reload under quota observed in $(((t1 - t0) / 1000000)) ms: $(metric 'jul_reload_total' | tr '\n' ' ')"
	ev "health under quota: $(metric 'jul_upstream_backends_healthy|jul_upstream_probes_total' | tr '\n' ' ')"
	wait "${LOAD_PID}" || true
	LOAD_PID=""
	ev "load quota20: $(tail -n1 "${OUT}/load-quota20.jsonl")"
	systemctl --user set-property --runtime "${SCOPE_UNIT}" CPUQuota=
	ev "lifted CPUQuota live ($(scope_prop CPUQuotaPerSecUSec))"
	load recovered "$(secs 30)" -url "http://${MAIN}/slow?ms=5" -workers 64
	quiescence
	systemctl --user set-property --runtime "${SCOPE_UNIT}" CPUQuota=20%
	ev "re-applied CPUQuota=20% for a shutdown-under-quota measurement"
	stop_scrape
	stop_jul
	manifest "cgroup v2 cpu.max 20000/100000 (CPUQuota=20%) during the pressure phase and at shutdown" "${WORKLOAD}" "See events.log, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md."
}

profile_mem() {
	ISOLATION="transient systemd --user scope (cgroup v2) holding only the Jul process; limits changed live with systemctl --user set-property"
	start_backends 127.0.0.1:19181 127.0.0.1:19182
	WORKLOAD="memory cache 64 MB cap; 48 workers filling unique 256 KiB cacheable objects under MemoryHigh=176M, then cache-hit re-reads under MemoryHigh lowered live to 96M (below the working set), then MemoryHigh restored; MemoryMax=192M throughout, swap 0"
	write_config '
[cache]
enabled = true
memory_max_size = "64MB"
default_ttl = "10m"

[[upstreams]]
name = "app"
strategy = "round_robin"
servers = ["127.0.0.1:19181", "127.0.0.1:19182"]
'
	sed -i 's#^  proxy_pass = "http://app"#  proxy_pass = "http://app"\n  cache = true#' "${OUT}/jul.toml"
	"${BIN}/jul" check -config "${OUT}/jul.toml" >>"${OUT}/events.log"
	SCOPE_UNIT="jul-fault-mem-$$.scope"
	start_jul systemd-run --user --scope --quiet --unit "${SCOPE_UNIT}" -p MemoryAccounting=yes -p MemoryHigh=176M -p MemoryMax=192M -p MemorySwapMax=0 --
	start_scrape
	snapshot idle
	load fill "$(secs 60)" -url "http://${MAIN}/blob?kb=256" -unique-paths -workers 48
	snapshot after-fill
	load reread-unconstrained "$(secs 20)" -url "http://${MAIN}/blob?kb=256&n=1" -workers 48
	systemctl --user set-property --runtime "${SCOPE_UNIT}" MemoryHigh=96M
	ev "lowered MemoryHigh to 96M live ($(scope_prop MemoryHigh))"
	load reread-throttled "$(secs 40)" -url "http://${MAIN}/blob?kb=256&n=1" -workers 48
	snapshot throttled
	systemctl --user set-property --runtime "${SCOPE_UNIT}" MemoryHigh=176M
	ev "restored MemoryHigh to 176M live ($(scope_prop MemoryHigh))"
	load reread-restored "$(secs 20)" -url "http://${MAIN}/blob?kb=256&n=1" -workers 48
	snapshot restored
	quiescence
	stop_scrape
	stop_jul
	manifest "cgroup v2 memory.max=192M, swap.max=0; memory.high 176M, lowered live to 96M for the throttled phase" "${WORKLOAD}" "See events.log, snapshots (memory.events), load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md."
}

profile_disk_inner() {
	ISOLATION="unshare --user --map-root-user --mount: a private 48 MiB tmpfs holds the disk cache tier, the access and audit logs, the managed config file and its history; the host filesystem is never filled"
	local fs="${OUT}/fs"
	mkdir -p "${fs}"
	mount -t tmpfs -o size=48m,mode=0755 tmpfs "${fs}"
	mkdir -p "${fs}/cache" "${fs}/logs" "${fs}/data/history"
	start_backends 127.0.0.1:19181 127.0.0.1:19182
	WORKLOAD="disk cache tier 24 MiB cap behind a 2 MiB memory tier; 32 workers filling unique 128 KiB cacheable objects; access+audit log on the same tmpfs; filler file to ~1 MiB free; managed config apply under pressure and after; filler removed"
	write_config "
[observability.access_log]
sinks = [\"file\"]
file = \"${fs}/logs/access.log\"
format = \"json\"

[cache]
enabled = true
memory_max_size = \"2MB\"
disk_path = \"${fs}/cache\"
disk_max_size = \"24MB\"
default_ttl = \"10m\"

[[upstreams]]
name = \"app\"
strategy = \"round_robin\"
servers = [\"127.0.0.1:19181\", \"127.0.0.1:19182\"]
"
	sed -i 's#^  proxy_pass = "http://app"#  proxy_pass = "http://app"\n  cache = true#' "${OUT}/jul.toml"
	sed -i "s#^log_level = \"warn\"#log_level = \"warn\"\nconfig_authority = \"managed\"#" "${OUT}/jul.toml"
	sed -i "s#^token = \"${ADMIN_TOKEN}\"#token = \"${ADMIN_TOKEN}\"\nhistory_dir = \"${fs}/data/history\"\naudit_log_file = \"${fs}/logs/audit.log\"#" "${OUT}/jul.toml"
	cp "${OUT}/jul.toml" "${fs}/data/jul.toml"
	"${BIN}/jul" check -config "${fs}/data/jul.toml" >>"${OUT}/events.log"
	df_ev() { ev "df $1: $(df -k --output=size,used,avail "${fs}" | tail -1 | xargs) cache_dir_kb=$(du -sk "${fs}/cache" | cut -f1) log_kb=$(du -sk "${fs}/logs" | cut -f1) data_kb=$(du -sk "${fs}/data" | cut -f1)"; }
	auth=(-H "Authorization: Bearer ${ADMIN_TOKEN}")
	apply() { # apply <label> <log_level>: managed apply of a one-field change
		local base code
		base=$(curl -fsS "${auth[@]}" "http://${ADMIN}/api/v1/config" | jq -r '.persisted_version // .serving_version')
		sed "s#^log_level = \"[a-z]*\"#log_level = \"$2\"#" "${fs}/data/jul.toml" >"${OUT}/candidate-$1.toml" 2>/dev/null ||
			sed "s#^log_level = \"[a-z]*\"#log_level = \"$2\"#" "${OUT}/jul.toml" >"${OUT}/candidate-$1.toml"
		code=$(curl -s -o "${OUT}/apply-$1.json" -w '%{http_code}' "${auth[@]}" -H 'Content-Type: application/toml' \
			--data-binary @"${OUT}/candidate-$1.toml" "http://${ADMIN}/api/v1/config/apply?base_version=${base}" || true)
		ev "managed apply $1 (log_level=$2): HTTP ${code} $(jq -c '{outcome: (.outcome // .error.code // .code), message: (.message // .error.message)}' "${OUT}/apply-$1.json" 2>/dev/null | head -c 400)"
		ev "config file after apply $1: sha256=$(sha256sum "${fs}/data/jul.toml" | cut -c1-16) log_level=$(grep -m1 '^log_level' "${fs}/data/jul.toml") history_entries=$(find "${fs}/data/history" -type f | wc -l)"
	}
	start_jul_cfg() {
		"${BIN}/jul" -config "${fs}/data/jul.toml" >>"${OUT}/jul.log" 2>&1 &
		JUL_PID=$!
		wait_http "http://${ADMIN}/metrics" 20 || {
			ev "Jul did not become ready"
			exit 1
		}
		ev "jul started pid=${JUL_PID}"
	}
	start_jul_cfg
	start_scrape
	snapshot idle
	df_ev idle
	local preview
	preview=$(curl -s "${auth[@]}" -H 'Content-Type: application/json' -d '{}' "http://${ADMIN}/api/v1/config/adopt-external/preview")
	echo "${preview}" >"${OUT}/adopt-preview.json"
	curl -s -o "${OUT}/adopt.json" "${auth[@]}" -H 'Content-Type: application/json' \
		-d "$(jq -c --arg base "$(curl -fsS "${auth[@]}" "http://${ADMIN}/api/v1/config" | jq -r '.serving_version // .persisted_version')" '{observed_digest: .observed_digest, base_version: $base, mode: "hot", confirm: true}' <<<"${preview}")" \
		"http://${ADMIN}/api/v1/config/adopt-external"
	ev "adopt the file as the managed baseline: $(head -c 300 "${OUT}/adopt.json")"
	apply baseline error
	load fill "$(secs 40)" -url "http://${MAIN}/blob?kb=128" -unique-paths -workers 32
	snapshot after-fill
	df_ev after-fill
	local avail
	avail=$(df -k --output=avail "${fs}" | tail -1)
	dd if=/dev/zero of="${fs}/filler" bs=1K count=$((avail - 1024)) status=none 2>>"${OUT}/events.log" || true
	df_ev after-filler
	ev "phase near-full: continuing unique fills with ~1 MiB free"
	load near-full "$(secs 40)" -url "http://${MAIN}/blob?kb=128" -unique-paths -workers 32
	snapshot near-full
	df_ev near-full
	cp "${fs}/data/jul.toml" "${OUT}/config-before-full-apply.toml" 2>/dev/null || true
	dd if=/dev/zero of="${fs}/filler2" bs=1K count=$(($(df -k --output=avail "${fs}" | tail -1))) status=none 2>/dev/null || true
	df_ev full
	apply while-full warn
	if cmp -s "${fs}/data/jul.toml" "${OUT}/config-before-full-apply.toml"; then ev "config file unchanged by the apply attempted while full"; else ev "config file CHANGED by the apply attempted while full"; fi
	"${BIN}/jul" check -config "${fs}/data/jul.toml" >>"${OUT}/events.log" 2>&1 && ev "config on the full filesystem still validates"
	rm -f "${fs}/filler" "${fs}/filler2"
	df_ev filler-removed
	apply after-recovery info
	load recovered "$(secs 30)" -url "http://${MAIN}/blob?kb=128" -unique-paths -workers 32
	snapshot recovered
	df_ev recovered
	quiescence
	stop_scrape
	stop_jul
	ev "cache disk-write-failed log lines: $(grep -c 'cache: disk write failed' "${OUT}/jul.log" || true)"
	ev "jul log lines mentioning ENOSPC: $(grep -ciE 'no space left' "${OUT}/jul.log" || true); access-log failure reports: $(grep -c 'access log write failed' "${OUT}/jul.log" || true)"
	grep -iE 'no space left|disk write failed|access log|audit' "${OUT}/jul.log" | sed -E 's/"time":"[^"]*",?//; s/time=[^ ]+ //; s/[0-9a-f]{64}/<hash>/g; s/\.tmp[-.][A-Za-z0-9]+/.tmp-<rand>/g' | cut -c1-220 | sort | uniq -c | sort -rn | head -20 >"${OUT}/disk-error-kinds.txt" || true
	"${BIN}/jul" check -config "${fs}/data/jul.toml" >>"${OUT}/events.log" && ev "post-pressure jul check: config valid"
	local bad
	bad=$(python3 -c 'import json,sys
bad=0
for l in open(sys.argv[1], errors="replace"):
    try: json.loads(l)
    except Exception: bad+=1
print(bad)' "${fs}/logs/access.log" 2>/dev/null || echo "n/a")
	ev "access log lines that are not whole JSON: ${bad} of $(wc -l <"${fs}/logs/access.log")"
	ev "audit log lines: $(wc -l <"${fs}/logs/audit.log" 2>/dev/null || echo 0)"
	ev "cache files on disk after pressure: $(find "${fs}/cache" -type f | wc -l) ($(du -sk "${fs}/cache" | cut -f1) KiB); temp leftovers: $(find "${fs}" -name '*.tmp*' | wc -l)"
	ev "history snapshots: $(find "${fs}/data/history" -type f | wc -l)"
	start_jul_cfg
	snapshot restart-rehydrated
	ev "restart rehydration: $(metric 'jul_cache_bytes|jul_cache_entries' | tr '\n' ' ')"
	stop_jul
	tail -n 200 "${fs}/logs/access.log" >"${OUT}/access-log-tail.jsonl" 2>/dev/null || true
	cp "${fs}/data/jul.toml" "${OUT}/config-final.toml"
	umount "${fs}"
	rmdir "${fs}"
	manifest "private tmpfs size=48m for cache/log/config/history; cache disk_max_size 24 MiB, memory 2 MiB" "${WORKLOAD}" "See events.log, disk-error-kinds.txt, apply-*.json, snapshots, load-*.jsonl and metrics-summary.md; analysis in docs/soak-evidence.md."
}

# ---- dispatch ------------------------------------------------------------------

trap cleanup EXIT
case "${PROFILE}" in
__inner_dns)
	profile_dns_inner
	;;
__inner_disk)
	profile_disk_inner
	;;
dns | disk)
	build
	prepare_out
	ev "building done; entering namespace for ${PROFILE}"
	# The inner run owns its namespace; OUT, JUL_SHA and HARNESS_SHA are exported.
	FAULT_BIN_DIR="${BIN}" unshare --user --map-root-user --mount $([[ "${PROFILE}" == dns ]] && echo --net) \
		bash "$0" "__inner_${PROFILE}"
	;;
fd)
	build
	prepare_out
	profile_fd
	;;
cpu)
	build
	prepare_out
	profile_cpu
	;;
mem)
	build
	prepare_out
	profile_mem
	;;
esac
echo "evidence: ${OUT}"

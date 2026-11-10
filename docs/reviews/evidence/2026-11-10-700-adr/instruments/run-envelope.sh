#!/usr/bin/env bash
# usage: run-envelope.sh <config-template> <outdir> -- client command...
set -u
CFG=$1; OUT=$2; shift 3
W=/home/victornf/jul-700-adr
mkdir -p $OUT /tmp/adr700/www
[ -f /tmp/adr700/www/page.html ] || head -c 262144 /dev/urandom | base64 -w0 > /tmp/adr700/www/page.html
sed -e "s#<run>/inspect-load.wasm#/tmp/adr700/inspect-load.wasm#" -e "s#<run>/final/www#/tmp/adr700/www#" -e "s#<worktree>#$W#" "$CFG" > $OUT/config.toml
export JUL_EFFECTIVE_VERSION_KEY_FILE=/tmp/adr700/vk
taskset -c 2,3 /tmp/adr700/harness origin -listen 127.0.0.1:19001 >/dev/null 2>&1 & OP=$!
UNIT=adr700-$RANDOM
systemd-run --user --scope --quiet --unit=$UNIT -p CPUQuota=200% -p MemoryMax=768M -p MemorySwapMax=0 env GOMAXPROCS=2 TMPDIR=/tmp/adr700/tmp taskset -c 0,1 $W/jul serve -config $OUT/config.toml > $OUT/jul.log 2>&1 & JP=$!
mkdir -p /tmp/adr700/tmp
for i in $(seq 100); do curl -sk -o /dev/null -w '%{http_code}' https://127.0.0.1:18391/healthz 2>/dev/null | grep -q 204 && break; sleep 0.1; done
CG=$(systemctl --user show -p ControlGroup --value $UNIT.scope)
taskset -c 4-9 "$@" | tee $OUT/client.txt
echo "cgroup_peak_MiB=$(( $(cat /sys/fs/cgroup$CG/memory.peak 2>/dev/null || echo 0) / 1048576 )) oom_kill=$(grep -o 'oom_kill [0-9]*' /sys/fs/cgroup$CG/memory.events 2>/dev/null)" | tee -a $OUT/client.txt
kill $JP $OP 2>/dev/null; sleep 1; systemctl --user stop $UNIT.scope 2>/dev/null; wait 2>/dev/null

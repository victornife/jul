#!/usr/bin/env bash
# 699 revalidation: single-client slow-upload reproducer (s8 config, no cgroup, one client).
set -u
BUILD=$1; BIN=/tmp/rv699/jul-$BUILD; OUT=/tmp/rv699/out/repro-$BUILD; mkdir -p "$OUT"
W=/home/victornf/jul-699
sed -e "s#<run>/inspect-load.wasm#/tmp/rv699/inspect-load.wasm#" -e "s#<run>/final/www#/tmp/rv699/www#" -e "s#<worktree>#$W#" \
  $W/docs/reviews/evidence/2026-11-09-695/final/configs/s8-slow-clients-h1--wasm.toml > "$OUT/config.toml"
mkdir -p /tmp/rv699/www
export JUL_EFFECTIVE_VERSION_KEY_FILE=/tmp/rv699/vk-$BUILD; [ -f "$JUL_EFFECTIVE_VERSION_KEY_FILE" ] || $BIN version-key init >/dev/null 2>&1
/tmp/rv699/harness origin -listen 127.0.0.1:19001 2>/dev/null & OP=$!
$BIN serve -config "$OUT/config.toml" > "$OUT/jul.log" 2>&1 & JP=$!
for i in $(seq 50); do curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18470/healthz | grep -q 204 && break; sleep 0.1; done
python3 -c "import json;print(json.dumps({'k':'x'*64320}))" > /tmp/rv699/body.json   # 64,332 bytes
TOKEN=$(sed -n 's/^token = "\(.*\)"/\1/p' "$OUT/config.toml"); A="Authorization: Bearer $TOKEN"
{
echo "# build=$BUILD sha256=$(sha256sum $BIN | cut -c1-64) plugin=$(sha256sum /tmp/rv699/inspect-load.wasm | cut -c1-12)"
echo "# body bytes=$(stat -c %s /tmp/rv699/body.json); curl --limit-rate <rate> --data-binary @body.json http://127.0.0.1:18470/proxy/echo?resp=4096"
for rate in 0 16k 1M; do
  if [ $rate = 16k ]; then
    ( sleep 2; curl -s -H "$A" "http://127.0.0.1:18472/debug/pprof/goroutine?debug=2" > "$OUT/goroutines-mid-upload.txt"
      curl -s -H "$A" http://127.0.0.1:18472/metrics | grep -E '^jul_plugin_instances' > "$OUT/instances-mid-upload.txt" ) &
  fi
  lr=""; [ $rate != 0 ] && lr="--limit-rate $rate"
  curl -s $lr -D "$OUT/h-$rate.txt" -o /dev/null -w "rate=$rate status=%{http_code} time=%{time_total}s" --data-binary @/tmp/rv699/body.json "http://127.0.0.1:18470/proxy/echo?resp=4096"
  echo " origin_len=$(grep -i '^x-origin-req-len' "$OUT/h-$rate.txt" | tr -d '\r' | cut -d' ' -f2) guest_a_len=$(grep -i '^x-echo-a-req-len' "$OUT/h-$rate.txt" | tr -d '\r' | cut -d' ' -f2) guest_b_len=$(grep -i '^x-echo-b-req-len' "$OUT/h-$rate.txt" | tr -d '\r' | cut -d' ' -f2)"
  wait
done
echo "# mid-upload (t=2s at 16k):"
sed 's/^/  /' "$OUT/instances-mid-upload.txt"
echo "  goroutines in readBounded (preparation): $(grep -c 'plugins.readBounded' "$OUT/goroutines-mid-upload.txt")"
echo "  goroutines reading the body inside a guest call (wazero Call + read_request_body): $(awk '/^goroutine /{if(b&&c)n++;b=0;c=0} /read_request_body|bufferRequestBody|requestBody/{b=1} /wazero.*Call|\(\*plugin\)\.call/{c=1} END{if(b&&c)n++;print n+0}' "$OUT/goroutines-mid-upload.txt")"
echo "# metrics after:"
curl -s -H "$A" http://127.0.0.1:18472/metrics | grep -E '^jul_plugin_(invocations_total|panics_total|request_body_failures_total|duration_seconds_(sum|count)|instance_waits_total)' | sed 's/^/  /'
} | tee "$OUT/repro.txt"
kill $JP $OP; wait 2>/dev/null

#!/bin/bash
# I2 frozen-plan campaign: plan v3 (plan.json sha256 5ce583d0...), harness built from b1a670fe.
# Every run is kept, whatever its outcome.
cd /home/victornf/jul-i2-builds/src/b1a670fe
B=/home/victornf/jul-i2-builds/bin
IA=$B/ia-b1a670fe
O=/home/victornf/jul-i2-evidence/frozen
G="-guest $B/inspect-load.wasm -guest-source examples/plugins/inspect-load -guest-nobody $B/inspect-nobody.wasm -guest-nobody-source examples/plugins/inspect-nobody"
BASE=base:baseline-858590dc:/home/victornf/jul-i5-builds/bin/jul-858590dc-go1.27.3
MAIN=main:current:$B/jul-d8d0520b-go1.27.4
CAND=cand:current:$B/jul-b1a670fe-go1.27.4
run() { name=$1; shift; echo "== $(date -u +%FT%TZ) $name: $*"; taskset -c 4-9 $IA run -out $O/$name $G "$@"; echo "exit=$?"; $IA verify $O/$name; echo "verify=$?"; }
run i2-normal   -profile qualification -suite i2-normal -reps 3 -target $BASE -target $MAIN -target $CAND
run i2-plugin   -profile qualification -suite i2-plugin -target $MAIN -target $CAND
run i2-deadline -profile qualification -suite i2-deadline -target $MAIN -target $CAND
echo "== $(date -u +%FT%TZ) done"

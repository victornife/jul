#!/bin/bash
# I2 supplement (plan v3, harness b1a670fe): additional repetitions required by the
# ADR rule for the inconclusive/failing i2-normal cells. They supplement, never
# replace, the frozen i2-normal runs. base4 is the same-toolchain (go1.27.4) rebuild
# of the ADR source, labelled separately from the historical go1.27.3 build.
cd /home/victornf/jul-i2-builds/src/b1a670fe
B=/home/victornf/jul-i2-builds/bin
IA=$B/ia-b1a670fe
O=/home/victornf/jul-i2-evidence/frozen
G="-guest $B/inspect-load.wasm -guest-source examples/plugins/inspect-load -guest-nobody $B/inspect-nobody.wasm -guest-nobody-source examples/plugins/inspect-nobody"
BASE=base:baseline-858590dc:/home/victornf/jul-i5-builds/bin/jul-858590dc-go1.27.3
BASE4=base4:baseline-858590dc:/home/victornf/jul-i5-builds/bin/jul-858590dc-go1.27.4
MAIN=main:current:$B/jul-d8d0520b-go1.27.4
CAND=cand:current:$B/jul-b1a670fe-go1.27.4
run() { name=$1; shift; echo "== $(date -u +%FT%TZ) $name: $*"; taskset -c 4-9 $IA run -out $O/$name $G "$@"; echo "exit=$?"; $IA verify $O/$name; echo "verify=$?"; }
run i2-normal-supplement -profile qualification -scenario u1-normal-h1,u2-normal-h2,u10-normal-h3,u1b-normal-h1-none-c8 -variants none,wasm -reps 3 -target $BASE -target $BASE4 -target $MAIN -target $CAND
echo "== $(date -u +%FT%TZ) done"

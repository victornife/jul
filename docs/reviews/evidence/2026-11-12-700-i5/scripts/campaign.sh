#!/bin/bash
# I5 frozen-plan campaign (harness 30ff0650). Every run is kept, whatever its outcome.
cd /home/victornf/jul-700-i5
B=/home/victornf/jul-i5-builds/bin
IA=$B/ia-30ff0650
O=/home/victornf/jul-i5-evidence/frozen
G="-guest /tmp/il.wasm -guest-source examples/plugins/inspect-load"
BASE=base:baseline-858590dc:$B/jul-858590dc-go1.27.3
BASE4=base4:baseline-858590dc:$B/jul-858590dc-go1.27.4
MAIN=main:current:$B/jul-363d6cd4-go1.27.4
run() { name=$1; shift; echo "== $(date -u +%FT%TZ) $name: $*"; taskset -c 4-9 $IA run -out $O/$name $G "$@"; echo "exit=$?"; $IA verify $O/$name; echo "verify=$?"; }
Q="-profile quick -target $MAIN -reps 1"
run controlled-success  $Q -scenario u1-normal-h1,u2-normal-h2,u10-normal-h3 -variants none,wasm
run controlled-truncate $Q -scenario u1-normal-h1 -variants none -fault truncate
run controlled-corrupt  $Q -scenario u2-normal-h2 -variants none -fault corrupt
run controlled-stall    $Q -scenario u1-normal-h1 -variants none -fault stall
run controlled-kill     $Q -scenario u1-normal-h1 -variants none -kill-jul-after 3s
run controlled-watchdog $Q -scenario u1-normal-h1 -variants none -watchdog 2s
run campaign-normal -profile qualification -suite campaign-normal -reps 3 -target $BASE -target $BASE4 -target $MAIN
run fidelity        -profile qualification -suite fidelity -target $BASE -target $MAIN
echo "== $(date -u +%FT%TZ) done"

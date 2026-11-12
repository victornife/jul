#!/bin/bash
# I5 supplementary and investigation runs after the frozen campaign. They add
# repetitions and diagnostics; nothing earlier is replaced or removed.
cd /home/victornf/jul-700-i5
B=/home/victornf/jul-i5-builds/bin
IA=$B/ia-30ff0650
O=/home/victornf/jul-i5-evidence/frozen
G="-guest /tmp/il.wasm -guest-source examples/plugins/inspect-load"
BASE=base:baseline-858590dc:$B/jul-858590dc-go1.27.3
BASE4=base4:baseline-858590dc:$B/jul-858590dc-go1.27.4
MAIN=main:current:$B/jul-363d6cd4-go1.27.4
run() { ia=$1; name=$2; shift 2; echo "== $(date -u +%FT%TZ) $name: $*"; taskset -c 4-9 $ia run -out $O/$name $G "$@"; echo "exit=$?"; $ia verify $O/$name; echo "verify=$?"; }
T="-target $BASE -target $BASE4 -target $MAIN"
# u1 none: base:main fail-regression and base4:main inconclusive-noise in campaign-normal.
run $IA supplement-u1-none -profile qualification -scenario u1-normal-h1 -variants none -reps 6 $T
# s7 none: lower main cache throughput in the single fidelity repetition.
run $IA investigate-s7-none -profile qualification -scenario s7-cache-h1 -variants none -reps 3 $T
# s6 both 500s: rerun with the corrected diagnostic counters (plugin errors/panics).
run $B/ia-diag diagnose-s6-both -profile qualification -scenario s6-static-h2 -variants both -reps 1 -target $BASE -target $MAIN
echo "== $(date -u +%FT%TZ) done"

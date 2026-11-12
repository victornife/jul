#!/bin/bash
# Controlled runner validation against real Jul (post-I1 main) in the real envelope.
cd /home/victornf/jul-700-i5
B=/home/victornf/jul-i5-builds/bin
O=/home/victornf/jul-i5-evidence/controlled
T=main:current:$B/jul-363d6cd4-go1.27.4
G="-guest /tmp/il.wasm -guest-source examples/plugins/inspect-load"
run() { name=$1; shift; echo "== $name: $*"; taskset -c 4-9 /tmp/ia run -out $O/$name -profile quick -target $T $G "$@"; echo "exit=$?"; /tmp/ia verify $O/$name; echo "verify=$?"; }
run success   -scenario u1-normal-h1,u2-normal-h2 -variants none,wasm -reps 1
run truncate  -scenario u1-normal-h1 -variants none -reps 1 -fault truncate
run corrupt   -scenario u2-normal-h2 -variants none -reps 1 -fault corrupt
run stall     -scenario u1-normal-h1 -variants none -reps 1 -fault stall
run kill      -scenario u1-normal-h1 -variants none -reps 1 -kill-jul-after 3s
run hung      -scenario u1-normal-h1 -variants none -reps 1 -watchdog 5s

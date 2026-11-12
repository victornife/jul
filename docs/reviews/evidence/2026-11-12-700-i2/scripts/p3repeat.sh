#!/bin/bash
# I2 investigation (plan v3, harness b1a670fe): p3 frozen rep1 candidate peaked at 704 MiB vs
# main 395 MiB, tracking plugin_instances (32 vs 15) rather than governed bytes (<0.6 MiB).
# Repeat main and candidate to decide whether instance-pool growth differs systematically.
while kill -0 $1 2>/dev/null; do sleep 20; done
cd /home/victornf/jul-i2-builds/src/b1a670fe
B=/home/victornf/jul-i2-builds/bin; IA=$B/ia-b1a670fe; O=/home/victornf/jul-i2-evidence/frozen
G="-guest $B/inspect-load.wasm -guest-source examples/plugins/inspect-load -guest-nobody $B/inspect-nobody.wasm -guest-nobody-source examples/plugins/inspect-nobody"
echo "== $(date -u +%FT%TZ) i2-p3-repeat"
taskset -c 4-9 $IA run -out $O/i2-p3-repeat $G -profile qualification -scenario p3-nobody-plugin-h1 -variants wasm -reps 5 -target main:current:$B/jul-d8d0520b-go1.27.4 -target cand:current:$B/jul-b1a670fe-go1.27.4; echo "exit=$?"
$IA verify $O/i2-p3-repeat; echo "verify=$?"; echo "== $(date -u +%FT%TZ) done"

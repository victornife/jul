#!/bin/bash
# I2 investigation (plan v3, harness b1a670fe): frozen p1/none rep1 (no plugin, no lease) read
# 1774 rps on the candidate vs 2968 on main. Repeat both to separate noise from a regression.
while pgrep -f p3repeat.sh >/dev/null; do sleep 20; done
cd /home/victornf/jul-i2-builds/src/b1a670fe
B=/home/victornf/jul-i2-builds/bin; IA=$B/ia-b1a670fe; O=/home/victornf/jul-i2-evidence/frozen
G="-guest $B/inspect-load.wasm -guest-source examples/plugins/inspect-load -guest-nobody $B/inspect-nobody.wasm -guest-nobody-source examples/plugins/inspect-nobody"
echo "== $(date -u +%FT%TZ) i2-p1-none-repeat"
taskset -c 4-9 $IA run -out $O/i2-p1-none-repeat $G -profile qualification -scenario p1-large-body-plugin-h1 -variants none -reps 3 -target main:current:$B/jul-d8d0520b-go1.27.4 -target cand:current:$B/jul-b1a670fe-go1.27.4; echo "exit=$?"
$IA verify $O/i2-p1-none-repeat; echo "verify=$?"; echo "== $(date -u +%FT%TZ) done"

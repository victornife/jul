#!/bin/bash
# #709 investigation and fix evidence (run after the frozen campaign).
# Every run is kept, whatever its outcome.
cd /home/victornf/jul-i2-builds/src/b1a670fe
B=/home/victornf/jul-i2-builds/bin
F=/home/victornf/jul-i2-evidence/frozen
D=/tmp/jul-709-dbg
G="-guest $B/inspect-load.wasm -guest-source examples/plugins/inspect-load"
P4="-profile qualification -scenario p4-reload-budget-plugin"

# 1. Diagnostic runs with instrumented builds (attachments/diag-709-instrumentation.patch),
#    frozen plan v3 harness. jul-dbg is 49669720 plus counters; jul-dbg-fix adds the
#    a8be1417 respwriter fix; jul-dbg-h1 also logs H1 retirement aborts after a header.
taskset -c 4-9 $B/ia-b1a670fe run -out $D/run2 $G $P4 -target dbg:current:$D/jul-dbg          # runs/diag-709-instr-unfixed
taskset -c 4-9 $B/ia-b1a670fe run -out $D/run3 $G $P4 -target dbgfix:current:$D/jul-dbg-fix   # runs/diag-709-instr-fixed
for i in 1 2 3; do                                                                             # runs/diag-709-instr-h1-$i
	taskset -c 4-9 $B/ia-b1a670fe run -out $D/run-h1-$i $G $P4 -target dbgh1:current:$D/jul-dbg-h1
done

# 2. Frozen plan v3 p4, main vs the fixed head, 3 interleaved repetitions.
taskset -c 4-9 $B/ia-b1a670fe run -out $F/i2-p4-709fix $G $P4 -reps 3 \
	-target main:current:$B/jul-d8d0520b-go1.27.4 -target cand2:current:$B/jul-a8be1417-go1.27.4

# 3. The pre-freeze ia-wip harness (default pools, no GOMEMLIMIT, unacknowledged 2 s reloads),
#    fixed head only: main's 2 s OOM is already archived in runs/pre-freeze-p4-default-main.
taskset -c 4-9 $B/ia-wip run -out $F/diag-709-p4-2s-cand2 $G $P4 -reps 2 \
	-target cand2:current:$B/jul-a8be1417-go1.27.4

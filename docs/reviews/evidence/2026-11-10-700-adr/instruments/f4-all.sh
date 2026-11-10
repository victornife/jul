#!/usr/bin/env bash
for p in h1 h2 h3; do for m in stall slow; do /tmp/adr700/f4 -proto $p -mode $m; done; done

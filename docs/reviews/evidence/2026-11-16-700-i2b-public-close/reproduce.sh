#!/usr/bin/env bash
set -euo pipefail

repo="$(git rev-parse --show-toplevel)"
evidence="$repo/docs/reviews/evidence/2026-11-16-700-i2b-public-close"
scratch="$evidence/.work"
umask 077
if ! mkdir -m 700 "$scratch"; then
	echo "Fixture path exists or cannot be created: $scratch; inspect it before another run" >&2
	exit 1
fi
mkdir -m 700 "$scratch/gotmp"
export TMPDIR="$scratch/gotmp" GOTMPDIR="$scratch/gotmp" GOPROXY=off
echo "Fixture retained for inspection: $scratch" >&2
cd "$repo"
version="$(go list -m -f '{{.Version}}' github.com/tetratelabs/wazero)"
if [ "$version" != "v1.12.0" ]; then
	echo "expected wazero v1.12.0, found $version" >&2
	exit 1
fi
echo "go: $(go version)"
echo "host: $(uname -sm)"
echo "wazero: $version"
echo "head: $(git rev-parse HEAD)"
echo "versioned status:"
git status --short --untracked-files=no
echo "input SHA-256:"
shasum -a 256 go.mod go.sum internal/plugins/runtime.go
shasum -a 256 "$evidence/probe.go.txt" "$evidence/runner.py" "$evidence/reproduce.sh"
cp "$evidence/probe.go.txt" "$scratch/probe.go"
printf 'command: go build -o %q %q\n' "$scratch/probe" "$scratch/probe.go"
go build -o "$scratch/probe" "$scratch/probe.go"
printf 'command: python3 %q %q (5s timeout per child)\n' "$evidence/runner.py" "$scratch/probe"
python3 "$evidence/runner.py" "$scratch/probe"

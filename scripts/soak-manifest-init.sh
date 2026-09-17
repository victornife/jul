#!/usr/bin/env bash
# Create a new dated soak-evidence directory with a pre-filled MANIFEST.md
# (JUL-AUD-018). See soak-artifacts/README.md for the convention.
#
# Usage:
#   scripts/soak-manifest-init.sh <scope>
#   JUL_BIN=./jul scripts/soak-manifest-init.sh resilience-24h
set -euo pipefail

if [ $# -ne 1 ] || [ -z "$1" ]; then
	echo "usage: scripts/soak-manifest-init.sh <scope>" >&2
	exit 2
fi
SCOPE="$1"

cd "$(git rev-parse --show-toplevel)"

DATE="$(date -u +%Y-%m-%d)"
DIR="soak-artifacts/${DATE}-${SCOPE}"
if [ -e "$DIR" ]; then
	echo "refusing to overwrite existing directory: $DIR" >&2
	exit 1
fi
mkdir -p "$DIR"

SHA="$(git rev-parse HEAD)"
DIRTY=""
if ! git diff --quiet || ! git diff --cached --quiet; then
	DIRTY=" (working tree NOT clean)"
fi
GOVER="$(go version)"
HOST="$(uname -a)"

CAPS=""
BIN="${JUL_BIN:-jul}"
if command -v "$BIN" >/dev/null 2>&1; then
	CAPS="$("$BIN" capabilities -json 2>/dev/null || true)"
fi

manifest="$DIR/MANIFEST.md"
python3 - "$manifest" "$SCOPE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${SHA}${DIRTY}" "$GOVER" "$HOST" "$CAPS" <<'PYEOF'
import sys

manifest, scope, started, sha, gover, host, caps = sys.argv[1:8]
with open("soak-artifacts/MANIFEST.template.md") as f:
    content = f.read()

replacements = {
    "| Scope | _e.g. \"final soak\", \"resilience-24h\", \"cache-recert\"_ |": f"| Scope | {scope} |",
    "| Date started (UTC) | |": f"| Date started (UTC) | {started} |",
    "| Build SHA | |": f"| Build SHA | {sha} |",
    "| `go version` | |": f"| `go version` | {gover} |",
    "| OS / kernel | |": f"| OS / kernel | {host} |",
}
if caps:
    replacements['| `jul capabilities -json` | ```<paste here>``` |'] = (
        "| `jul capabilities -json` | ```" + caps.replace("\n", " ") + "``` |"
    )
for old, new in replacements.items():
    if old not in content:
        raise SystemExit(f"template line not found (template drifted?): {old!r}")
    content = content.replace(old, new, 1)

with open(manifest, "w") as f:
    f.write(content)
PYEOF

echo "created ${DIR}/MANIFEST.md"
echo "fill in the remaining sections as the run proceeds."

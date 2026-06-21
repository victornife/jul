#!/usr/bin/env bash
#
# Fuzz harness.
#
# Discovers every in-tree Fuzz* target and runs each for a short fuzztime with
# the full opt-in build-tag set, so tagged targets (e.g. FuzzParseTemplate,
# which lives behind the `grpc` tag) are included alongside the always-on ones
# (auth JWKS/token, router host/location, FastCGI script-name/socket-address).
#
# CI runs this as the `fuzz (smoke)` job: each target must survive a brief
# active fuzzing burst without finding a crasher. When a crasher IS found, Go
# writes the minimised input to the package's testdata/fuzz/<Target>/ directory;
# CI uploads that so the failing input is reproducible and can be committed as a
# permanent regression seed.
#
# Each target is retried once. Go's fuzzing engine intermittently reports a
# flaky "context deadline exceeded" at the -fuzztime boundary on very cheap
# targets -- the coordinator's shutdown RPC to a worker churning millions of
# near-instant execs times out (golang/go#48591). That transient writes no
# crasher, so it clears on a retry. A genuine crasher (or a pathologically slow
# input) is instead saved under testdata/fuzz/<Target>/ and replayed
# deterministically on every subsequent run -- including the retry -- so real
# bugs still fail the job.
#
# Usage:
#   scripts/fuzz.sh                      # 15s per target (default)
#   FUZZTIME=2m scripts/fuzz.sh          # longer local soak of the parsers
#   scripts/fuzz.sh ./internal/auth/     # restrict to one or more packages
#
# Environment overrides:
#   FUZZ_TAGS  build tags (default: the full opt-in set, kept in sync with CI)
#   FUZZTIME   -fuzztime value per target (default: 15s)
#
set -euo pipefail

# Keep in sync with .github/workflows/ci.yml `env.FULL_TAGS`.
DEFAULT_TAGS="brotli zstd acme acme_dns console otel grpc http3 importer wasmplugins stream consul kubernetes"

TAGS="${FUZZ_TAGS:-$DEFAULT_TAGS}"
FUZZTIME="${FUZZTIME:-15s}"

PKGS=("$@")
if [ "${#PKGS[@]}" -eq 0 ]; then
	mapfile -t PKGS < <(go list -tags "${TAGS}" ./...)
fi

status=0
for pkg in "${PKGS[@]}"; do
	# List fuzz targets defined in this package (empty when it has none).
	targets="$(go test -tags "${TAGS}" -list '^Fuzz' "${pkg}" 2>/dev/null | grep '^Fuzz' || true)"
	[ -z "${targets}" ] && continue
	for t in ${targets}; do
		# Try up to twice to absorb the flaky "context deadline exceeded"
		# shutdown race (see header). A real crasher is saved to testdata and
		# replays deterministically, so it fails the retry too.
		ok=0
		for attempt in 1 2; do
			echo "== fuzzing ${t} in ${pkg} (${FUZZTIME}, attempt ${attempt})"
			# -fuzz runs exactly one target; -run '^$' skips the unit tests.
			if go test -tags "${TAGS}" -run '^$' -fuzz "^${t}$" -fuzztime "${FUZZTIME}" "${pkg}"; then
				ok=1
				break
			fi
			echo "-- ${t} (${pkg}) failed on attempt ${attempt}"
		done
		if [ "${ok}" -ne 1 ]; then
			echo "!! fuzz target ${t} (${pkg}) found a crasher or failed twice"
			status=1
		fi
	done
done

exit "${status}"

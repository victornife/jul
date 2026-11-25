# DNS-01 review verification

This records the follow-up to the review of #737 at
`54fe8c489a7ab5265128f086e661087ebe25f38f`. The final tested commit and hosted
results are identified in the PR verification comment. Results below are local
Linux/arm64 results, not public-CA certification.

## Final bounded review of merge `08f5d1c`

An AI-assisted, read-only security review checked the merged diff and existing
verification record for TXT ownership and cleanup after ambiguous CA responses,
TSIG scope and secret handling, managed egress, and serving-certificate
preservation after failed renewal. No independent human PKI review was
available; DNS-01 remains Beta.

The review found one high-confidence blocker: managed preflight validation
could return a DNS-01 field value after resolving a secret reference. A
`config:apply` caller able to submit a candidate could place such a reference
in a DNS name or another validated field and receive the expanded value in the
apply response. The finding is at the reviewed merge's
`internal/config/validate_acme_dns.go:132`; the same diagnostic pattern existed
for DNS-zone and server-name checks. The current focused follow-up removes
values from those diagnostics and sanitizes failed managed preflight results
whenever the candidate contains secret references, including downstream
preflight failures. Secret-safe validation also omits parsed field paths because
an error can contain a resolved value that looks like a path. Regression tests
check raw apply, staged apply, TLS preflight failures, and multiline secrets.
The targeted tests and affected Go package suites pass locally; this follow-up
is not part of merge `08f5d1c`.

The review found no blocker in the other three requested areas: owned records
for submitted or uncertain challenges are retained while CA validation may
still be active and reconciled; TSIG owner restrictions are enforced by DNS
server policy; managed egress is used; and failed renewal does not replace a
still-valid serving certificate. The credential-disclosure finding above was
the secret-handling blocker. The review reused the BIND9, Pebble, race, and
Gitleaks evidence recorded below. Gitleaks results remain findings-bearing:
the tree scan reported 35 matches and the commit-range scan reported seven
previously reviewed synthetic-key/assignment matches, not a clean scan.

Public Let's Encrypt staging issuance and renewal on an authorized disposable
zone remain unverified. DNS-01 remains Beta and is not production-qualified.

## Findings and disposition

- Local timeout or shutdown could remove TXT data while the CA was still
  validating. Submitted or uncertain leases are now retained and reconciled
  through the bounded order retry path; cleanup follows a terminal CA state.
- DNS cleanup failure could block certificate installation. Cleanup now runs
  independently with bounded retries and a separate operator warning.
- CA-facing replication and CNAME delegation were not verified before
  submission. Read-only authoritative observers and explicit one-hop mappings
  now gate submission; mismatches and chains fail before mutation.
- Real BIND returns `dns.ErrAuth` for TSIG `NOTAUTH` responses. Authentication
  failures are now reported as such, and a `BADTIME` response is used only as a
  clock diagnostic, never as an authenticated success.
- The full race gate exposed an asynchronous cleanup race in a test fixture;
  its counters are synchronized and the assertion waits for cleanup.
- Hosted semantic-noop and Windows Full jobs on follow-up head
  `03f2f2cbae0d424a297ad7b2defcd7d6f3e573b9` found that a multi-owner test
  waited for only one owner's cleanup. Its assertion now waits for all owners.
- Hosted generation-security on `6fd84762731290109ee880bfebe5af36894b0035`
  exposed the same immediate-cleanup assumption in another test. Related
  issuance tests now wait for all asynchronous cleanup before asserting.

These implementation follow-ups preceded the bounded AI-assisted review
summarized above. The review does not constitute an independent human PKI
review. Public Let's Encrypt staging on an authorized disposable zone remains
unverified. Qlty previously reported one blocking issue despite a successful
GitHub status; its details were inaccessible without sign-in.

## Real DNS and TLS

- `JUL_BIND_E2E=1 go test -tags 'acme http3' ./internal/server -run '^TestDNS01Bind9Interoperability$' -count=1 -v -timeout=90s`:
  passed. The pinned BIND 9.20.11 primary and secondary use
  `ubuntu/bind9@sha256:45d541e9688f2c9010d22a78200117b66ed2df31b78f3464876f7e6e15cfaf77`;
  the CA uses the pinned Pebble image in the test. The fixture verifies
  NS-delegated and one-hop CNAME issuance and renewal, no challenge submission
  before a delayed secondary catches up, mismatch and unconfigured-alias
  rejection before mutation, exact cleanup and unrelated TXT preservation,
  scoped HMAC-SHA256 TSIG, wrong-key and clock-skew diagnostics, and refusal of
  an update outside the permitted TXT owner.
- The same command with `go test -race` passed.
- `JUL_PEBBLE_E2E=1 JUL_PEBBLE_AUTHZREUSE=0 go test -tags 'acme http3' ./internal/server -run '^TestDNS01PebbleEndToEnd$' -count=1 -v -timeout=180s`:
  passed. The same command with `JUL_PEBBLE_AUTHZREUSE=100` also passed. Both
  exercise ECDSA/RSA issuance and renewal and the real HTTP/1.1, HTTP/2 and
  HTTP/3 TLS/mTLS matrix.

The BIND fixture represents a configured CA-facing secondary, not all public
resolver caches or CA network vantage points. See
[TLS/ACME setup and recovery](../tls-acme.md#dns-01-with-rfc-2136-and-tsig).

## Split-authority and retry follow-up

The follow-up to `cd3ef15bb1872885d07c695d54e37008ed48ae3c` requires a
per-owner alias observer list and checks both alias and target before each
challenge retry, while CA polling continues independently. Existing
string-valued CNAME mappings must be migrated to the documented table form.

- `JUL_BIND_E2E=1 go test -tags 'acme http3' ./internal/server -run '^TestDNS01Bind9Interoperability$' -count=1 -v -timeout=90s`:
  passed with two alias-only BIND authorities and a separate target-only
  authority. Issuance, renewal, exact cleanup and missing/restored TXT retry
  probes were exercised.
- The same BIND command with `go test -race` passed when run independently.
  An earlier concurrent run with another Pebble test timed out during renewal.
- `JUL_PEBBLE_E2E=1 JUL_PEBBLE_AUTHZREUSE=0 go test -tags 'acme http3' ./internal/server -run '^TestDNS01PebbleEndToEnd$' -count=1 -v -timeout=180s`:
  passed the real TLS/mTLS matrix; a separate run with reuse set to `100` passed.
- `go test -tags 'acme http3' ./internal/server -run 'Test(DNS01|RFC2136|.*ACME.*|.*TLS.*|.*MTLS.*)' -count=1`:
  passed. Focused retry, cancellation, ownership and split-observer tests also
  passed under `-race`.
- `go test -tags 'acme http3 console' ./internal/config ./internal/lifecycle ./internal/admin ./internal/app ./cmd/jul -count=1 -timeout=180s`:
  passed. The focused Console TLS, mTLS and editor tests passed (38 tests).
- The added Go statement coverage for this follow-up relative to `cd3ef15`,
  using the three local affected-package profiles and
  `scripts/check-added-go-coverage.py`, was 90/99 (90.91%). This is not a
  replacement for the complete hosted PR coverage and Codecov checks.
- `make generated-check`, `make format-check` and `python3 scripts/docs-check.py`:
  passed.
- Gitleaks 8.30.1 was run against the committed tree at
  `2d3925bff2de775c5539d5ee93b00e4d2c7f280b` and its PR commit range. The
  distribution archive passed its SHA-256 check and the executable matched the
  archive. The tree scan reported 35 findings, the same count as the previous
  tree scan; the range scan reported the same seven previously reviewed
  synthetic test-key or variable-assignment matches. Both scans therefore
  returned the scanner's findings exit code, not a clean result.

These results were collected before the merge and do not establish hosted
checks or Codecov results for the current follow-up. Public Let's Encrypt
staging and the Qlty detail remain open as described above.

## Local gates

- `go test -tags 'acme http3' ./internal/server -run 'Test(DNS01|RFC2136|DNSACME)' -count=1 -timeout=180s`:
  passed.
- `go test -race -tags 'acme http3' ./internal/server -run '^Test(DNS01ReconcilesLateCAValidationAfterTimeout|DNS01ShutdownRetainsUnresolvedCAChallenge|DNS01CleanupFailureDoesNotBlockCertificateInstallation|DNS01DoesNotSubmitUntilEveryObserverSeesTXT|RFC2136CNAMEChecksEveryObserverBeforeUpdateAndSubmission|RFC2136ExplicitCNAMEDelegation)$' -count=1 -timeout=90s`:
  passed.
- `go test -race -p 2 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/server -run '^TestDNS01InProcess(AuthorizationsPropagateConcurrently|ConcurrentAuthorizationFailureCleansAllLeases)$' -count=5 -timeout=90s`:
  passed. A prior 30-repeat run hit its overall 180-second timeout without an
  individual test failure.
- `go test -race -p 2 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/server -run '^TestDNS01InProcess(ReconcilesAmbiguousAcceptance|LostAcceptanceResponseWaitsForProcessing|ACMEConcurrentCertificateGroups|AuthorizationsPropagateConcurrently|ConcurrentAuthorizationFailureCleansAllLeases)$' -count=3 -timeout=180s`:
  passed after correcting the remaining assertions.
- `go test -tags 'acme http3 console' ./internal/config ./internal/lifecycle ./internal/admin ./internal/app ./cmd/jul -count=1 -timeout=180s`:
  passed.
- `go test -p 2 -count=1 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/server ./internal/app ./internal/admin ./internal/adminapi ./internal/adminclient ./internal/apicontract ./internal/config ./internal/lifecycle ./internal/observability ./internal/redact ./cmd/jul`:
  passed after the multi-owner assertion fix.
- `go test -p 2 -count=2 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./internal/egress ./internal/auth ./internal/upstream ./internal/plugins ./internal/server ./internal/app`:
  passed after correcting the remaining assertions.
- `go test -race -p 2 -tags 'brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf' ./...`:
  passed after the fixture fix; unaffected packages reused Go's test cache.
- `make console-check`: passed, 1,112 tests and 83.55% statements.
- `cd internal/admin/ui && xvfb-run -a pnpm exec playwright test --headed --project=dns-real-server --reporter=line`:
  passed. Headless Chromium in this environment did not deliver animation
  frames and timed out; the headed run used an isolated virtual display.
- `make generated-check`, `make format-check`, `make lint-full`,
  `make license-check`, `make vulncheck-full`, full-tag `go vet ./...`,
  `go mod verify`, `python3 scripts/check-d11-certification.py`, and
  `python3 scripts/docs-check.py`: passed.
- `make security-gates`: passed the lean and Full negative tests and the
  Full-tag security package coverage floors.
- `JUL_BIN=<isolated-full-binary> make config-check`: passed after building that
  binary with the full tag set; the shared repository-root binary was untouched.
- `JUL_DNS_TSIG='c3ludGhldGljLXRzaWcta2V5LWZpeHR1cmU=' go run -tags acme ./cmd/jul check -config testdata/acme-dns-mtls.toml`:
  passed with a synthetic fixture key.

The move-aware complete changed-Go statement score was **1,861/2,049 (90.82%)**
using the repository's `scripts/strip_moved_lines.py` and
`scripts/check-added-go-coverage.py` with lean and full-tag profiles for the
changed admin, app, config, egress, lifecycle and server packages. The exact
#407 workflow's lean/importer profiles measured **163/171 (95.32%)**. These
profiles were generated before the test-only race correction; production source
was unchanged afterward. Hosted Codecov and all final-head checks must be
verified separately.

To reproduce the complete score from the final commit:

```sh
BASE=e1631d6011df4fb40dfbbfbcd3183da61d2382cb
FULL='brotli zstd acme console otel grpc http3 importer wasmplugins stream consul kubernetes waf'
COVER='./internal/admin,./internal/app,./internal/config,./internal/egress,./internal/lifecycle,./internal/server'
PKGS='./internal/admin ./internal/app ./internal/config ./internal/egress ./internal/lifecycle ./internal/server'
OUT="$(mktemp -d)"
go test -p 2 -count=1 -covermode=atomic -coverpkg="$COVER" -coverprofile="$OUT/lean.cover" $PKGS
go test -p 2 -count=1 -tags "$FULL" -covermode=atomic -coverpkg="$COVER" -coverprofile="$OUT/full.cover" $PKGS
git diff --unified=0 "$BASE"...HEAD -- '*.go' | python3 scripts/strip_moved_lines.py > "$OUT/changed.diff"
python3 scripts/check-added-go-coverage.py --diff "$OUT/changed.diff" --profile "$OUT/lean.cover" --profile "$OUT/full.cover" --floor 90
```

The #407 profile commands and scoring algorithm are in the
[workflow](../../.github/workflows/issue407-quality.yml).

## Secret scanning

The official [Gitleaks 8.30.1 release](https://github.com/gitleaks/gitleaks/releases/tag/v8.30.1)
Linux/arm64 archive was verified against its published checksum list with
`sha256sum -c`. Its SHA-256 is
`e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080`.

A `gitleaks dir --redact=100` scan of the staged tree archive returned exit 1
with 35 matches; the pre-PR base tree returned 28. The seven additional matches
are six occurrences of deliberately synthetic test TSIG keys and one
variable-to-variable assignment, not credentials. The base matches include
test keys, test request payloads, marker strings, historical test evidence and
checksums. These are reported rather than silently allowlisted. The final-tree
and complete PR commit-range results are repeated in the PR verification
comment, without secret values.

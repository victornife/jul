# HTTP load-generator profile capture — #598

The maintainer selected this correction during #515's D11 review. Small
reproducible audit defects are repair work; this finding is not an accepted
limitation. This corrective PR depends on #597 and adds no gateway capability,
maturity promotion or long-duration campaign. Published v2.2.0 is unchanged.

The original helper wrote HTTP 403 response bodies as profiler files and the
command unconditionally claimed artifacts were saved. The retained synthetic
reproducer fails against main `616f0ca0ec7ef3f7568ec8b11d0fc1c783df14bc`
and passes against the correction. Historical soak reports retain their
original identity and are not relabeled as executions of this repair.

## Result and regression boundary

Each heap/goroutine capture requires HTTP 200 and a nonempty, complete download,
with a ten-second timeout and normal TLS verification. Redirects are rejected.
A temporary file is published only after a successful copy and close; failures
remove temporary files and preserve the existing destination. Errors report
the profile and a fixed category or status, without headers, response bodies
or wrapped transport errors. The command reports traffic independently and
exits 1 if any of its four captures failed. Health/output-directory preflight
failures stop before traffic; successful completion names all four profiles.

The explicit-file race suite exercises real runtime heap/goroutine handlers,
401/403/404/500/redirects, untrusted and trusted TLS, timeout, empty/truncated
bodies, missing output directories, failed publication and preservation of a
previous complete profile. The actual command path is exercised at 100 ms
with success, forbidden capture, output failure and health failure. This is a
deterministic loopback fixture, not a Jul production deployment or a soak.

## Coverage and verification

CI explicitly tests both standalone Go load generators, which use the ignore
build constraint, and includes their profiles in normal Codecov. Complete
changed-production scoring uses the established HR-07C block algorithm and
`strip_moved_lines.py`, without a package allowlist. All existing repository,
owner and Console coverage floors remain unchanged.

The evidence manifest records the measured source/tree, tool versions,
complete cumulative and corrective-only diffs, statement counts and raw
profiles. Whole-file HTTP-tool coverage is reported separately; many existing
traffic modes are outside this narrow regression suite. The thin `os.Exit`
wrapper and exceptional file-close error branch remain uncovered; no coverage
exclusion suppresses them.

Local checks include explicit-file race tests and vet, scorer fixtures,
docs-check, docs regressions, semantic regressions and generated-check. Hosted
mandatory/platform checks must pass at the final PR head before integration.
NGINX migration is not applicable: no translation or gateway runtime contract
changes. Maintainer integration and #515's actual human spot-check remain
pending; neither capture validation nor numeric coverage is certification.

[Raw evidence and source manifest](evidence/2026-10-13-profiler/).

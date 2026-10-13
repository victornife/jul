# Operational harness discovery and build profiles — #605

The #515 script review reproduced false successful fuzz exits when Go package
or target listing failed. The benchmark's local default also omitted WAF while
claiming the full profile; CI supplied its correct explicit profile. The
maintainer selected small reproducible audit defects for repair. This separate
tooling correction depends on #603 and does not change Go runtime statements,
release packages, maturity, coverage floors or a long-soak result.

Package listing and target listing now preserve their Go failure status; a
successfully compiled package with no fuzz targets remains valid. Fuzzing still
retries at most twice, preserves a recovered pass and fails persistent target
errors. Benchmark defaults now match all **13** authoritative Makefile/CI tags.
The existing sync guard additionally checks both shell-harness default consumers;
explicit caller overrides remain supported.

Eight deterministic regressions execute the actual shell scripts with a fake
Go boundary. Four fail against the original source (listing failures, benchmark
profile and consumer drift); all eight pass after correction. They prove actual
argument/profile/time settings, successful no-target behavior and bounded retry,
not just string presence. CI runs them in the existing full-tags-sync job.

A real one-iteration WAF fuzz and benchmark smoke passes with the default full
profile. This is bounded compilation/execution evidence, not fresh performance
numbers, complete corpus execution, long-duration reliability or security
certification. Existing inherited Go corrections retain their separate source
coverage/CI records; this correction adds no Go production statement.

[Raw original/repaired logs and source manifest](evidence/2026-10-13-harness/)
retain commands, tool/profile identities and outcomes. Docs/regression/generated
checks and normal exact-head hosted checks remain required. NGINX translation
is unaffected. Approved integration and #515's human spot-check remain pending.

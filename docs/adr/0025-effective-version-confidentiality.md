# ADR 0025 — Confidential effective configuration versions

- Status: Accepted for #644 / PR #647
- Date: 2026-10-19
- Deciders: maintainer approval, 2026-10-19; implementation merge gated on green CI
- Applies to: startup, reload, admin version and identity output
- Source: [#644](https://github.com/victornife/jul/issues/644)
- Scope: effective desired/serving versions and public authentication generations
- Amends: ADR 0019 §20; preserves unresolved configuration CAS/history versions

## Context

Unkeyed effective configuration fingerprints let a reader check guessed referenced
values offline. A public identifier must retain equality without that verifier.

## Decision

Keep `CanonicalVersion(raw)` unchanged. Effective versions use HMAC-SHA-256 over
canonical bytes from the exact immutable resolved candidate or serving snapshot.
A 256-bit independently generated server-held key provides a restart-stable
context. Public identifiers have format `ev1:<128-bit context id>:<256-bit tag>`.
The context id is a separately domain-separated HMAC of a fixed string; the
configuration and compiled WAF `external_digest` (`waf-external-source`, #649)
use distinct domains under the server's durable context while keeping private
in-memory WAF source-change comparison separate. Authentication generation and posture
use distinct domains under a separate private boot-only context: their fences
need within-process equality and do not promise comparison across restart. Both
contexts use length framing and neither reuses the sharing key. No provider is read again for hashing. No unkeyed alias is emitted.

This protects references in every supported string, including ordinary fields,
embedded/multiple/reused and short/empty references. It preserves effective
rotation detection, unchanged-value equality and returning to an earlier value
within one key context. Raw persisted/base revisions and raw history correlation
remain unchanged. Directly embedded plaintext secrets remain within the raw-hash
threat boundary; this does not make those raw revisions confidential.

The smallest alternative, process-random identifiers, avoids an offline oracle
but loses restart-stable comparisons. A public salt, longer hash, masking or
credential-field allowlist does not protect all supported references. A durable
random key adds an operational provisioning/backup duty but preserves the
existing meaning of effective versions at bounded implementation cost.

## Provisioning and failure contract

Use startup-only `JUL_EFFECTIVE_VERSION_KEY_FILE`, outside configuration TOML.
The default is an OS user state location independent of the configuration
folder. `jul version-key init [--file PATH]` explicitly creates a protected key
without replacing an existing one or printing its contents. Startup only loads;
missing, invalid, inaccessible or unprotected state fails before serving.
An existing valid key is reused by competing initializers; interrupted private
temporary files are never treated as installed keys. Publication is atomic and
without replacement. Keep the key and raw state together in restricted backups.

This is an explicit upgrade requirement. An absent file cannot distinguish first
installation from loss of established state without another surviving authority.
Silently auto-generating a replacement is therefore rejected. Provisioning a
read-only key file supports immutable configuration and read-only service roots;
no writable configuration directory is required. A completely ephemeral system
must provision the same private key on each launch to retain restart comparison.

Contexts from independent keys are incomparable, not proof of changed secret
values. Unknown formats and legacy effective identifiers are unverifiable.
Changing keys is an explicit new context: first converge pending work, preserve
raw backups and the old key, then provision/select the new protected key file.
Never replace a missing/corrupt key to pretend historical equality survives.

## Retained records and downgrade

Existing raw history and baseline digests remain valid. Legacy planned-restart
fields are not publicly emitted. Raw-digest reconciliation remains authoritative;
legacy/mismatched effective metadata cannot authorize discard by an omitted
comparison. A successful restart that genuinely serves the staged raw file may
clear its marker through the existing recovery path. No historical effective
identifier is regenerated from today's secret providers.

Binary downgrade is separate from configuration rollback. The supported boundary
requires the fixed binary to converge pending restart state first, then stop,
retain key/raw backups and run the earlier binary with its supported invocation.
Unresolved post-fix markers require recovery under the fixed binary first.
A pre-fix binary restores the vulnerability; downgrade never retains #644's
confidentiality guarantee. Copies of legacy hashes already distributed cannot be
revoked; rotate any at-risk weak referenced value after upgrading.

## Consequences

Serving now requires an explicitly provisioned private key, including local and
zero-config runs. This is an operator-visible upgrade requirement. Native Windows
ACL execution and complete cross-platform coverage remain merge gates.

## Related

See [operator recovery and deployment patterns](../effective-versions.md).

## Sharing context

#640 remains Proposed. Sharing postconditions keep raw configuration as their
configuration-digest input. Its latest AD-09 keys every postcondition digest,
including raw configuration digests and access fingerprints, under a separate
process-only context with a deliberately restart-unverifiable lifetime. Do not
reuse that key or the new effective-version context to change those guarantees.
Cursor/evidence consumers must treat these
version strings as opaque and compare only within their identified context.

Companion edit required before #640 lands: its proposed sharing ADR currently
uses 0025, the next available main identifier. Rebase it as ADR 0026 and update
its spec/packet/index links; append the effective-version scope clarification.
This does not accept the sharing design or change #641/#642 completion.

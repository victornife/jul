# Diagnostics and support bundles

Jul provides two local, operator-triggered diagnostic commands:

- `jul doctor` evaluates configuration and local deployment prerequisites.
- `jul support-bundle` writes a bounded, redacted `tar.gz` archive for offline troubleshooting.

Neither command mutates Jul configuration or live runtime state. `jul doctor` is read-only; `jul support-bundle` writes only the explicitly requested local archive. They do not upload data, create an installation identifier, start a server, rewrite configuration, change deployment permissions, repair files, or execute arbitrary commands.

## `jul doctor`

Run the default local and network-free checks:

```bash
jul doctor --config /etc/jul/server.toml
```

Emit the versioned JSON contract:

```bash
jul doctor --config /etc/jul/server.toml --json
```

Make warnings fail in CI:

```bash
jul doctor --config /etc/jul/server.toml --json --strict
```

Enable bounded network-capable checks explicitly:

```bash
jul doctor --config /etc/jul/server.toml --check-network
```

`--check-network` permits Jul to resolve configured secret references, run the authoritative runtime preflight, and perform immediate-close local TCP/UDP bind probes. It does not send application traffic or probe arbitrary URLs. Default doctor operation performs no external network checks.

### Check phases

The local registry is closed and deterministic. Current checks cover:

1. configuration-file type, readability, symlink state, size, and permissions;
2. strict TOML decoding, including unknown-field rejection;
3. authoritative semantic validation;
4. configuration lint and authority/filesystem findings;
5. configured input/output path existence, type, readability, static-root availability, and sensitive-file permissions without reporting path values;
6. operator-supplied certificate/key parsing, matching, validity windows, concrete configured-name coverage, and near-expiration;
7. admin-listener exposure and authentication posture;
8. bounded topology metadata: counts and enabled states only;
9. safe build and process metadata;
10. optional runtime preflight and listener bind probes.

Later checks are marked `skipped` when an earlier prerequisite is unavailable. One failure does not hide independent evidence.

### Stable check codes

| Code | Phase | Meaning |
| --- | --- | --- |
| `CONFIG_FILE` | configuration | File type, readability, symlink state, size, and platform-appropriate permission guidance. |
| `CONFIG_PARSE` | configuration | Strict TOML decoding and unknown-field rejection. |
| `CONFIG_VALIDATE` | configuration | Authoritative semantic validation. |
| `CONFIG_LINT` | configuration | Lint plus authority/filesystem findings. |
| `CONFIGURED_PATHS` | deployment | Bounded checks of configured files, directories, and static roots without exposing their values. |
| `TLS_CERTIFICATES` | security | Certificate/key match, validity window, concrete configured-name coverage, and expiry horizon. |
| `ADMIN_SECURITY` | security | Listener exposure and presence of a currently usable configured credential. |
| `CONFIG_TOPOLOGY` | runtime | Counts and enabled-state metadata only. |
| `SYSTEM_RUNTIME` | runtime | Product, version, commit, build profile, Go, platform, CPU, and capability metadata. |
| `RUNTIME_PREFLIGHT` | network | Opt-in authoritative runtime preflight. |
| `LISTENER_BIND` | network | Opt-in immediate-close local TCP/UDP bind probes. |

Later additive checks require new stable codes; existing codes are not repurposed.

### Result contract

JSON reports use `schema_version: 1` and contain:

- a bounded summary;
- checks in registry order;
- stable check codes;
- `pass`, `warning`, `error`, or `skipped` status;
- severity;
- a human message;
- bounded, secret-safe evidence;
- remediation guidance;
- a documentation link.

Messages and remediation text may improve. Check codes, statuses, severities, and the versioned envelope are the machine contract.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | No error result. Warnings are allowed unless `--strict` is used. |
| `1` | One or more error results, or command output could not be written. |
| `2` | Invalid command usage, or warnings are present with `--strict`. |

## `jul support-bundle`

Create a bundle in the current directory:

```bash
jul support-bundle --config /etc/jul/server.toml
```

Choose a destination and emit generation metadata as JSON:

```bash
jul support-bundle \
  --config /etc/jul/server.toml \
  --output ./jul-support.tar.gz \
  --json
```

Include a bounded tail of the configured Jul access-log file:

```bash
jul support-bundle \
  --config /etc/jul/server.toml \
  --include-logs \
  --log-tail-bytes 65536
```

Logs are excluded by default. The log collector can read only the file configured as the Jul access-log file and only when the `file` sink is enabled. It rejects symbolic links and non-regular files. There is no arbitrary path, glob, directory, or environment-variable collector.

### Archive layout

The local registry currently creates the following fixed entries when their prerequisites are available:

```text
manifest.json
NOTICE.txt
build/runtime.json
configuration/metadata.json
diagnostics/doctor.json
diagnostics/doctor.txt
logs/access.log.tail       # only with --include-logs and a configured file sink
```

The local/offline bundle does not claim live runtime, reload, Prometheus, or remote-admin evidence. Those surfaces require a separately supported, authenticated external API. Private Console endpoints are not used as an implicit compatibility contract.

### Manifest

`manifest.json` records:

- bundle format version;
- Jul version, commit, and build profile when available;
- creation time and collection duration;
- requested collectors;
- collector success, error, skipped, or truncated status;
- fixed artifact paths and content types;
- artifact byte sizes and SHA-256 checksums;
- sensitivity notes;
- redaction profile;
- the active timeout, item, artifact, uncompressed, and compressed-size bounds.

Collector failures are explicit. A noncritical collector failure still produces a usable archive and a nonzero CLI exit code.

### Safety and resource bounds

Default limits are:

| Limit | Default |
| --- | ---: |
| Total collection time | 30 seconds |
| Per collector | 8 seconds |
| Artifact count | 32 |
| One artifact | 2 MiB |
| Total uncompressed content | 12 MiB |
| Compressed archive | 8 MiB |
| Concurrent generations | 1 |
| Optional access-log tail | 64 KiB, hard-capped at 512 KiB |

CLI flags may reduce or increase the byte/time limits within the operator's local invocation. Collection remains bounded and cancellable.

The archive is streamed to a same-directory owner-only temporary file, synchronized, and then published without overwriting an existing path. Jul rejects symbolic-link components and removes temporary files after cancellation or failure. Archive entries use fixed, traversal-safe names.

### Secret and privacy policy

Collectors exclude these values structurally:

- raw configuration values;
- bearer or basic credentials;
- tokens, passwords, API keys, cookies, and client secrets;
- private keys;
- request and response bodies;
- raw traffic;
- full environment-variable dumps;
- arbitrary files or directories.

Text and JSON artifacts receive a defensive second redaction pass. Known resolved values supplied internally to the generator are replaced exactly before serialization. Errors are sanitized because operating-system and library errors can contain URLs or credentials.

Redaction is risk reduction, not a mathematical proof that every business-sensitive identifier has been removed. Hostnames, configured names, log messages, and operational identifiers may still be sensitive even when they are not authentication secrets.

**Review every bundle before sharing it. Do not publish support bundles as public issue attachments by default.**

## No automatic repair or upload

`jul doctor` does not implement `--fix`. `jul support-bundle` does not upload to a vendor, support service, object store, or telemetry endpoint. Generating a bundle never changes configuration authority and works in both `managed` and `file_owned` operation because it is read-only.

## Remote diagnostics

This implementation is local. A future authenticated support-bundle or doctor operation must be added deliberately to the versioned external admin API, with dedicated RBAC, transport security, rate/concurrency limits, audit metadata, cache-prevention headers, and deterministic cleanup. The remote CLI must consume that supported API rather than private Console routes or a second diagnostic engine.

## Quality gate

CI enforces at least 85% statement coverage independently for:

- `internal/diagnostics`;
- `internal/doctor`;
- `internal/supportbundle`.

The same gate runs the three packages and `cmd/jul` under the race detector and the complete optional build-tag set.

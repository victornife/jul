# Effective versions: provisioning and recovery

Unreleased #644 introduces an explicit startup prerequisite for every serving
mode, including `jul run`, managed/file-owned configurations and embeddings.
Local check/lint/format/doctor/support commands do not need a key. The key is
outside TOML and cannot be changed through the Console or API.

## Install or upgrade

Stop the old process, retain a restricted backup of raw configuration, history,
managed baseline and any pending-restart sidecars, then provision once as the
service identity:

```sh
export JUL_EFFECTIVE_VERSION_KEY_FILE=/private/jul/effective-version.key
jul version-key init
jul serve -config /path/to/server.toml
```

Choose a real private directory with trusted ancestors. The command creates
missing directories and a cryptographically random 32-byte key, atomically
without replacing an existing destination. It prints only the path. Startup
loads the key before opening listeners; it never creates, repairs or rotates it.
An absent file cannot distinguish first installation from established key loss.
Do not run init to mask loss: restore the original protected backup instead.
Concurrent initializers load the single published winner. Incomplete private
tempfiles are ignored. Hard-link publication is required; unsupported filesystems
must receive an already-provisioned protected key. Keep storage local and trusted.
Directory fsync is best effort where unsupported; back up the installed key.

Default paths (when the environment override is absent):

| Platform | Path |
| --- | --- |
| Linux/Unix | `$XDG_STATE_HOME/jul/effective-version.key`, or `$HOME/.local/state/jul/effective-version.key` |
| macOS | user configuration directory `/jul/effective-version.key` |
| Windows | `%AppData%\jul\effective-version.key` |

Unix key files require no group/other permission (0600 or read-only 0400), and
trusted current-user/root ownership. Ancestors must not permit untrusted
replacement; symlinks are refused. Windows requires a private leaf directory and
protected key grants limited to the service identity, SYSTEM and Administrators;
ancestors may allow reads/sibling creation, but no replacement or metadata/ACL
writes. Windows TrustedInstaller is accepted only as an ancestor owner, never
as a key-reader grant. An explicit ancestor grant to that already-trusted
owner has the same authority as its implicit ability to change the DACL. Unsupported ACEs fail
closed. Native Windows ACL and virtual-service-account tests remain a merge
blocker for this change; cross-compilation is not runtime evidence. Do not deploy
this proposal to Windows services until that gate passes.

## Startup path selection

`--effective-version-key-file PATH` takes precedence over
`JUL_EFFECTIVE_VERSION_KEY_FILE`, then the platform default. This flag is
available for bare serving, `jul serve`, `jul run` and Windows SCM startup.
It selects a protected existing file; it never initializes it.

## Containers, services and immutable deployments

No writable configuration directory is required. Mount a private key read-only,
or persist the key separately from immutable TOML. A non-root process must own
or be permitted to read it under the protection checks. Keep the same key for
repeated ephemeral launches; an independent random key creates an incomparable
context. Never bake one shared key into a distributable image.

The Docker image uses `/var/lib/jul/private/effective-version.key`. Seed its
nonroot-owned state volume explicitly, then run with that same volume:

```sh
docker run --rm -v jul-state:/var/lib/jul jul:local version-key init
docker run --rm -v jul-state:/var/lib/jul jul:local --config /etc/jul/server.toml
```

Use your built image tag in place of `jul:local`. With a read-only root, mount a
provisioned key at that path and supply the config/cache/log mounts your chosen
features require. Protect bind-mount ownership for uid 65532.

The editable systemd example sets its path under `/var/lib/jul/private`; before
start, provision that directory as jul and run init as jul. The read-only example
uses `/etc/jul/private`: provision as jul before making it read-only (0400 key,
0700 directory). Root-owned key files are accepted only when the service can
actually read them; avoid making them group-readable to work around access.
Neither unit automatically initializes missing state. `ProtectHome` means a
service cannot use an inaccessible home-directory default.

## Windows service first installation

From elevated PowerShell, prepare the config in its protected root, then invoke
this source version's installer with explicit first-install opt-in:

```powershell
.\install-service.ps1 -BinaryPath 'C:\Program Files\jul\jul.exe' `
  -ConfigPath 'C:\ProgramData\jul\server.toml' -DataDir 'C:\ProgramData\jul' `
  -InitializeVersionKey
Start-Service jul
```

The default service key is `%SystemDrive%\JulVersionKey-jul\effective-version.key`
(on the system volume), independent of DataDir. Standard ProgramData grants include
untrusted metadata writes and are refused for key ancestors. Use
`-VersionKeyFile PATH` to select another dedicated private location. The installer
passes that path as an explicit SCM image argument, provisions only when
explicitly opted in, and sets an administrator-owned protected ACL limited to
SYSTEM/Administrators and read access for the virtual service identity. Existing
bytes are retained; unsafe prior grants/reparse points are refused. The service
validates the key under its actual identity. On installer reruns omit the opt-in
and reuse the same path. Missing established state requires backup restoration,
not another initialization. Native service CI must verify this boundary before
merge; a syntactically valid installer is insufficient evidence.

## Meaning and exposure

Raw persisted/base/history versions remain the canonical unresolved SHA-256
revision used for CAS. Whitespace-only changes retain that revision. Inline
plaintext secrets remain guessable through a known raw configuration; use
references and protect raw backups. #644 does not conceal intentionally exposed
configuration values, provider files or application behavior.

Effective versions are `ev1:<32 lowercase hex context>:<64 lowercase hex tag>`.
HMAC-SHA-256 covers the full canonical immutable resolved configuration with
length-framed domains and bytes. All supported reference strings are covered,
including ordinary fields and literal/embedded/multiple/reused references.
Providers are resolved once per candidate; version computation never reads them
again. Publication caches the value for concurrent status reads. A file provider
or environment rotation with identical raw TOML changes the effective ID if its
accepted resolved value changes. Rolling back to an earlier effective value
under the same key restores equality.

Public credential IDs use domain-separated `cid1:<context>:<tag>`, including
identity and JSON/CSV/durable audit. Internal ownership IDs remain private.
Derived OIDC group principals are keyed too. Auth-generation/posture fences use
a separate boot-only random context, because those fences have no cross-restart
contract. Location preparation errors use declaration indexes instead of private
matcher hashes. No unkeyed effective compatibility alias is emitted.

## Recovery, retained records and downgrade

| Transition | Outcome / procedure |
| --- | --- |
| Same key, unchanged effective inputs; restart/backup restore | Effective equality retained; raw CAS unchanged. |
| Env/file rotation or non-secret effective change | Effective ID changes; raw reference revision stays fixed when raw TOML stays fixed. |
| Independent keys / explicit key rotation | `different_context`; no claim that inputs are equal or different. Converge pending work first, stop, back up old state+key, select a separately provisioned key. |
| Missing, corrupt, inaccessible or unprotected key | Startup fails before listeners. Restore original protected key; no unkeyed or random fallback. |
| Legacy/unknown effective marker values | `unverifiable`; suppress those values in API/Console. Discard is refused. Restart the fixed binary into the staged file after validation, or restore consistent raw state+sidecars from a restricted backup while stopped. |
| Key mismatch with pending marker | `different_context`; restore original key or converge by a real restart. No force-discard or CAS bypass. |
| New staged configuration | Desired differs from serving until actual publication/startup; the version scheme alone never implies serving. |
| Raw history/baseline from old binary | Unchanged raw input contracts remain valid. No historical effective value is reconstructed from current providers. |
| Unknown/corrupt marker state | Existing raw-digest recovery fails explicitly; preserve backup, investigate and restore consistent state. |

Older retained audit files are append-only evidence, not loaded into the API's
in-memory ring. New writes protect IDs; public export protects any legacy raw ID
supplied through an embedding. Already distributed unkeyed hashes/IDs cannot be
revoked. Protect historical files and rotate at-risk weak references after
upgrading. Do not rewrite historical records or expose the old identifier as an
alias. Treat public IDs as opaque strings, with no fixed 16-character assumption.

Configuration rollback and binary downgrade are separate operations. The tested
binary-downgrade boundary is **converged state only**: complete or discard pending
work with the fixed binary, restart it if necessary, verify no pending marker,
stop, and retain raw/history/baseline/key backups before starting the earlier
binary. The earlier binary ignores the key file and can read converged raw state
and history, but restores the #644 vulnerability. Returning to the fixed binary
with the same key restores its context. Downgrade with an unresolved post-fix
marker is unsupported; recover under the fixed binary first. Never synthesize
vulnerable hashes or disable concurrency checks to make a downgrade proceed.

## Namespace fault harness boundary

Normal non-root and container deployments use a protected service-owned key.
Non-root user namespaces can map all host ancestor owners to an indistinguishable
overflow UID; that cannot establish the required ownership trust. The namespace
fault harness now requires root on a dedicated host and retains mount/network
namespace isolation without user remapping. It provisions a disposable key
outside retained evidence and removes it after the run. This is an explicit
harness invocation change, not permission to trust arbitrary unmapped owners.
No long fault/soak job was run for this fix.

Windows ancestor ownership follows the host OS trust boundary. Microsoft documents
[owner DACL authority](https://learn.microsoft.com/en-us/windows/win32/secauthz/owner-of-a-new-object)
and [TrustedInstaller servicing authority](https://learn.microsoft.com/en-us/windows/win32/wfp/about-windows-file-protection).
This is a trust decision about ancestors, not a grant to read the private key.

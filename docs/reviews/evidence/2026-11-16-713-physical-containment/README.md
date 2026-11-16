# #713 / #725: physical containment evidence

## Identity and scope

The archived observations were made on 16-11-2026 with Jul **`67ca93ee61c0c98e01cda0f06f77c0766dd5c871`**, Coraza **v3.8.1**, Go **go1.27.4 darwin/arm64**, macOS **26.7.1 (25G241)** and a non-root user. These are observations of that checkout, not tests of the #725 head. When the package was prepared, #725 pointed to **`410b5c9cb8c065bacd3dad0fc7286ec53eb213d5`**, base **`9c70b64d31396882ab62cc38f1c95377e2e9ddf6`**.

`coraza-probe.go.txt` and `filesystem-probe.py.txt` are byte-for-byte copies of the executed code; the extension prevents Go from picking them up when running `go test ./...`. `observed-*.txt` contains output with fixture paths redacted and trailing whitespace trimmed. The original launcher is not preserved here. The following manual instructions **have not been run identically to that launcher** and do not guarantee exact automated reproduction.

## Executed observations

| Experiment | Result and limitation |
| --- | --- |
| Coraza, private TMPDIR, multipart with spill | `body*` and `crzmp*` coexisted in TMPDIR and disappeared on close. With an explicit `SecUploadDir`, `crzmp*` was created in another directory. One case per configuration, not a universal proof. |
| CreateTemp denied | Spill: `WriteRequestBody` returned an error. Multipart: `ProcessRequestBody` returned nil error and interruption, with `REQBODY_ERROR=1`. |
| Jul/httptest, same multipart failure | Without CRS in `block`: 204/origin 362 bytes; CRS in `block`: 400/origin 0; CRS in `detect`: 204/origin 362. `detect` is not the governor's observe mode. |
| Spill cleanup | The owned file was removed after processing the body; `Close` returned an error, and `os.IsNotExist` on that error was false. Not all close/remove failures were injected. |
| `no_fs_access` | Small multipart: no `crzmp*` file; 4330-byte body with a 1024-byte in-memory limit: error, no spill. This does not exhaustively evaluate file-dependent rules. |
| Private 16 MiB HFS+ image | 5000 empty files were created without exhaustion; writing 13,631,488 complete bytes ended in ENOSPC with 643,072 bytes available according to `statvfs`. These are physical limits of the experiment, **not** an exact logical quota or proof of an independent inode quota. The 5000 cap belongs to the test. |

Existing tests, with private `TMPDIR` and `CGO_ENABLED=0`, at the specified commit:

```sh
go test -tags waf -count=1 -v -run '^(TestFirewallRequestBodyLimitRejectsOversizedBody|TestAssessmentRequestBodySpill|TestAssessmentArgumentLimitPartialInspection)$' ./internal/waf
# exit 0: observed-waf-default.txt
go test -tags 'waf no_fs_access' -count=1 -v -run '^(TestFirewallRequestBodyLimitRejectsOversizedBody|TestAssessmentRequestBodySpill|TestAssessmentArgumentLimitPartialInspection)$' ./internal/waf
# exit 1: observed-waf-no-fs.txt
```

The `no_fs_access` variant failed with CRS, a 1 MiB limit and a 512 KiB body: 400/origin 0 versus 204/origin 524288. The other selected cases passed. No default change is proposed.

## Static evidence and outstanding limitations

In Coraza v3.8.1, `internal/corazawaf/waf.go` sets `TmpDir` from `os.TempDir`; `body_buffer.go` uses that path and `transaction.go` passes `UploadDir` to the multipart parser. With an empty `UploadDir`, `CreateTemp` uses `os.TempDir`; `SecUploadDir` changes the path and `SecTmpDir` is accepted but ignored. TMPDIR is process-wide and must be set before creating the WAF.

In `ProcessRequestBody`, parser errors are converted into generic variables that do not distinguish quota, I/O and syntax. In Jul `internal/waf/outcomes.go`, returned buffering/processing errors are handled before the handler, but the body reader is consumed by the handler and the error from the deferred `tx.Close` is ignored after the handler. Those paths are static inspection; not all read/partial-write/close failures or H1/H2/H3 were tested. The absence of partial forwarding and fail-closed behavior for all profiles are not established.

Linux was not tested: Docker had no available daemon; no services or VMs were started. An independent inode quota is not established on macOS or any other OS. The image shares the host's physical storage and does not guarantee host availability, per-transaction ownership, logical limits, crash recovery or A19. Keeping private isolation outside static roots, restricting upload redirection and monitoring capacity are only partial mitigations; a typed lifecycle boundary, advance byte/file reservation and safe debt and migration management remain outstanding.

## Optional manual reproduction

Use a **disposable, clean** checkout of the specified commit, without important files, under the user's exclusive control and outside served roots. Do not use root: the permissions experiment requires an unprivileged user. The instructions are for macOS; the image portion requires `hdiutil`. Check Go and dependencies before starting. The process temporarily modifies the disposable checkout and the physical test mounts a private image; do not run it in a shared environment or if another process can modify the fixture. If any step fails, stop and inspect; do not proceed assuming the mount or cleanup succeeded.

From the disposable checkout, in a shell with `set -e`, set `EVIDENCE` to the absolute path of this directory and prepare the exclusive fixture. First check that neither the checkout, its path nor the fixture are symlinks or unexpected mount points. The second `test` also rejects a dangling symlink. Do not reuse an existing directory.

```sh
set -e
EVIDENCE=/ruta/absoluta/al/directorio/2026-11-16-713-physical-containment
test -f "$EVIDENCE/coraza-probe.go.txt"
test "$(git rev-parse HEAD)" = 67ca93ee61c0c98e01cda0f06f77c0766dd5c871
test -z "$(git status --porcelain --untracked-files=normal)"
umask 077
root="$(pwd -P)/.i3b-713-fixture"
test ! -e "$root"
test ! -L "$root"
mkdir -m 700 "$root"
mkdir -m 700 "$root/gotmp" "$root/engine" "$root/alternate" "$root/mount"
cp "$EVIDENCE/coraza-probe.go.txt" "$root/probe.go"
TMPDIR="$root/gotmp" CGO_ENABLED=0 go build -tags waf -o "$root/probe" ./.i3b-713-fixture
env -u PROBE_NOFS TMPDIR="$root/engine" "$root/probe"
TMPDIR="$root/gotmp" CGO_ENABLED=0 go build -tags 'waf no_fs_access' -o "$root/probe-nofs" ./.i3b-713-fixture
PROBE_NOFS=1 TMPDIR="$root/engine" "$root/probe-nofs"
```

To repeat the existing Go tests above, set `TMPDIR="$root/gotmp"` and `CGO_ENABLED=0` for each command. Compare the output; a zero exit code from the probe does not by itself certify its observations.

### Physical test, only if needed

Before mounting, manually confirm that `root` is still the owned fixture, that `mount` is an empty directory and not a symlink or mount point, and that `bounded2.dmg` does not exist and is not a symlink. Do not continue if in doubt. The image depends on shared host space.

```sh
test -f "$EVIDENCE/filesystem-probe.py.txt"
test ! -e "$root/bounded2.dmg"
test ! -L "$root/bounded2.dmg"
test -d "$root/mount"
test ! -L "$root/mount"
test -z "$(ls -A "$root/mount")"
hdiutil create -size 16m -fs HFS+ -volname I3bProbe "$root/bounded2.dmg"
hdiutil attach "$root/bounded2.dmg" -mountpoint "$root/mount" -nobrowse -noverify
```

Inspect `hdiutil info`, `mount` and `diskutil info "$root/mount"`; confirm that exactly that private image was mounted at that directory. Only then run:

```sh
python3 "$EVIDENCE/filesystem-probe.py.txt" "$root/mount"
hdiutil detach "$root/mount"
```

Check `hdiutil info` and `mount` again. If attachment or detachment fails, or the volume's identity is uncertain, stop and resolve it manually before touching the fixture. Do not detach another volume.

### Manual cleanup

After confirming that the volume is detached, inspect `pwd -P`, `ls -la` of the fixture and each subdirectory. Confirm that they are the original directories, without symlinks, mount points or unrelated files. Remove **only individually** the known fixture files (`probe.go`, `probe`, `probe-nofs` and, if created and detached, `bounded2.dmg`). If other files or directories exist, stop and investigate ownership; do not delete them automatically. Then remove only the empty directories `gotmp`, `engine`, `alternate`, `mount` and finally `.i3b-713-fixture` using `rmdir`. An `rmdir` failure means stop and inspect.

To check integrity from this evidence directory: `shasum -a 256 -c SHA256SUMS`.

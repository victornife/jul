# NGINX migration assessment

> Feature ID: **Y1-09** · Build tag: `importer` · Report schema: **2**

`jul import nginx` produces deterministic evidence about what the importer
translated, approximated, ignored, or could not represent. It is not a
production-equivalence certificate and it never reports a compatibility
percentage.

Assessment and conversion use the same NGINX parser, translator, canonical Jul
marshaler, `config.Parse`, `config.Validate`, and `config.Lint` path. Assessment
modes do not write generated configuration.

## Commands

```bash
go build -tags importer ./cmd/jul

# Existing single-file conversion.
jul import nginx -o jul.toml /etc/nginx/nginx.conf

# Human or JSON assessment. Relative paths are the default.
jul import nginx --assess /etc/nginx/nginx.conf
jul import nginx --json /etc/nginx/nginx.conf

# Navigate human output by source, line, and column.
jul import nginx --assess --source-order /etc/nginx/nginx.conf

# Follow an include tree under an explicit root.
jul import nginx \
  --assess \
  --follow-includes \
  --root /etc/nginx \
  /etc/nginx/nginx.conf

# Generate TOML and a JSON report only when traversal is complete.
jul import nginx \
  --follow-includes \
  --include-root /etc/nginx \
  --input /etc/nginx/nginx.conf \
  --output jul.toml \
  --report migration-assessment.json
```

`--root` and `--include-root` are aliases. When both are supplied, they must
resolve to the same directory. A root flag requires `--follow-includes`.
`--input` is an alternative to the positional source path and `--output` is an
alias for `-o`.

`--json` and `--assess` are alternative stdout formats. They cannot be combined
with a generated-config output path. `--report <file>` is used when generated
TOML and a machine-readable report are both required. `--source-order` applies
only to human output because JSON results already use deterministic source
traversal order.

Absolute paths are opt-in:

```bash
jul import nginx --json --path-style absolute /etc/nginx/nginx.conf
```

Absolute reports can expose host topology and should normally remain local.

## Include traversal and trust boundary

Include traversal is **disabled by default**. Without `--follow-includes`, Jul
reads only the root file and every `include` is a blocking
`NGX_INCLUDE_DISABLED` result. This preserves the historical single-file
behavior and prevents hidden filesystem reads.

With `--follow-includes`, Jul—not `gonginx`—discovers and reads each source. The
third-party parser remains the semantic parser for each file, but it never owns
unbounded include traversal.

The allowed root defaults to the input file's directory. Before reading an
included file, Jul verifies both:

1. the cleaned lexical path remains under the configured root; and
2. the evaluated symlink target remains under the evaluated root.

There is no network include, shell expansion, arbitrary directory crawl, or
unrestricted host-root mode. Relative includes resolve from the including
file's directory. Absolute include paths are accepted only when they remain
inside the configured root. Glob matches are sorted before parsing. Hidden
files are excluded from wildcard matches, matching ordinary NGINX glob
behavior. Repeated non-cyclic includes remain separate source instances; they
are not silently deduplicated.

Traversal uses conservative positive limits:

| Flag | Default | Meaning |
| --- | ---: | --- |
| `--max-include-depth` | `16` | Maximum nested include depth. |
| `--max-include-files` | `256` | Maximum source files, including the root. |
| `--max-include-file-bytes` | `4194304` | Maximum bytes read from one source. |
| `--max-include-total-bytes` | `33554432` | Maximum bytes read across the tree. |
| `--max-include-glob-matches` | `1024` | Maximum files matched by one glob. |

Every override must be positive. Hitting a limit stops further expansion at the
responsible include and produces a blocking result. Jul returns a partial but
truthful report containing the sources already read. It never writes generated
TOML after a followed traversal marked `complete = false`.

### Include result codes

| Code | Meaning |
| --- | --- |
| `NGX_INCLUDE_DISABLED` | Traversal was not requested; no included source was read. |
| `NGX_INCLUDE_RESOLVED` | All matches for that include were read and inserted at the include point. |
| `NGX_INCLUDE_MISSING` | An explicit source or glob matched no source. |
| `NGX_INCLUDE_UNREADABLE` | A matched source could not be opened or inspected safely. |
| `NGX_INCLUDE_GLOB_INVALID` | The include pattern is malformed. |
| `NGX_INCLUDE_CYCLE` | The include closes a direct or indirect active cycle. |
| `NGX_INCLUDE_DEPTH_LIMIT` | The depth limit was reached. |
| `NGX_INCLUDE_FILE_LIMIT` | The source-file or glob-match limit was reached. |
| `NGX_INCLUDE_BYTE_LIMIT` | The per-file or total-byte limit was reached. |
| `NGX_INCLUDE_ROOT_ESCAPE` | The lexical path leaves the configured root or uses a network form. |
| `NGX_INCLUDE_SYMLINK_ESCAPE` | The evaluated symlink target leaves the root. |
| `NGX_INCLUDE_PARSE_ERROR` | A bounded source was read but could not be parsed. |

All outcomes have root-file or included-file provenance. Failure messages use
safe display paths rather than disclosing an unintended host root.

## Result taxonomy

Every parsed directive and structural block receives one primary result. A
small number of cross-directive checks add `synthetic` results.

| Class | Meaning | Default severity |
| --- | --- | --- |
| `supported` | Represented within documented Jul semantics. | `info` |
| `approximated` | A related behavior is emitted, but a semantic difference needs review. | `warning` |
| `ignored` | No generated effect, usually because the directive controls the NGINX process. | `info` |
| `blocking` | Not safely representable; manual action is required. | `error` |
| `informational` | Structural/contextual source material with no generated setting. | `info` |
| `parse_error` | A source could not be parsed. | `error` |
| `validation_error` | The generated Jul candidate failed authoritative validation. | `error` |

Risk is a bounded enum: `security`, `routing`, `availability`, `observability`,
`performance`, `operational`, or `cosmetic`. Automation should branch on stable
codes, classes, severities, and risks rather than human prose.

## Schema version 2

The machine contract is committed at
[`docs/nginx-assessment.schema.json`](nginx-assessment.schema.json). A complete
multi-file example is available at
[`docs/nginx-assessment.example.json`](nginx-assessment.example.json).

`source_policy` records the trust boundary and traversal outcome:

```json
{
  "path_style": "relative",
  "root": ".",
  "follow_includes": true,
  "complete": true,
  "files_read": 3,
  "total_bytes": 9812,
  "limits": {
    "max_depth": 16,
    "max_files": 256,
    "max_file_bytes": 4194304,
    "max_total_bytes": 33554432,
    "max_glob_matches": 1024
  }
}
```

`limits` is present when traversal is enabled. `complete = false` means the
assessed tree is partial, even if some valid Jul settings were generated in
memory. A consumer must not interpret such a candidate as a complete migration.

Incompatible machine-contract changes increment `schema_version`. New finding
or guidance codes inside the existing bounded structures do not require a
version bump. Consumers should tolerate unknown codes unless deliberately
implementing a closed policy.

## Source catalogue and ancestry

Each item in `sources` contains:

- a deterministic report-local `source-0001` style ID;
- a safe display path;
- an optional SHA-256 digest of the assessed bytes;
- `parent_id` and `include_line` for included instances.

The digest is evidence about bytes read, not a signature. Repeated includes get
separate IDs so traversal order and ancestry remain truthful. Relative display
paths do not contain temporary or working-directory prefixes. Windows
separators are normalized to `/` in shareable output; root and drive checks use
platform-native path rules before display normalization.

## Result identity and provenance

Parsed findings use IDs such as `result-source-0002-0007`. Synthetic and
unmatched findings use separate namespaces. IDs depend on source traversal and
directive occurrence, not prose, absolute temporary paths, map iteration, or
generated target text.

A parsed result's `provenance` contains:

- source ID and display path;
- one-based start line/column and an end position where recoverable;
- a bounded context path such as
  `http > server[example.test @ :443] > location[/api]`;
- directive name;
- a bounded redacted summary shared by JSON and human output.

The lexical index records coordinates only. `gonginx` remains the semantic
parser and the existing classifier remains the taxonomy authority. When an
exact span cannot be matched confidently, the result falls back to line-only
provenance rather than attaching the wrong source range.

Include directives use `scope = "source"`. Synthetic findings use `global` or
`derived`; a derived result can reference `related_result_ids`.

## Context preservation

Included directives are inserted at the include point before the existing
translator/classifier runs. A fragment included inside `http`, `server`,
`location`, or `upstream` therefore inherits that context instead of being
translated as a standalone root configuration. Successful include expansion is
informational; the included directives produce their own ordinary assessment
results and target mappings.

Generated Jul TOML remains canonical and receives no provenance comments by
default.

## Target mappings

Supported and approximated findings can expose canonical Jul paths through
`target_mappings`:

| Relation | Meaning |
| --- | --- |
| `direct` | One source result maps directly to the listed path or paths. |
| `approximate` | A target is emitted but needs semantic review. |
| `expands_to_multiple` | One source construct produces multiple target paths. |
| `combines_with_siblings` | Several source directives jointly define one target policy. |

Paths use wildcard array notation such as
`servers[].locations[].proxy_pass`. They describe generated configuration, not
durable resource identity.

## Guidance

Blocking and approximated results reference stable `GUIDE_*` codes. The
report-level catalogue deduplicates titles, actions, consequences, documentation
anchors, and blocking disposition.

Include outcomes use:

- `GUIDE_INCLUDE_ENABLE` when traversal was not requested;
- `GUIDE_INCLUDE_RESOLVE` when a followed tree is incomplete.

Finding and guidance codes are independent: findings describe what happened;
guidance describes what the operator must do next.

## Human output

Default human output groups blockers first. `--source-order` emits one sequence
ordered by source instance, line, column, and result ID. Both modes show concise
`file:line:column` locations, target paths, guidance, and related results.

The source-policy line states whether includes were enabled, whether traversal
was complete, files and bytes read, and active limits. Information is never
conveyed only by color; non-TTY output has no ANSI-dependent meaning.

## Readiness and exit codes

`ready` means there is no blocking, parse, or generated-candidate validation
result. It does **not** certify behavioral equivalence or production cutover.
Approximations still require review.

| Exit | Meaning |
| --- | --- |
| `0` | Conversion or assessment completed without blocking findings. |
| `1` | Internal importer failure. |
| `2` | Invalid command usage or conflicting flags. |
| `3` | Blocking/incomplete traversal, or strict-mode warning findings. |
| `4` | Root NGINX parse failure. |
| `5` | Generated Jul candidate failed parsing or validation. |
| `6` | Source, generated-config, or report I/O failure. |

Assessment-only JSON is still emitted for diagnostic findings. A root file that
cannot be read or parsed uses the dedicated command failure exit. A parse error
in a followed include is a provenance-bearing blocking assessment result and
marks the tree incomplete.

## Candidate validation

The assessor:

1. parses each bounded NGINX source;
2. assembles the source tree at include points;
3. translates with the existing importer;
4. marshals with Jul's canonical TOML marshaler;
5. reparses through `config.Parse`;
6. validates through `config.Validate`;
7. records `config.Lint` warnings.

A candidate that fails parsing or validation is never written. A followed but
incomplete source tree is also never written.

## Secret and source safety

Reports never contain raw source excerpts or stable raw argument fields. Human
and JSON output share one summary function that redacts or omits:

- Authorization, cookie, and token header values;
- credentials embedded in proxy URLs;
- authentication material;
- private-key and credential-file paths where required;
- values from maps, variables, Lua, and snippets;
- include path arguments.

Reports are written with owner-only permissions. They can still contain
business-sensitive topology, hostnames, relative paths, and digests; review them
before sharing. Generated TOML is a separate artifact and may preserve values
needed by the translation, so it must be reviewed independently.

## CI examples

```bash
jul import nginx \
  --json \
  --follow-includes \
  --root ./nginx \
  ./nginx/nginx.conf > assessment.json

case $? in
  0) echo "complete assessment contains no blockers" ;;
  3) echo "manual action or incomplete source tree" >&2; exit 1 ;;
  *) echo "assessment command failed" >&2; exit 1 ;;
esac

# Treat approximations, ignored process settings, and Jul lint findings as
# non-clean as well.
jul import nginx --assess --strict --follow-includes --root ./nginx ./nginx/nginx.conf
```

The assessment performs no DNS lookup, backend probe, NGINX execution, traffic
replay, source edit, or production cutover.
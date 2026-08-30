# NGINX migration assessment

> Feature ID: **Y1-09** · Build tag: `importer` · Report schema: **2**

`jul import nginx` can produce a deterministic assessment before an operator
relies on generated configuration. The assessment is evidence about what the
importer translated, approximated, ignored, or could not represent. It is not a
production-equivalence certificate and it never produces a compatibility
percentage.

The existing conversion command remains valid:

```bash
go build -tags importer ./cmd/jul
jul import nginx -o jul.toml /etc/nginx/nginx.conf
```

Assessment modes use the same parser, translator, canonical Jul marshaler, and
`config.Validate` path as ordinary conversion:

```bash
# Human report only; writes no Jul configuration.
jul import nginx --assess /etc/nginx/nginx.conf

# Navigate the human report in source order rather than blocking-first order.
jul import nginx --assess --source-order /etc/nginx/nginx.conf

# Versioned JSON report only on stdout. Relative paths are the default.
jul import nginx --json /etc/nginx/nginx.conf

# Emit absolute paths only after an explicit local-only choice.
jul import nginx --json --path-style absolute /etc/nginx/nginx.conf

# Generate Jul TOML and write the JSON assessment beside it.
jul import nginx \
  --input /etc/nginx/nginx.conf \
  --output jul.toml \
  --report migration-assessment.json
```

`--input` is an alias for the positional source path. `--output` is an alias for
`-o`. `--json` and `--assess` are assessment-only modes and cannot be combined
with an output-config path. Use `--report <file>` when both generated TOML and a
machine-readable report are required. `--source-order` applies only to the
human report because JSON results already remain in deterministic source order.

## Result taxonomy

Every parsed directive and structural block receives one primary result. A
small number of cross-directive checks, such as conflicting trusted-proxy
policies on one listener, add a result marked `synthetic`.

| Class | Meaning | Default severity |
| --- | --- | --- |
| `supported` | The source construct is represented within the documented Jul semantics. | `info` |
| `approximated` | Jul emits a related behavior, but a semantic difference needs review. | `warning` |
| `ignored` | The directive has no generated effect, usually because it controls the NGINX process rather than request behavior. | `info` |
| `blocking` | The source behavior is not safely representable; migration is not ready without manual action. | `error` |
| `informational` | Structural or contextual source material that needs no generated setting. | `info` |
| `parse_error` | The NGINX input could not be parsed. | `error` |
| `validation_error` | The generated Jul candidate failed Jul's authoritative validation. | `error` |

Risk categories are a bounded enum: `security`, `routing`, `availability`,
`observability`, `performance`, `operational`, and `cosmetic`. Machine consumers
should branch on `code`, `class`, `severity`, and `risk`; human messages may be
clarified in later releases.

## Schema version 2

The JSON Schema is committed at
[`docs/nginx-assessment.schema.json`](nginx-assessment.schema.json), with a
complete example at
[`docs/nginx-assessment.example.json`](nginx-assessment.example.json).

Schema version 2 adds structured source provenance, source policy, target
mappings, related results, and a deduplicated guidance catalogue. It retains the
version 1 `line` and `target_paths` fields as compatibility projections; new
consumers should prefer `provenance` and `target_mappings`.

A shortened report has this shape:

```json
{
  "schema_version": 2,
  "source": "nginx.conf",
  "source_policy": {
    "path_style": "relative",
    "root": ".",
    "follow_includes": false
  },
  "sources": [
    {
      "id": "source-0001",
      "display_path": "nginx.conf",
      "digest": "sha256:..."
    }
  ],
  "status": "manual_action_required",
  "summary": {
    "total": 12,
    "supported": 9,
    "approximated": 1,
    "ignored": 0,
    "blocking": 2,
    "informational": 0,
    "parse_errors": 0,
    "validation_errors": 0,
    "ready": false
  },
  "results": [],
  "guidance": [],
  "validation": {
    "status": "valid"
  }
}
```

Incompatible machine-contract changes increment `schema_version`. Adding a new
finding code or guidance entry within the existing bounded structures does not
by itself require a version bump. Consumers must ignore unknown finding and
guidance codes unless they intentionally enforce a closed policy.

## Source catalogue and path privacy

`source_policy` records how source files were represented:

- `path_style = "relative"` is the default and makes the report portable and
  safer to share. The root is rendered as `.` and `source`/`display_path` do not
  contain the host's temporary or working-directory prefix.
- `path_style = "absolute"` is available only through the explicit
  `--path-style absolute` flag. It is useful for local navigation but can expose
  host topology and should not be used for broadly shared artifacts.
- `follow_includes` is `false` in schema version 2. The current tranche assesses
  one root file only. Every `include` remains an explicit blocking finding; no
  included file is read implicitly.

Each entry in `sources` has a deterministic report-local source ID and optional
SHA-256 digest. The digest identifies the bytes assessed; it is not a security
signature or a durable identity across changed files. Repeated future include
expansions may receive separate source IDs so ancestry remains truthful.

## Result identity and provenance

Parsed findings use deterministic report-local IDs such as
`result-source-0001-0007`. Synthetic and unmatched findings use separate
namespaces such as `result-synthetic-0001`. IDs depend on deterministic source
traversal and directive occurrence, not human prose, absolute paths, map order,
or generated target text.

A parsed result's `provenance` contains:

- `source_id` and safe `display_path`;
- one-based start line/column and an end coordinate when the lexical index can
  recover it safely;
- a bounded context path such as
  `http > server[example.test @ :443] > location[/api]`;
- directive name;
- a bounded, redacted summary shared by JSON and human output.

The lexical index records coordinates only. `gonginx` remains the single
semantic parser and the existing assessment classifier remains the only source
of finding taxonomy. If a coordinate cannot be matched confidently, the report
falls back to line-only provenance rather than attaching the wrong span.

Synthetic findings set `scope` to `global` or `derived`. A derived result can
reference `related_result_ids`; a global result does not pretend to originate
from one arbitrary directive.

## Target mappings

Supported and approximated results can expose `target_mappings` with canonical
Jul configuration paths. Relations are bounded:

| Relation | Meaning |
| --- | --- |
| `direct` | One source result maps directly to the listed Jul path or paths. |
| `approximate` | A target is generated, but the semantic difference still requires review. |
| `expands_to_multiple` | One source construct produces more than one target path. |
| `combines_with_siblings` | Several source directives jointly define one target policy. |

Canonical paths use wildcard array notation, for example
`servers[].locations[].proxy_pass`. A mapping is evidence about generated
configuration, not a promise of durable object identity. Generated Jul TOML
remains canonical and receives no provenance comments by default.

## Guidance and manual action

Blocking and approximated findings reference stable `GUIDE_*` codes. The
report-level `guidance` array deduplicates their operator-facing title, action,
consequence, documentation anchor, and blocking disposition. Finding codes and
guidance codes are independent: finding taxonomy describes what happened;
guidance describes what to do next.

Representative guidance includes:

| Guidance code | Required action |
| --- | --- |
| `GUIDE_INCLUDE_ENABLE` | Treat the source tree as incomplete until bounded include traversal is used. |
| `GUIDE_CONDITIONAL_MANUAL` | Rewrite variable-driven or conditional behavior explicitly. |
| `GUIDE_PROXY_TARGET_MANUAL` | Replace a dynamic proxy target with finite explicit routes/upstreams. |
| `GUIDE_HEADER_POLICY_MANUAL` | Port static request/response header or CORS policy explicitly. |
| `GUIDE_AUTH_MANUAL` | Recreate and test the authentication boundary. |
| `GUIDE_LIMITS_MANUAL` | Recreate body, rate, concurrency, or connection limits. |
| `GUIDE_LOCATION_REVIEW` | Review path matching, precedence, alias, and rewrite semantics. |
| `GUIDE_REALIP_HEADER` | Declare and validate a supported trusted forwarded header. |
| `GUIDE_REALIP_LISTENER` | Reconcile listener-scoped trusted-proxy policy. |
| `GUIDE_CANDIDATE_VALIDATION` | Correct the generated candidate before writing or loading it. |
| `GUIDE_MANUAL_REVIEW` | Review an otherwise uncategorized semantic difference explicitly. |

Messages and prose may improve; automation should use codes.

## Human output navigation

The default human report groups blocking and validation findings first, then
approximations, ignored directives, supported directives, and informational
results. `--source-order` instead prints one navigable sequence with concise
`file:line:column` locations. Both modes show target paths, guidance codes, and
related result IDs where present.

The JSON result array always remains in deterministic source traversal order.
Information is never conveyed only by color, and non-TTY output contains no
ANSI-dependent meaning.

## Readiness and exit codes

`ready` means the importer found no blocking, parse, or generated-candidate
validation result. It does **not** claim full behavioral equivalence or a safe
production cutover. Approximated behavior still requires operator review.

| Exit | Meaning |
| --- | --- |
| `0` | Conversion or assessment completed without blocking findings. |
| `1` | Internal importer failure. |
| `2` | Invalid command usage or conflicting flags. |
| `3` | Assessment completed with blocking findings, or `--strict` found approximated/ignored/lint findings. |
| `4` | NGINX parse failure. |
| `5` | Generated Jul candidate failed authoritative parsing or validation. |
| `6` | Source, generated-config, or report file I/O failure. |

For backward compatibility, ordinary conversion without `--assess`, `--json`,
`--report`, or `--strict` still writes a valid generated candidate even when its
legacy TODO header lists untranslated directives. Assessment/report modes return
exit `3` for those same blocking findings so CI can gate migration work.

## Candidate validation

The assessor does not implement a second validation engine. It:

1. translates the parsed NGINX tree with the existing importer;
2. marshals the candidate with Jul's canonical TOML marshaler;
3. parses the generated TOML through `config.Parse`;
4. validates it through `config.Validate`;
5. records `config.Lint` findings as candidate warnings.

A candidate that fails parsing or validation is never written. JSON Schema
validation of the assessment is separate from runtime validation of the
generated Jul configuration.

## Secret and source safety

Assessment results do not include raw directive arguments or source excerpts.
The same summary function feeds human and JSON output and structurally redacts
or omits common sensitive inputs, including:

- `Authorization`, cookie, and custom token header values;
- credentials embedded in proxy URLs;
- basic-auth or other credential material;
- certificate/private-key and credential-file paths where policy requires it;
- arbitrary values from NGINX variables, maps, Lua, or snippets;
- include path arguments.

Reports are written with owner-only permissions. Review both generated config
and report before sharing them: paths, hostnames, listener topology, source
digests, and other operator metadata may still be business-sensitive even when
secret values are excluded.

## Important blocking and approximate cases

The assessment registry follows the actual current importer surface documented
in [NGINX config importer](nginx-importer.md). In particular:

- `include`, `stream`, `mail`, variable maps, arbitrary `if`, server-level
  rewrites, source-address ACLs, authentication directives, request body limits,
  dynamic proxy targets, and untranslated cache/rate-limit behavior are
  blocking rather than silently dropped;
- `alias`, trailing-URI `proxy_pass`, server-level `return`, selected location
  modifiers, non-default balancing algorithms, and the narrow `limit_except`
  mapping are approximated;
- NGINX process/event-loop and selected connection-pool tuning directives are
  ignored with explicit results;
- unknown directives are always explicit and default to blocking, with a risk
  category derived from the directive family.

### Trusted client address (`realip`)

The assessment reflects the implemented real-IP mapping:

- `set_real_ip_from` with a canonical address or CIDR is supported;
- `real_ip_header X-Forwarded-For` and `real_ip_header Forwarded` are supported;
- `real_ip_recursive on` is already Jul's right-to-left behavior;
- `X-Real-IP`, `proxy_protocol`, and `real_ip_recursive off` are blocking;
- trusted sources without an explicit supported `real_ip_header` are blocking;
- incompatible policies on server blocks sharing one listen address are
  blocking and emit no trusted-proxy policy;
- a source with no real-IP directives never gains trust.

### Response headers and CORS

Only static `add_header NAME VALUE always;` forms are supported. Missing
`always`, variable-derived values, invalid max-age, and wildcard origin combined
with credentials are blocking because translating them would widen or invalidate
policy.

## Current single-file boundary

Schema version 2 deliberately does not implement include traversal. The root
file receives complete provenance, but each `include` is still blocking and
`source_policy.follow_includes` remains `false`. Do not concatenate independently
translated snippets and assume that reproduces NGINX include context or order.
Bounded root-confined traversal, cycle detection, ancestry, glob ordering,
symlink protection, and resource limits are a separate implementation tranche.

## CI examples

```bash
# Gate only on unrepresentable behavior.
jul import nginx --json nginx.conf > assessment.json
case $? in
  0) echo "assessment contains no blockers" ;;
  3) echo "manual migration action required" >&2; exit 1 ;;
  *) echo "assessment command failed" >&2; exit 1 ;;
esac

# Treat approximations, ignored process settings, and Jul lint findings as
# non-clean as well.
jul import nginx --assess --strict nginx.conf

# Produce a portable source-navigation report.
jul import nginx --assess --source-order --path-style relative nginx.conf
```

The assessment is deliberately local and deterministic. It performs no network
lookup, backend probe, NGINX execution, traffic replay, automatic source edit,
or production cutover.

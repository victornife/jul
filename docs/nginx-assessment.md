# NGINX migration assessment

> Feature ID: **Y1-09** · Build tag: `importer` · Report schema: **1**

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

# Versioned JSON report only on stdout.
jul import nginx --json /etc/nginx/nginx.conf

# Generate Jul TOML and write the JSON assessment beside it.
jul import nginx \
  --input /etc/nginx/nginx.conf \
  --output jul.toml \
  --report migration-assessment.json
```

`--input` is an alias for the positional source path. `--output` is an alias for
`-o`. `--json` and `--assess` are assessment-only modes and cannot be combined
with an output-config path. Use `--report <file>` when both generated TOML and a
machine-readable report are required.

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

The JSON Schema is committed at
[`docs/nginx-assessment.schema.json`](nginx-assessment.schema.json). The top-level
contract is:

```json
{
  "schema_version": 1,
  "source": "/etc/nginx/nginx.conf",
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
  "validation": {
    "status": "valid"
  }
}
```

Results stay in deterministic source order in JSON. The human report groups
blocking findings first, followed by approximations, ignored directives,
supported directives, and informational results.

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
validation of the report is separate from runtime validation of the generated
Jul configuration.

## Secret and source safety

Assessment results identify the directive name, bounded context, result code,
and source line. They do not include raw directive arguments or source excerpts.
This prevents common secret-bearing inputs from entering reports, including:

- `Authorization`, cookie, and custom token header values;
- credentials embedded in proxy URLs;
- basic-auth or other credential material;
- private-key contents;
- arbitrary values from NGINX variables or snippets.

Generated Jul TOML follows the existing importer and configuration secret rules.
Reports are written with owner-only permissions. Review both the generated config
and report before sharing them: paths, hostnames, listener topology, and other
operator metadata may still be business-sensitive even when secret values are
excluded.

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
```

The assessment is deliberately local and deterministic. It performs no network
lookup, backend probe, NGINX execution, traffic replay, automatic source edit,
or production cutover.

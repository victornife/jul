# Provider-free application quota example

This local example exercises a public route, a verified account, an approved
integration, and a tighter export operation without an external identity
provider. It uses Basic authentication backed by a local bcrypt htpasswd file.
It is a #745 candidate feature, not a GA commitment.

| Class | Admission budget | Principal |
| --- | --- | --- |
| `public` | 15 req/s, burst 30; 10 req/s per client, burst 20 | Fallback |
| `account` | 10 req/s, burst 20; 3 req/s per account, burst 6 | Basic `alice` in `local-accounts` |
| `integration` | 5 req/s, burst 10; 2 req/s per account, burst 4 | Basic `integration` in `local-accounts` |

The policy ceiling is 30 req/s with burst 60. The `/export` route adds a shared
2 req/s, burst 4 limit and a 1 req/s, burst 2 per-caller limit. Class allocations
are fixed and do not borrow unused capacity. Different Basic accounts share
their class allocation but have separate caller limits; repeated sessions for
one account use the same caller bucket.

## Run

From this directory, create local credentials. The password hashes stay in an
ignored local file and no demo password is committed:

```bash
htpasswd -B -c users.htpasswd alice
htpasswd -B users.htpasswd integration
mkdir -p backend/public backend/account backend/export
printf '%s\n' 'public route' > backend/public/index.html
printf '%s\n' 'account route' > backend/account/index.html
printf '%s\n' 'export route' > backend/export/index.html
```

In one terminal, start a local upstream:

```bash
python3 -m http.server 3000 --bind 127.0.0.1 --directory backend
```

In another terminal, validate and start Jul from this directory:

```bash
go run ../../cmd/jul -check -config jul.toml
go run ../../cmd/jul -config jul.toml
```

Exercise the public and authenticated paths. `curl -u` prompts for the
password when none is included in the argument:

```bash
curl -i -H 'Host: quota.example.test' http://127.0.0.1:8080/public/
curl -i -u alice -H 'Host: quota.example.test' http://127.0.0.1:8080/account/
curl -i -u integration -H 'Host: quota.example.test' http://127.0.0.1:8080/export/
```

Send repeated requests to observe `429 Too Many Requests` and the upward-rounded
`Retry-After` header. The public route cannot select either identity-reserved
class. Basic credentials are verified by Jul before the corresponding account
class is selected. If an account is not explicitly mapped, it uses the public
fallback.

When the managed Admin API is enabled, use the same complete TOML candidate
with `jul plan --endpoint <admin-endpoint> --config jul.toml` and then the
reviewed `jul apply` workflow. The server validates and publishes the
candidate; the Console's guided editor submits typed preview/publish operations
against that same server-owned configuration. File-owned configuration remains
read-only remotely.

## Limits

This example demonstrates local authentication and request accounting only. It
does not qualify production latency, HTTP/2 or HTTP/3, real authentication
providers, database concurrency, overload protection, or managed-edge policy.
Keep account credentials outside version control and use TLS and a suitable
identity provider for deployments beyond a local fixture.

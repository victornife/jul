# Jul Console v2 Recovery and Excellence Plan

> Purpose: Convert the current Console v2 implementation from a technical substrate into the self-explanatory operations cockpit promised by `docs/specs/console-v2.md` and `docs/reviews/jul_console_v2_spike.md`.
>
> Audience: Jul maintainers, principal engineers, frontend engineers, backend/API owners, product reviewers, and UX reviewers.
>
> Status: Action plan derived from the Console v2 implementation audit.
>
> Primary principle: The Console must make Jul easier to understand, safer to operate, and friendlier than traditional HTTP servers, reverse proxies, and API gateways. There is no negotiation on simplicity.

---

## 1. Executive Summary

Console v2 has made meaningful technical progress. The repository now has the right substrate:

- React, TypeScript, Vite, and Tailwind foundation.
- Embedded static bundle behind the `console` build tag.
- v2 API routes registered in the admin server.
- Structured backend projections for routes, apps, TLS, security, and traffic controls.
- Config validation, diff, apply, and rollback mechanics.
- SSE event stream.
- CI frontend job with typecheck, lint, test, build, size gate, drift guard, and embedded Go build.

That is the right foundation.

But the Console is still not the product promised by the spike. It is currently closer to:

```text
read-only projections + raw TOML editor + minimal event stream
```

It needs to become:

```text
live operations view + guided configuration + effective config + safe apply + observable impact + rollback
```

The most urgent gaps are:

1. The v2 UI no longer exposes enough real-time request information.
2. The UI does not show request types, origins, top failing routes, or useful traffic shape.
3. Routes, apps/upstreams, TLS, compression, cache, rate limits, and auth are mostly not editable through guided UI.
4. The wizard still only supports basic serve/proxy flows.
5. There is no dark/light mode toggle.
6. There is no top-bar vs left-sidebar menu preference.
7. Diff and blast-radius analysis are too shallow.
8. Observability is only an event list, not an operational diagnosis surface.
9. Screens are functional but not yet self-explanatory.

This plan fixes those gaps in phases.

---

## 2. Product North Star

Console v2 must become the simplest serious edge-server control plane.

The user should feel:

> I understand what this server is doing. I know what will happen if I change it. I can apply safely. I can undo. I am in control.

The Console is not just a dashboard. It is the place where the user:

1. Understands the running server.
2. Sees whether traffic is healthy.
3. Understands routes, apps, TLS, security, and traffic controls.
4. Changes configuration safely.
5. Previews the generated TOML.
6. Validates the change.
7. Sees a clear diff and blast-radius summary.
8. Applies through hot reload.
9. Confirms runtime impact.
10. Rolls back if needed.

Every major screen must answer:

1. What is this?
2. Is it enabled?
3. Where is it used?
4. Is anything risky or misconfigured?
5. What can I do next?

If a screen does not answer those questions, it is not done.

---

## 3. Current State Summary

### 3.1 Implemented

| Area | State |
| --- | --- |
| React/Vite/Tailwind substrate | Implemented |
| TypeScript frontend app | Implemented |
| Embedded assets via Go build tag | Implemented |
| v2 admin routes | Implemented |
| Route/app/TLS/security/traffic projections | Implemented, shallow |
| Config validation | Implemented |
| Config diff | Implemented, shallow |
| Config apply | Implemented |
| History/rollback | Implemented |
| SSE event stream | Implemented |
| CI frontend checks | Implemented |
| CodeMirror lazy editor | Implemented |
| Wizard | Implemented, minimal serve/proxy only |

### 3.2 Missing or Incomplete

| Area | Gap |
| --- | --- |
| Overview | Does not show full live traffic cards |
| Real-time request information | Not surfaced properly |
| Request types | Not exposed |
| Request origins | Not exposed |
| Routes | Read-only; no guided edit |
| Apps/upstreams | Mostly read-only |
| TLS | Read-only cert inventory |
| Security | Shallow posture only |
| Traffic Controls | Read-only compression/rate/cache |
| Observability | SSE event list only |
| Wizard | Too limited |
| Diff | Not deep enough |
| Human validation errors | Heuristic and incomplete |
| Dark/light mode | Missing |
| Top/left navigation preference | Missing |
| Self-explanatory UX copy | Incomplete |
| Route-level metrics | Missing |
| Top failing routes | Missing |
| Logs/request samples | Missing |

---

## 4. Delivery Strategy

Do not deliver this as one large Console rewrite. The substrate already exists. The right approach is incremental hardening around user value.

The phases are:

1. Phase 1 - Make the Console truthful again.
2. Phase 2 - Make routes and apps editable.
3. Phase 3 - Make traffic controls real.
4. Phase 4 - Make the UI self-explanatory and polished.
5. Phase 5 - Add operational depth.
6. Phase 6 - GA hardening and soak.

Each phase must leave the Console better than before, with clear acceptance criteria.

---

# Phase 1 - Make the Console Truthful Again

## Goal

Restore the user's ability to understand what Jul is doing right now.

The current v2 Console has status rows and an event stream, but it does not sufficiently show real-time request behavior. This is a serious product gap because an operations cockpit without traffic truth is only a configuration viewer.

## Milestone 1.1 - Live Overview Cards

### Build

Add Overview cards for:

- uptime,
- total requests,
- requests per second,
- in-flight requests,
- active connections,
- error rate,
- status class breakdown,
- latency average,
- p50 latency,
- p95 latency,
- p99 latency,
- cache hit ratio,
- cache events,
- active feature status summary.

### Backend

Use the existing `StatsSnapshot` fields as the first source of truth:

```text
available
uptimeSeconds
requestsTotal
requestsPerSec
inFlight
connections
errorRate
statusClasses
latencyAvgMs
latencyP50Ms
latencyP95Ms
latencyP99Ms
cacheHitRatio
cacheEvents
```

If `/api/runtime/overview` does not expose these cleanly, extend it.

### Frontend

Update `OverviewPanel` to render:

- summary cards,
- latency panel,
- status-class bars,
- cache panel,
- operational warnings,
- grouped feature status below the live dashboard.

Polling interval:

```text
2 seconds
```

Do not make this SSE-first yet. Polling is simpler and sufficient for numeric cards.

### Acceptance Criteria

- User can answer: Is Jul receiving traffic?
- User can answer: Are requests failing?
- User can answer: Is latency bad?
- User can answer: Is cache helping?
- User can answer: Is traffic increasing or flat?

---

## Milestone 1.2 - Frontend Sampling and Sparklines

### Build

Keep a rolling in-memory sample history in the frontend for:

- requests per second,
- error rate,
- p95 latency,
- in-flight requests,
- cache hit ratio.

Render simple token-styled SVG sparklines.

### Constraints

- No charting library.
- No large dependency.
- No canvas unless necessary.
- Keep within the bundle size budget.

### Acceptance Criteria

- User sees recent trend, not only a single number.
- Trend rendering does not require backend storage.
- Dashboard remains fast and lightweight.

---

## Milestone 1.3 - Request Method and Status Breakdown

### Build

Expose request method breakdown:

- GET,
- POST,
- PUT,
- PATCH,
- DELETE,
- OPTIONS,
- HEAD,
- other.

Expose status breakdown:

- 2xx,
- 3xx,
- 4xx,
- 5xx,
- optionally top exact status codes.

### Backend

Current metrics record method, host, and code. Do not add unbounded labels.

Add a bounded projection from Prometheus gather data:

```go
type RequestBreakdown struct {
    Methods       map[string]float64 `json:"methods"`
    StatusClasses map[string]float64 `json:"status_classes"`
    StatusCodes   map[string]float64 `json:"status_codes,omitempty"`
}
```

### Frontend

Show:

- method distribution,
- status distribution,
- preflight OPTIONS count,
- warning if 4xx or 5xx is above threshold.

### Acceptance Criteria

- User can understand the type of traffic.
- User can spot unusual method mix.
- CORS preflight visibility exists via OPTIONS count.

---

## Milestone 1.4 - Basic Host and Origin Visibility

### Build

Add a safe Traffic Sources panel.

Start with:

- top hostnames,
- top `Origin` headers,
- top `Referer` hosts,
- CORS preflight count,
- same-origin vs cross-origin estimate.

### Critical Implementation Rule

Do not expose raw unbounded values as Prometheus labels.

Instead implement a bounded in-memory top-N rollup:

```go
type TopNCounter struct {
    MaxKeys int
    Window  time.Duration
    Values  map[string]counter
}
```

Normalize:

- lower-case host,
- strip path and query,
- cap length,
- replace empty with `(none)`,
- group overflow into `(other)`.

Privacy:

- do not store full URLs,
- do not store query strings,
- do not store tokens,
- do not expose cookies,
- do not expose Authorization headers.

### Acceptance Criteria

- User can answer: Which origins are calling me?
- User can debug CORS-related traffic.
- Cardinality remains bounded.
- Sensitive information is not exposed.

---

## Milestone 1.5 - Remove Unsafe SSE Defaults

### Build

Tighten `/api/events`.

Current concerns:

- wildcard CORS is unnecessary for a same-origin embedded Console,
- query-token fallback is leak-prone and not needed by the React client because it uses fetch streaming with Authorization headers.

### Change

- Remove `Access-Control-Allow-Origin: *` from `/api/events`.
- Remove query-token fallback for v2 by default.
- Keep any legacy fallback behind explicit config only, if truly needed.
- Document that v2 uses Authorization header over fetch streaming.

### Acceptance Criteria

- `/api/events` remains same-origin by default.
- Tokens are not encouraged in query strings.
- Event stream continues working from the React app.

---

# Phase 2 - Make Routes and Apps Editable

## Goal

Routes and apps/upstreams are Jul's core mental model. If users cannot add and edit them safely through the Console, the Console is not fulfilling its purpose.

---

## Milestone 2.1 - Route Detail Drawer

### Build

Clicking a route opens a detail drawer.

Show:

- server/listener,
- hostnames,
- path match,
- match type,
- action,
- target,
- auth status,
- cache status,
- compression status,
- rate-limit status,
- TLS status,
- upstream health if proxied,
- generated TOML fragment,
- request metrics if available,
- warnings.

### UX Copy

Add beginner-friendly explanations:

> A route tells Jul what to do when an incoming request matches a host and path.

> This route proxies traffic to an app. Jul receives the request, applies edge rules, and forwards it to the selected upstream.

### Acceptance Criteria

- User understands what a route does without reading TOML.
- User can see effective config, not just raw config.
- User sees warnings before editing.

---

## Milestone 2.2 - Route Editor MVP

### Build

Add guided route creation and editing for:

- static route,
- reverse proxy route,
- redirect,
- deny,
- basic return response.

Fields:

- server/listener,
- hostnames,
- path,
- match type,
- action,
- target,
- auth toggle,
- cache toggle,
- compression toggle,
- rate-limit toggle.

### Apply Model

Route editor must not write directly.

It must generate a candidate config and pass through:

```text
Generate TOML
-> Validate
-> Diff
-> Explain impact
-> Apply and reload
-> Runtime confirmation
-> Rollback available
```

### Acceptance Criteria

- User can add `/api/` reverse proxy route without editing raw TOML.
- User can edit route target safely.
- User can disable a route.
- User sees generated TOML.
- Invalid route does not replace running config.

---

## Milestone 2.3 - Route Testing

### Build

Add a Test route action.

User enters:

- method,
- path,
- host,
- optional headers.

Backend returns:

- matched server,
- matched route,
- action,
- target,
- applied auth/cache/compression/rate-limit flags,
- warnings.

This can initially be a dry-run matcher, not a real upstream request.

### Acceptance Criteria

- User can understand how Jul will route a request before sending real traffic.
- Debugging wrong path/host matching becomes easier.

---

## Milestone 2.4 - App / Upstream Detail View

### Build

Clicking an app opens detail.

Show:

- app name,
- upstream technical name,
- strategy,
- backend list,
- backend health,
- in-flight per backend,
- health-check settings,
- discovery source,
- routes using this app,
- recent failures if available.

### Acceptance Criteria

- User can understand which routes depend on an app.
- User can identify unhealthy backends.
- User can see whether load balancing is configured.

---

## Milestone 2.5 - App / Upstream Editor MVP

### Build

Allow:

- create app/upstream,
- rename app/upstream if safe,
- add backend,
- remove backend,
- edit weight,
- choose strategy,
- enable/disable health checks,
- configure health-check path/interval if supported.

### Supported Presets

Friendly app presets:

- Express / Node.js,
- Apollo GraphQL,
- FastAPI,
- Django / Flask,
- Go HTTP app,
- generic HTTP app,
- gRPC backend.

Presets only influence copy and defaults. They should not create framework-specific magic.

### Acceptance Criteria

- User can put an app behind Jul from the UI.
- User can add multiple backend instances.
- User can configure basic load balancing.
- User can apply changes through the validated path.

---

# Phase 3 - Make Traffic Controls Real

## Goal

Compression, cache, rate limits, timeouts, body limits, and retries must become understandable and editable. These features are core to Jul as an edge server and API gateway.

---

## Milestone 3.1 - Compression Editor

### Build

Add guided compression controls:

- global enable/disable,
- encoder selection,
- minimum size,
- content types,
- route-level override,
- preview effective setting by route.

### Risk Warnings

Warn when:

- compression is enabled on authenticated JSON responses,
- compression is applied to already-compressed assets,
- compression may increase CPU load.

### UX Copy

> Compression reduces response size before sending data to clients. It usually helps HTML, CSS, JavaScript, JSON, and SVG. It usually does not help images, video, or ZIP files.

### Acceptance Criteria

- User can change compression while server is running.
- User sees which routes are compressed.
- User sees generated TOML.
- User sees risk warnings.

---

## Milestone 3.2 - Cache Editor

### Build

Controls:

- enable/disable cache,
- memory max,
- disk cache path,
- default TTL,
- stale behavior if supported,
- route-level cache toggle,
- cache purge action,
- cache hit ratio display.

### Risk Warnings

Warn when:

- cache is enabled on authenticated routes,
- cache is enabled on POST routes,
- TTL is high for dynamic data,
- disk path is missing or unwritable if backend can detect.

### Acceptance Criteria

- User can configure cache safely.
- User understands risk of caching private data.
- User can see cache effectiveness.

---

## Milestone 3.3 - Rate Limit Editor

### Build

Controls:

- enable/disable,
- key type: IP, header, JWT claim if supported,
- rate,
- burst,
- scope: global or route,
- response behavior.

### Observability

Show:

- rate-limited request count,
- key kind breakdown,
- affected routes.

### Acceptance Criteria

- User can configure rate limiting without raw TOML.
- User understands what is being limited.
- User can see whether rate limiting is active.

---

## Milestone 3.4 - Timeout, Retry, Body Limit Editor

### Build

Controls:

- read timeout,
- write timeout,
- idle timeout,
- upstream timeout,
- retry count,
- retry conditions,
- body size limit.

### UX Copy

> Timeouts stop Jul from waiting forever for slow clients or backends.

> Retries can help with temporary backend failures, but too many retries can make incidents worse.

### Acceptance Criteria

- User can configure operational safety settings.
- Dangerous values produce warnings.
- Generated TOML is visible.

---

# Phase 4 - Make the UI Self-Explanatory and Polished

## Goal

Make the Console feel like a friendly product, not a raw admin surface.

---

## Milestone 4.1 - Dark / Light Mode

### Build

Add:

- theme provider,
- light token set,
- dark token set,
- system preference detection,
- manual toggle,
- persisted preference.

Preference values:

```ts
type ThemePreference = "system" | "light" | "dark";
```

### Token Rules

Do not scatter raw colors across components.

Use semantic tokens:

- background,
- surface,
- surface elevated,
- border,
- text,
- muted,
- accent,
- success,
- warning,
- danger.

### Acceptance Criteria

- User can toggle light/dark/system.
- Theme persists across reloads.
- All main panels are readable in both themes.
- No raw dark-only assumptions remain.

---

## Milestone 4.2 - Top Bar / Left Sidebar Preference

### Build

Add layout preference:

```ts
type NavigationLayout = "top" | "sidebar";
```

Modes:

- top navigation,
- left sidebar,
- collapsed sidebar,
- responsive mobile fallback.

Persist locally.

### UX Rules

Top nav is good for small installations.

Left sidebar is better as the Console grows.

The preference should be easy to find in a small View or Preferences menu.

### Acceptance Criteria

- User can choose top-bar or left-sidebar navigation.
- Preference persists.
- Sidebar works with all current routes.
- Mobile layout remains usable.

---

## Milestone 4.3 - Self-Explanatory Screen Headers

### Build

Every panel gets a header block:

- title,
- one-paragraph explanation,
- current state summary,
- primary action,
- docs/help link if available.

Example for Routes:

> Routes decide what Jul does with incoming requests. A route can serve files, proxy to an app, redirect, deny, or connect to a protocol adapter.

Example for Traffic Controls:

> Traffic controls shape how Jul handles requests and responses: compression, caching, rate limits, timeouts, retries, and body limits.

### Acceptance Criteria

- No screen starts with only a table.
- A beginner knows what the screen is for.
- Primary action is visible.

---

## Milestone 4.4 - Empty States That Teach

### Build

Replace empty states.

Bad:

```text
No upstream pools configured.
```

Good:

```text
No apps are configured yet. Add an app when you want Jul to send traffic to a backend service such as Express, Apollo, FastAPI, Django, or a Go API.
```

### Acceptance Criteria

- Empty states explain what the concept means.
- Empty states include the next action.
- Empty states do not blame the user.

---

## Milestone 4.5 - Consistent Component System

### Build

Create and use shared primitives:

- Button,
- Input,
- Select,
- Switch,
- Textarea,
- Card,
- Table,
- Drawer,
- Modal,
- Tabs,
- Tooltip,
- Badge,
- StatusPill,
- Alert,
- EmptyState,
- DiffView,
- WizardStepper,
- PreferenceMenu.

### Acceptance Criteria

- Screens look coherent.
- Dangerous actions are visually consistent.
- Disabled/loading/error states are standardized.
- Tailwind utility usage is mostly encapsulated in components.

---

# Phase 5 - Add Operational Depth

## Goal

Help users diagnose real production behavior.

---

## Milestone 5.1 - Request Samples

### Build

Add bounded recent request samples.

Fields:

- time,
- method,
- path,
- host,
- route ID,
- status,
- duration,
- upstream,
- cache state,
- compressed yes/no,
- rate-limited yes/no,
- origin host if present,
- user-agent family if available.

### Privacy Rules

- no cookies,
- no Authorization,
- no query strings by default,
- redact configured sensitive headers,
- cap sample size,
- ring buffer only.

### Acceptance Criteria

- User can inspect recent behavior.
- Sensitive data is not exposed.
- Sample stream is bounded.

---

## Milestone 5.2 - Top Failing Routes

### Build

Panel showing:

- route,
- 4xx count,
- 5xx count,
- p95 latency,
- upstream health,
- last known error class.

### Acceptance Criteria

- User can identify which route is failing.
- User can jump from failing route to route detail.

---

## Milestone 5.3 - Origin and CORS Diagnostics

### Build

Panel showing:

- top origins,
- top referers,
- OPTIONS/preflight count,
- blocked/failed CORS if detectable,
- public vs authenticated traffic estimates.

### Acceptance Criteria

- User can debug frontend integration problems.
- User can see suspicious origin patterns.

---

## Milestone 5.4 - Config and Runtime Event Timeline

### Build

Merge events into a useful timeline:

- validation attempts,
- apply attempts,
- reload success/failure,
- rollback,
- certificate renewal,
- upstream health changes,
- high error-rate warning,
- rate-limit spikes.

### Acceptance Criteria

- User can answer: Did a config change cause this?
- Events link to config history where relevant.

---

## Milestone 5.5 - Upstream Health History

### Build

Track:

- backend up/down transitions,
- flapping,
- recent probe latency,
- last failure time,
- last success time.

### Acceptance Criteria

- User can diagnose unstable backends.
- Apps panel becomes operational, not just descriptive.

---

## Milestone 5.6 - Certificate Renewal History

### Build

Track:

- last renewal attempt,
- last success,
- last error,
- next expiry,
- issuer,
- ACME staging/prod indicator.

### Acceptance Criteria

- User can diagnose certificate problems before outage.
- Expiring cert warnings are actionable.

---

# Phase 6 - GA Hardening and Soak

## Goal

Move Console v2 from implemented to trusted.

---

## Milestone 6.1 - Backend API Tests

### Required Tests

- `/api/runtime/overview`
- `/api/routes`
- `/api/apps`
- `/api/tls`
- `/api/security`
- `/api/traffic-controls`
- `/api/config/validate`
- `/api/config/diff`
- `/api/config/apply`
- `/api/config/history`
- `/api/config/rollback`
- `/api/wizard/generate`
- `/api/events`

### Test Scenarios

- unauthenticated request,
- authenticated request,
- invalid method,
- missing dependencies,
- nil hooks,
- bad TOML,
- valid TOML,
- invalid snapshot ID,
- rollback path traversal attempt,
- SSE connect/disconnect.

### Acceptance Criteria

- All endpoints have tests.
- Auth is verified.
- Error shapes are stable.

---

## Milestone 6.2 - Frontend Tests

### Required Tests

- Overview renders live cards.
- Routes render route cards and details.
- Route editor generates TOML.
- App editor generates TOML.
- Compression editor generates TOML.
- Wizard app flow generates TOML.
- Config validation errors render.
- Diff renders additions/removals/modifications.
- Apply confirmation works.
- History rollback confirmation works.
- Theme toggle persists.
- Layout toggle persists.

### Acceptance Criteria

- Tests cover behavior, not implementation details.
- Critical flows do not regress silently.

---

## Milestone 6.3 - Accessibility Pass

### Required

- keyboard navigation,
- visible focus states,
- modal focus trap,
- drawer focus return,
- aria labels for icon buttons,
- table headers,
- color contrast in both themes,
- prefers-reduced-motion support.

### Acceptance Criteria

- No critical accessibility blockers.
- Console is usable without a mouse.

---

## Milestone 6.4 - Responsive Pass

### Required Breakpoints

- laptop,
- small laptop,
- tablet,
- narrow mobile fallback.

### Acceptance Criteria

- Top nav wraps gracefully.
- Sidebar collapses.
- Tables are scrollable or cardified.
- Config editor remains usable.

---

## Milestone 6.5 - Soak Plan

### Run

Run Console v2 with a realistic config for:

- static route,
- reverse proxy route,
- Apollo/Express route,
- FastAPI/Python route,
- TLS,
- compression,
- cache,
- rate limit,
- auth,
- multiple upstreams.

### Soak Tracks

During soak:

- edit route,
- edit upstream,
- change compression,
- validate invalid config,
- apply valid config,
- rollback,
- stream events,
- observe traffic,
- force upstream failure,
- recover upstream,
- expire/mock certificate warning.

### Acceptance Criteria

- No broken reload path.
- No stale UI after apply.
- No unsafe config write.
- No unbounded memory growth in event/request sample buffers.
- No token leaks.
- No major layout issues.
- Operators can complete core tasks without docs.

---

# 5. Detailed Backlog

## P0 Backlog

| ID | Task | Owner Area | Impact |
| --- | --- | --- | --- |
| P0-01 | Render StatsSnapshot in Overview | Frontend/API | Critical |
| P0-02 | Add frontend sparklines | Frontend | High |
| P0-03 | Add request method/status breakdown | Observability/API | High |
| P0-04 | Add safe top-N origin projection | Observability/API | High |
| P0-05 | Tighten SSE auth/CORS | Admin/API | High |
| P0-06 | Add route detail drawer | Frontend | Critical |
| P0-07 | Add Route Editor MVP | Frontend/API | Critical |
| P0-08 | Add App Editor MVP | Frontend/API | High |
| P0-09 | Expand diff to location/upstream changes | Admin/API | Critical |
| P0-10 | Add Compression Editor MVP | Frontend/API | High |
| P0-11 | Expand wizard: app behind Jul | Frontend/API | Critical |
| P0-12 | Add self-explanatory headers to major screens | UX/Frontend | High |

## P1 Backlog

| ID | Task | Owner Area | Impact |
| --- | --- | --- | --- |
| P1-01 | Theme provider and dark/light toggle | Frontend | Medium |
| P1-02 | Top/sidebar navigation preference | Frontend | Medium |
| P1-03 | TLS enablement flow | Frontend/API | High |
| P1-04 | ACME configuration flow | Frontend/API | High |
| P1-05 | Cache editor | Frontend/API | High |
| P1-06 | Rate-limit editor | Frontend/API | High |
| P1-07 | Timeout/body-limit editor | Frontend/API | Medium |
| P1-08 | Public route/security posture map | Security/API | High |
| P1-09 | Observability event timeline | Frontend/API | High |
| P1-10 | Human validation error coverage expansion | Admin/API | High |
| P1-11 | Empty states rewrite | UX/Frontend | Medium |
| P1-12 | Shared component system | Frontend | Medium |

## P2 Backlog

| ID | Task | Owner Area | Impact |
| --- | --- | --- | --- |
| P2-01 | Request samples ring buffer | Observability/API | High |
| P2-02 | Top failing routes panel | Observability/Frontend | High |
| P2-03 | CORS diagnostics | Observability/Frontend | Medium |
| P2-04 | Upstream health history | Upstream/API | Medium |
| P2-05 | Certificate renewal history | TLS/API | Medium |
| P2-06 | Log tail | Observability/API | Medium |
| P2-07 | gRPC wizard | Frontend/API | Medium |
| P2-08 | WebSocket/SSE route hints | Frontend/API | Medium |
| P2-09 | Sanitized support bundle | Admin/API | Medium |
| P2-10 | Visual regression tests | QA | Medium |

---

# 6. Implementation Guidelines

## 6.1 Backend Truth Rule

The frontend may help users create drafts, but the backend is the source of truth.

Backend owns:

- TOML parsing,
- config validation,
- effective config projection,
- diff,
- apply,
- reload,
- rollback,
- runtime metrics,
- event stream,
- sensitive security checks.

Frontend owns:

- form state,
- draft state,
- display,
- explanation,
- generated candidate review,
- progressive disclosure.

Do not implement business validation only in TypeScript.

---

## 6.2 Apply Flow Rule

Every mutating flow must use the same pipeline:

```text
Edit
-> Generate candidate config
-> Validate
-> Diff
-> Explain impact
-> Confirm
-> Apply
-> Hot reload
-> Confirm runtime state
-> Offer rollback
```

No screen gets a special write path.

---

## 6.3 Effective Config Rule

Users care about what is active, not only what is written.

Show effective settings for:

- route auth,
- route cache,
- route compression,
- route rate limit,
- TLS,
- upstream target,
- body limit,
- timeout,
- retry behavior.

When inheritance exists, show where the value comes from:

```text
Compression: enabled globally
Route override: disabled
Effective value: disabled
```

---

## 6.4 Bounded Observability Rule

Do not create high-cardinality Prometheus labels for:

- path with IDs,
- full URLs,
- query strings,
- Origin values,
- Referer values,
- User-Agent values,
- IP addresses,
- JWT subjects.

Use bounded in-memory top-N rollups and normalized values.

---

## 6.5 Security and Privacy Rule

Never expose:

- Authorization headers,
- cookies,
- raw tokens,
- private key material,
- full query strings,
- request bodies,
- secrets in config,
- unredacted env values.

Every support/export/request-sample feature must redact.

---

## 6.6 UI Simplicity Rule

A screen is not complete if it only shows a table.

Every screen needs:

- explanation,
- state summary,
- primary action,
- risk/warning if relevant,
- detail view,
- empty state,
- next step.

---

## 6.7 Dependency Rule

Do not add heavy frontend dependencies without ADR review.

Avoid for now:

- charting libraries,
- component libraries,
- Monaco,
- Redux/global stores,
- web terminal,
- AI assistant dependencies,
- external fonts/CDNs.

Prefer:

- small in-repo components,
- custom SVG charts,
- TanStack Query,
- Zod,
- CodeMirror lazy-loaded only.

---

# 7. Suggested File-Level Work Plan

## Backend

Likely touched files:

```text
internal/admin/server.go
internal/admin/projections.go
internal/admin/diff.go
internal/admin/events.go
internal/admin/humanerrors.go
internal/admin/history.go
internal/admin/wizard.go
internal/observability/metrics.go
internal/observability/stats.go
internal/config/validate.go
```

Potential new files:

```text
internal/admin/request_rollup.go
internal/admin/route_matcher.go
internal/admin/effective_config.go
internal/admin/traffic_projection.go
internal/admin/security_projection.go
internal/admin/console_events_test.go
internal/admin/console_v2_api_test.go
internal/observability/topn.go
internal/observability/request_samples.go
```

## Frontend

Likely touched files:

```text
internal/admin/ui/src/app/Layout.tsx
internal/admin/ui/src/app/providers.tsx
internal/admin/ui/src/api/client.ts
internal/admin/ui/src/styles/globals.css
internal/admin/ui/src/features/overview/OverviewPanel.tsx
internal/admin/ui/src/features/routes/RoutesPanel.tsx
internal/admin/ui/src/features/apps/AppsPanel.tsx
internal/admin/ui/src/features/tls/TLSPanel.tsx
internal/admin/ui/src/features/security/SecurityPanel.tsx
internal/admin/ui/src/features/traffic-controls/TrafficControlsPanel.tsx
internal/admin/ui/src/features/observability/ObservabilityPanel.tsx
internal/admin/ui/src/features/config/ConfigPanel.tsx
internal/admin/ui/src/features/history/HistoryPanel.tsx
internal/admin/ui/src/features/wizard/WizardPanel.tsx
```

Potential new files:

```text
internal/admin/ui/src/app/preferences.tsx
internal/admin/ui/src/app/navigation.tsx
internal/admin/ui/src/components/Button.tsx
internal/admin/ui/src/components/Card.tsx
internal/admin/ui/src/components/Drawer.tsx
internal/admin/ui/src/components/EmptyState.tsx
internal/admin/ui/src/components/MetricCard.tsx
internal/admin/ui/src/components/Sparkline.tsx
internal/admin/ui/src/components/StatusPill.tsx
internal/admin/ui/src/features/routes/RouteEditor.tsx
internal/admin/ui/src/features/routes/RouteDetailDrawer.tsx
internal/admin/ui/src/features/apps/AppEditor.tsx
internal/admin/ui/src/features/traffic-controls/CompressionEditor.tsx
internal/admin/ui/src/features/traffic-controls/CacheEditor.tsx
internal/admin/ui/src/features/traffic-controls/RateLimitEditor.tsx
internal/admin/ui/src/features/overview/TrafficCards.tsx
internal/admin/ui/src/features/observability/RequestSamples.tsx
internal/admin/ui/src/features/observability/TopOrigins.tsx
internal/admin/ui/src/features/wizard/AppBehindJulFlow.tsx
```

---

# 8. Milestone Acceptance Matrix

| Capability | Phase | Exit Criteria |
| --- | --- | --- |
| Live overview metrics | 1 | RPS, errors, latency, in-flight, cache shown |
| Request method/status breakdown | 1 | GET/POST/etc and 2xx/4xx/5xx visible |
| Origin visibility | 1 | Top origins/referers visible with bounded storage |
| SSE security cleanup | 1 | No wildcard CORS/query token by default |
| Route detail | 2 | Route explains action, target, effective config |
| Route editor | 2 | Add/edit/disable route via guided UI |
| App editor | 2 | Add app/backend/strategy via guided UI |
| Better diff | 2 | Route/upstream/TLS/security/traffic changes visible |
| Compression editor | 3 | Change compression and apply safely |
| Cache/rate editors | 3 | Configure cache/rate limit from UI |
| Theme toggle | 4 | Light/dark/system persisted |
| Navigation preference | 4 | Top/sidebar persisted |
| Request samples | 5 | Recent bounded samples visible |
| Top failing routes | 5 | Routes ranked by failures/latency |
| Health/cert history | 5 | Operational histories visible |
| GA hardening | 6 | Tests, a11y, responsive, soak complete |

---

# 9. Definition of Done

A feature is done only when:

1. It works through the validated apply pipeline if mutating.
2. It has backend tests.
3. It has frontend behavior tests if visible in UI.
4. It has human-readable empty, loading, error, and success states.
5. It explains what it does.
6. It shows effective config where relevant.
7. It warns about dangerous changes.
8. It does not leak secrets.
9. It respects the single-binary/no-Node-runtime invariant.
10. It fits within the frontend size budget or has ADR approval.

---

# 10. What Not to Do Yet

Do not spend immediate effort on:

- RBAC/SSO,
- multi-node fleet UI,
- cluster management,
- AI assistant,
- web terminal,
- i18n,
- marketplace,
- MCP/agent control,
- service mesh UI,
- cloud control plane concepts.

These are not the blocking gaps.

The blocking gap is simpler:

> Users need to see live traffic, understand routes/apps, safely change config, and confirm runtime impact.

---

# 11. Recommended First Three PRs

## PR 1 - Live Overview and Traffic Breakdown

Scope:

- extend `/api/runtime/overview` or add a stats projection,
- render live cards in `OverviewPanel`,
- show method/status breakdown,
- add frontend sparklines,
- keep polling simple.

Why first:

- Restores trust in the cockpit.
- Makes v2 visibly useful immediately.

## PR 2 - Route Detail + Route Editor MVP

Scope:

- route detail drawer,
- guided add/edit route form,
- generated TOML preview,
- route-level diff integration,
- apply through config pipeline.

Why second:

- Routes are the core product concept.
- This directly supports the simplicity/friendliness pillar.

## PR 3 - App Editor + Compression Editor

Scope:

- app/upstream creation/editing,
- backend add/remove/weight/strategy,
- compression global/route control,
- warnings and effective config.

Why third:

- Covers the most common real-world use case: putting Express/Apollo/FastAPI behind Jul and shaping response behavior.

---

# 12. Final Recommendation

Treat current Console v2 as Phase 0: substrate landed.

The next effort should not add more broad surface area. It should restore product truth and guided operations:

1. Live Overview and traffic breakdown.
2. Route and app guided editing.
3. Compression and diff hardening.
4. Theme and navigation preferences.
5. Self-explanatory copy and empty states.
6. Operational depth: origins, request samples, top failing routes.

The Console should not become the biggest admin UI.

It should become the clearest.

That is how Jul wins.

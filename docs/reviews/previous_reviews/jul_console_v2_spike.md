# Jul Console v2 Spike: Self-Explanatory Operations and Configuration UI

> **See also (2026-07-01).** Current repository state is tracked in the consolidated
> [Full Repository Audit (2026-07)](jul_full_repository_audit_2026-07-09.md) (single
> source of truth). This document remains a valid historical strategy input.

> **Reviewed — 2026-06-23 ✅** · Status: **Adopted.** This internal spike was
> evaluated and accepted; its operating model and P0/P1/P2 plan are now durable
> decisions in [ADR 0006 — Console v2 stack](../adr/0006-console-v2-stack.md) and
> the [Console v2 execution spec](../specs/console-v2.md). See the
> [reviews & decision log](README.md) for the full adopted / reframed / deferred /
> rejected mapping. The original text is preserved below unchanged.
>
> **Status update — 2026-06-27.** The substrate cutover and the read/edit panels
> have shipped (the console is **GA — soak pending**). The five design rules and
> the information architecture in this spike remain the authoritative product
> standard against which new panels are judged; live delivery tracking now lives
> in the [recovery action plan](previous_reviews/jul_console_v2_recovery_action_plan.md#console-cockpit--remaining-work-phase-4-backlog)
> and the [roadmap](../roadmap/README.md).

---

## Purpose

This spike defines how Jul's Admin Console should evolve from a useful admin panel into a self-explanatory operations cockpit.

The goal is not to make Jul look like every other infrastructure dashboard. The goal is to make Jul easier to understand, safer to operate, and friendlier than traditional HTTP servers, reverse proxies, and API gateways.

The Console must let users monitor the running server, understand what is active, safely change configuration, validate those changes, apply them through hot reload, and roll back if needed.

This includes configuration for routes, apps/upstreams, TLS, certificates, compression, caching, rate limiting, authentication, and other operational settings.

The Console should be excellent for beginners without limiting experts.

## Product principle

The Console is not just a dashboard.

The Console is the place where a user understands, changes, validates, applies, observes, and safely rolls back a running Jul server.

The standard is simple:

> A new user should be able to open the Console, understand what Jul is doing, make a safe configuration change, see the impact, and recover if something goes wrong without reading raw TOML first.

The Console must protect Jul's core product pillars:

- **Friendly:** users understand what they are looking at.
- **Simple:** common tasks are guided and obvious.
- **Safe:** no change is applied without validation and rollback.
- **Live:** configuration changes can be applied while the server keeps running.
- **Transparent:** every feature explains what it does, why it matters, and what risks exist.
- **Lean:** no unnecessary platform bloat, no heavyweight dependency unless it clearly improves the product.

There is no negotiation on simplicity.

If a screen requires infrastructure expertise to understand, the screen is not done.

## Spike objective

Run a focused spike to redesign Jul's Admin Console into a self-explanatory operations and configuration experience.

The spike must answer:

1. What does Jul currently expose in the Console?
2. What can the user safely change while the server is running?
3. What should be editable through guided UI instead of raw TOML?
4. What needs to remain advanced/raw for now?
5. How should validation, preview, diff, apply, reload, and rollback work?
6. How does Jul compare with Nginx UI?
7. Which gaps are worth closing now?
8. Which gaps are not worth closing because they would make Jul less simple?
9. What is the minimum excellent version of Console v2?
10. What should be deferred until fleet, cloud, or team use cases exist?

The deliverable is not only a mockup. The deliverable is a clear operating model for the Console.

## Current baseline

Jul already has the correct foundation:

- live metrics dashboard,
- runtime capability status,
- upstream health,
- certificate inventory,
- configuration editing,
- config history,
- rollback,
- setup wizard,
- validated apply path,
- hot reload after config changes,
- single embedded UI,
- no external runtime dependency.

That is a strong base.

But the current experience still feels closer to an admin utility than a world-class operations interface.

The main gap is not capability.

The main gap is explanatory design.

A great Console should not only show information. It should answer:

- What is running?
- Is it healthy?
- What changed?
- What is risky?
- What can I safely do next?
- Why did validation fail?
- What will happen if I apply this?
- How do I undo it?
- Which users, routes, apps, or certificates are affected?

## Target experience

The user opens the Console and sees one clear message:

> Here is what Jul is serving, what is healthy, what needs attention, and what you can safely change.

The Console should be organized around user goals, not internal implementation.

The main navigation should be:

1. **Overview**
2. **Routes**
3. **Apps / Upstreams**
4. **TLS & Certificates**
5. **Security**
6. **Traffic Controls**
7. **Observability**
8. **Configuration**
9. **History**
10. **Setup Wizard**

Each section must support three levels of user depth:

- **Beginner view:** plain explanation, safe defaults, guided actions.
- **Operator view:** concrete status, warnings, validation, impact.
- **Expert view:** raw TOML, diff, advanced fields, exact error details.

Do not make users choose between a toy UI and raw config.

The correct model is progressive disclosure.

## Design rule: every screen must answer five questions

Every major Console screen must answer:

1. **What is this?**  
   Example: Compression makes text responses smaller before sending them to users.

2. **Is it enabled?**  
   Example: Enabled globally. gzip active. Brotli not compiled in this build.

3. **Where is it used?**  
   Example: Applied to four routes: `/`, `/assets`, `/api/products`, `/docs`.

4. **Is anything risky or misconfigured?**  
   Example: Compression is enabled for authenticated JSON responses. Review secret-leakage risk before caching or compressing sensitive data.

5. **What can I do next?**  
   Example: Edit compression settings, disable for this route, view generated TOML, apply and reload.

If a screen does not answer these questions, it is not self-explanatory.

## Console information architecture

### Overview

Purpose: one page that explains the current running server.

Must show:

- server status,
- uptime,
- active listeners,
- total routes,
- active upstream pools,
- TLS status,
- certificates expiring soon,
- current config version,
- last reload result,
- active warnings,
- request rate,
- error rate,
- p95 and p99 latency,
- in-flight requests,
- cache hit ratio,
- rate-limited requests,
- top failing routes.

The Overview should include a "What changed recently?" panel:

- last config apply time,
- who or what applied it,
- validation result,
- reload result,
- rollback availability.

For now, "who" can be "admin token user" or "local admin session"; full users and RBAC can wait.

### Routes

Purpose: help users understand how traffic is routed.

Routes are the heart of Jul.

A route view should show:

- domain or server block,
- listen address,
- match type: exact, prefix, regex,
- path,
- action: static, proxy, gRPC, FastCGI/uWSGI, redirect, deny,
- auth status,
- compression status,
- cache status,
- rate limit status,
- upstream target,
- last errors,
- request count,
- p95 latency,
- status-code breakdown.

Beginner-friendly copy:

> A route tells Jul what to do when a request matches a path.

Examples:

- `/` serves static files.
- `/api/` proxies to an Express app.
- `/graphql` proxies to Apollo.
- `/v1/` transcodes REST/JSON to gRPC.
- `/admin` requires authentication.

Required actions:

- Add route.
- Edit route.
- Disable route.
- Test route.
- View generated TOML.
- Apply and reload.
- Roll back.

### Apps / Upstreams

Purpose: explain backend services.

Use **App** as the friendly word and **Upstream** as the technical word.

Example:

> An app is a backend service Jul can send traffic to. In the TOML config, apps are called upstreams.

Show:

- app name,
- backend servers,
- health,
- load-balancing strategy,
- active connections,
- in-flight requests,
- recent failures,
- discovery source,
- last health-check result,
- routes using this app.

Supported flows:

- Add Node/Express app.
- Add Apollo GraphQL app.
- Add Python/FastAPI app.
- Add Django/Flask app.
- Add static app.
- Add gRPC backend.
- Add multiple instances for load balancing.
- Configure strategy: round robin, weighted, least connections.
- Configure health checks.
- Test connection.

### TLS & Certificates

Purpose: make HTTPS understandable and safe.

Show:

- domains,
- certificate source: file or ACME,
- expiration date,
- days remaining,
- issuer,
- SANs,
- renewal mode,
- last renewal status,
- warnings.

Required actions:

- Enable TLS.
- Upload or select cert and key.
- Enable ACME.
- Choose staging or production.
- Set minimum TLS version.
- Redirect HTTP to HTTPS.
- View certificate details.
- Validate cert/key pair.
- Apply and reload.

Copy must explain:

> TLS protects traffic between users and Jul. Without it, browsers will warn users and sensitive data may be exposed.

### Security

Purpose: show access controls and risk posture.

Sections:

- authentication,
- mutual TLS,
- IP allow/deny,
- admin token,
- request body limits,
- security headers,
- WAF when available.

For auth, show:

- which routes require auth,
- method: Basic, JWT, forward-auth,
- JWT issuer and audience,
- JWKS URL status,
- last JWKS refresh,
- token validation errors,
- routes without auth.

For mTLS, show:

- server blocks requiring client certs,
- CA loaded,
- CRL loaded,
- SAN allow-list count,
- rejected handshakes,
- routes requiring certs.

The UI should warn clearly:

> This route is public.

Not every public route is bad. But the operator should always know.

### Traffic Controls

Purpose: central place for compression, caching, rate limits, timeouts, retries, and body limits.

Subsections:

- Compression.
- Cache.
- Rate limiting.
- Timeouts.
- Request body limits.
- Retry/failover behavior.

Compression screen must explain:

> Compression reduces response size. It usually helps HTML, CSS, JavaScript, and JSON. It may not help images or video.

Cache screen must explain:

> Caching can make responses much faster, but private or frequently changing data should not be cached carelessly.

Rate limit screen must explain:

> Rate limiting protects your service from overload and abuse.

Every control must show:

- global setting,
- route-level overrides,
- effective setting,
- risk warning,
- generated TOML.

### Observability

Purpose: help users diagnose behavior.

Show:

- request rate,
- errors by route,
- latency by route,
- upstream health,
- top 4xx,
- top 5xx,
- recent reloads,
- recent validation failures,
- logs when implemented,
- tracing status,
- metrics endpoint status.

Avoid generic charts that look impressive but do not answer questions.

The screen should help answer:

- Is Jul healthy?
- Which route is slow?
- Which upstream is failing?
- Did a config change cause this?
- Are users being rate limited?
- Are certificates close to expiry?

### Configuration

Purpose: safe config editing.

This must not be just a textarea.

It should have three modes:

1. **Guided editor**  
   Route, app, TLS, security, and traffic-control forms.

2. **Generated TOML preview**  
   Shows exactly what the UI will write.

3. **Raw TOML editor**  
   For experts.

Every save must follow the same pipeline:

```text
Edit
-> Validate
-> Show diff
-> Explain impact
-> Apply
-> Hot reload
-> Confirm runtime status
-> Offer rollback
```

Validation errors must be written for humans.

Bad:

```text
invalid upstream ref at servers[0].locations[2].proxy_pass
```

Good:

```text
Route `/api/` points to upstream `api`, but no upstream named `api` exists. Create it or choose an existing app.
```

### History

Purpose: confidence.

History must show:

- config version,
- time,
- summary of changes,
- validation result,
- reload result,
- preview,
- diff,
- rollback action.

A user should trust Jul because Jul makes every change reversible.

### Setup Wizard

Purpose: help new users produce a working config without knowing TOML.

The wizard should ask plain questions.

Step 1:

> What do you want Jul to do first?

Options:

- Serve a folder as a website.
- Put an app behind Jul.
- Add HTTPS to an existing app.
- Load balance multiple app instances.
- Protect an admin/API route.
- Expose a gRPC service.
- Create a local development reverse proxy.

Step 2 depends on the choice.

For "Put an app behind Jul":

- What domain should users visit?
- Where is your app running?
- Is it Node, Python, Go, GraphQL, static, or something else?
- Should Jul use HTTPS?
- Should traffic be compressed?
- Should this route require auth?
- Do you have multiple app instances?
- Do you want health checks?

Final step:

- show explanation,
- show generated TOML,
- validate,
- apply and reload,
- show resulting route status.

The wizard should teach without becoming a tutorial.

## Hot reload operating model

Hot reload is a core promise.

The UI must make the reload lifecycle visible.

### Apply flow

```text
User edits config
-> UI sends draft config
-> Server validates syntax
-> Server validates semantics
-> Server snapshots current config
-> Server writes new config
-> Server reloads atomically
-> Server reports success/failure
-> UI confirms runtime state
```

### Non-negotiable requirements

- Invalid config never replaces the running config.
- Every successful apply creates rollback history.
- Every rollback is also validated.
- The UI shows both config validation and runtime reload result.
- The UI explains what changed.
- The UI warns when a change may interrupt traffic.
- The UI never hides raw TOML from expert users.

### Apply button language

Do not use vague labels like "Save."

Use precise actions:

- Validate.
- Preview diff.
- Apply and reload.
- Roll back to this version.

## What Jul has vs what Nginx UI has

### Jul currently has

- embedded single-binary Console,
- live metrics dashboard,
- runtime capability status,
- upstream health,
- certificate inventory,
- config editing,
- history and rollback,
- setup wizard,
- safe validated apply path,
- hot reload,
- security model around admin token and loopback usage.

This is a strong base.

### Nginx UI has

- richer server statistics,
- automatic config backup,
- version comparison and restore,
- cluster management,
- encrypted config export,
- Let's Encrypt deployment and renewal,
- online Nginx config editing,
- block editor and code editor,
- logs,
- automatic config test and reload,
- web terminal,
- dark mode,
- responsive design,
- AI assistant,
- MCP support for agents,
- internationalization.

### Gaps worth closing now

Close these because they strengthen Jul's pillar of friendliness without bloating the product:

1. Better config diff and change summary.
2. Guided route editor.
3. Guided upstream/app editor.
4. Better TLS/ACME certificate screen.
5. Human validation errors.
6. Logs panel or at least reload/config event log.
7. Responsive layout.
8. Dark/light mode if cheap.
9. Config history comparison.
10. One-click rollback with visible impact.
11. Wizard expansion beyond serve/proxy.
12. Clear health and risk warnings.

### Gaps worth closing later

Defer these until demand proves them:

1. Cluster management.
2. Multi-node mirroring.
3. Encrypted export/import.
4. Web terminal.
5. AI assistant.
6. MCP/agent control.
7. Full RBAC/SSO.
8. Internationalization.

These are useful, but they could pull Jul away from lean simplicity if added too early.

### Gaps not worth copying directly

Do not copy Nginx UI's complexity.

Jul is not Nginx UI.

Jul should not become a generic server-control platform.

Avoid:

- terminal-first operations inside the UI,
- AI assistant before the core UI is excellent,
- cluster controls before Jul has a fleet product,
- too many config abstractions,
- code editor as the primary experience,
- hiding behavior behind magic.

Jul should win by making edge/app/API configuration understandable.

## Critical UX requirements

### 1. The UI must explain effective configuration

Users should not only see what is written.

They should see what is effective.

Example:

```text
Compression: enabled globally
Route /api/private: disabled by override
Route /assets: enabled, gzip + br
```

This is critical because configuration inheritance is where users get lost.

### 2. The UI must distinguish desired state from runtime state

Desired state:

> The config says this upstream exists.

Runtime state:

> The upstream currently has 2 healthy backends and 1 failing backend.

Both matter.

Never mix them.

### 3. The UI must show blast radius before apply

Before applying a change, show:

- routes affected,
- listeners affected,
- upstreams affected,
- whether reload is required,
- whether connections may be impacted,
- rollback availability.

### 4. The UI must be safe by default

Dangerous actions require confirmation.

Examples:

- disable TLS,
- expose admin listener publicly,
- remove auth from a route,
- reduce body limit below common upload needs,
- cache authenticated responses,
- change upstream pool for active route.

### 5. The UI must teach through microcopy

Every complex field should have one sentence of explanation.

Example:

> TTL controls how long cached responses are considered fresh. Longer TTLs are faster but may serve older data.

Do not send beginners to docs for every basic concept.

## Technical spike tasks

### Task 1 - Inventory current admin API

List current endpoints:

- stats,
- status,
- upstreams,
- certs,
- config read,
- config raw apply,
- history,
- rollback,
- wizard.

For each endpoint, document method, request shape, response shape, error shape, auth behavior, mutability, and UI safety.

### Task 2 - Define UI state model

Separate draft config, persisted config, running config, runtime metrics, validation result, reload result, and history snapshot.

This prevents confusing UX.

### Task 3 - Define config editing contract

Create an internal API contract for validate config, generate diff, apply config, reload result, rollback, and wizard generation.

### Task 4 - Design route/app/TLS/compression forms

Start with forms that cover high-value common tasks: add reverse proxy route, add static route, add upstream pool, enable TLS, enable compression, configure rate limit, configure cache, and configure auth.

Do not try to model every TOML option in the first guided UI.

### Task 5 - Redesign wizard

Expand the wizard from two modes to goal-based flows.

Required first flows:

1. Serve a website folder.
2. Put an app behind Jul.
3. Load balance multiple app instances.
4. Add HTTPS.
5. Protect a route.
6. Add GraphQL/Apollo behind Jul.
7. Add Python/FastAPI behind Jul.
8. Expose a gRPC service.

### Task 6 - Add diff view

Diff must show raw TOML diff, human summary, affected routes, and warnings.

### Task 7 - Add config event log

Add a timeline of validation attempts, apply attempts, reload success/failure, rollback, certificate renewal events, and upstream health transitions.

This is more important than a generic terminal.

### Task 8 - Improve certificates panel

Move from inventory to operations.

Add expiry urgency, ACME renewal state, last renewal error, challenge type, staging/production indicator, and action to enable HTTPS.

### Task 9 - Improve status panel

Move from active/off rows to effective capability summaries.

Each capability should open a detail drawer.

### Task 10 - Add UX tests

For every guided flow, test generated TOML, validation error, apply success, rollback, empty state, and dangerous change warning.

## Spike deliverables

The spike should produce:

1. Console v2 information architecture.
2. Wireframes for all main screens.
3. Endpoint inventory.
4. Missing backend API list.
5. Config editing state model.
6. Wizard flow map.
7. Nginx UI gap analysis.
8. P0/P1/P2 delivery plan.
9. Acceptance criteria.
10. Risks and non-goals.

## P0 implementation plan

1. **Routes view** - Add route list and route detail. This is the most important screen.
2. **Apps / Upstreams view** - Rename upstreams into "Apps / Upstreams" in the UI while keeping technical naming visible.
3. **Config diff before apply** - No apply without diff.
4. **Human validation errors** - Translate validation failures into useful messages.
5. **Expanded wizard** - Add the "put an app behind Jul" flow with Express, Apollo, FastAPI, Django, generic HTTP apps, and static folders.
6. **TLS enablement flow** - Make HTTPS setup understandable.
7. **Compression and cache controls** - Expose safe guided controls with warnings.
8. **Config history compare** - History without diff is only half useful.
9. **Post-apply confirmation** - After apply, show what is now running.
10. **Event log** - Show reloads and config operations.

## P1 implementation plan

1. Route-level metrics.
2. Upstream health history.
3. Certificate renewal history.
4. Guided auth editor.
5. Rate limit editor.
6. Responsive polish.
7. Dark/light mode, if cheap and clean.
8. Export sanitized support bundle.

## P2 / deferred

Defer:

- cluster management,
- fleet UI,
- RBAC/SSO,
- web terminal,
- AI assistant,
- MCP,
- i18n,
- plugin marketplace,
- multi-node diff/apply.

These may become important later, but they are not required for the Console v2 spike.

## Acceptance criteria

The spike succeeds only if all of this is true:

1. A beginner can create a working reverse proxy route without editing TOML.
2. A beginner can put Express/Apollo/FastAPI behind Jul using the wizard.
3. A beginner can enable HTTPS safely.
4. A user can change compression while the server is running.
5. A user can edit routes while the server is running.
6. A user can preview generated TOML.
7. A user can see a human-readable diff.
8. A user can apply and hot reload.
9. A user can roll back.
10. A user can understand why validation failed.
11. A user can see which routes, apps, and TLS settings are currently active.
12. A user can tell whether a route is public, authenticated, cached, compressed, rate-limited, or proxied.
13. The UI explains concepts without forcing documentation lookup.
14. The UI does not hide raw TOML from advanced users.
15. The product remains lean.

## Adversarial review questions

Before accepting the spike, ask:

- Are we building a beautiful TOML editor or a true operations cockpit?
- Can a non-infra user understand this screen?
- Does every change have validation, diff, apply, and rollback?
- Are we making dangerous changes too easy?
- Are we exposing too many knobs too early?
- Are we copying Nginx UI features that do not fit Jul?
- Are we preserving Jul's single-binary and lean identity?
- Can this UI explain effective config better than raw TOML?
- Can a user recover from a mistake?
- Would this UI reduce support questions?
- Would this UI make Jul feel friendlier than NGINX, not just prettier?

If the answer is no, the design is not good enough.

## Final product direction

Jul Console should become the simplest serious edge-server control plane.

Not the biggest.

Not the most enterprise-heavy.

Not the most dashboard-dense.

The friendliest.

The user should feel:

> I understand what this server is doing. I know what will happen if I change it. I can apply safely. I can undo. I am in control.

That is the bar.

## Source links

- Jul repository: <https://github.com/victornife/jul>
- Jul Console docs: <https://github.com/victornife/jul/blob/main/docs/console.md>
- Jul admin backend: <https://github.com/victornife/jul/tree/main/internal/admin>
- Jul embedded UI assets: <https://github.com/victornife/jul/tree/main/internal/admin/assets>
- Nginx UI repository: <https://github.com/0xJacky/nginx-ui>
- Nginx UI website: <https://nginxui.com/>

# Jul Console v2 Frontend Stack Recommendation

> **See also (2026-07-01).** Current repository state is tracked in the consolidated
> [Full Repository Audit (2026-07)](jul_full_repository_audit_2026-07-09.md) (single
> source of truth). This document remains a valid historical strategy input.

> **Reviewed — 2026-06-23 ✅** · Status: **Adopted.** This stack recommendation was
> evaluated and accepted; its substrate choice (React/TS/Vite/Tailwind, prebuilt +
> embedded, Node-free runtime) is now a durable decision in
> [ADR 0006 — Console v2 stack](../adr/0006-console-v2-stack.md) and the
> [Console v2 execution spec](../specs/console-v2.md). See the
> [reviews & decision log](README.md) for the full adopted / reframed / deferred /
> rejected mapping. The original text is preserved below unchanged.

---

## Decision

Jul Console v2 should use:

```text
TypeScript
React
Vite
Tailwind CSS
TanStack Query
Zod
CodeMirror 6
Go admin API
go:embed static assets
```

The most important architectural rule is this:

> React is a build-time UI implementation detail. Jul must still ship as a single self-contained Go binary with embedded static assets and no Node.js runtime requirement.

This distinction matters.

Jul should use modern frontend tooling to build a better Console, but users running Jul should not need Node.js, npm, pnpm, a CDN, a separate frontend process, or any external assets.

The product rule should become:

> The Console source is built with Node-based frontend tooling, but Jul releases contain prebuilt embedded assets. Running Jul never requires Node.js, npm, pnpm, a CDN, or a separate frontend server.

This keeps the Console maintainable without compromising Jul's lean single-binary identity.

## Why change the current approach?

The current embedded HTML/CSS/JavaScript approach is elegant for a small v1 Console. It keeps the product simple and avoids frontend build complexity.

However, Console v2 is no longer a tiny admin page.

Console v2 needs to support:

- guided route creation,
- app/upstream editing,
- TLS and ACME flows,
- certificate inspection,
- compression controls,
- cache controls,
- rate-limit controls,
- auth and mTLS posture,
- raw TOML editing,
- validation results,
- human-readable errors,
- config diffs,
- apply-and-hot-reload state,
- rollback,
- setup wizard flows,
- runtime confirmation,
- event history.

That is real application state.

Vanilla JavaScript starts simple but becomes secretly complex as soon as we add multi-step flows, drafts, validation, optimistic state, error states, drawers, dialogs, forms, and diff views.

React plus TypeScript gives us better tools for that complexity.

The goal is not to make Jul trendy.

The goal is to make the Console safe, understandable, and maintainable.

## Recommended stack

| Layer | Choice | Reason |
| --- | --- | --- |
| Language | TypeScript, strict mode | Infrastructure config editing needs strong types and explicit state modeling. |
| UI framework | React | Good fit for complex forms, wizards, drawers, state transitions, and progressive disclosure. |
| Build tool | Vite | Fast development, simple production builds, good TypeScript and React support. |
| Styling | Tailwind CSS | Enables a custom Jul-native design system without importing a heavy admin theme. |
| Server-state | TanStack Query | The Console is mostly server state: stats, routes, apps, certs, validation, reloads, history. |
| Validation | Zod | Useful for UI-side form schemas and API response sanity checks. Go remains the source of truth. |
| Code editor | CodeMirror 6 | Better than a textarea for TOML, validation markers, syntax highlighting, and expert mode. |
| Runtime delivery | Go `embed` | Built JS/CSS are embedded into the Jul binary. |
| Charts | Custom SVG/canvas first | Avoid heavy chart dependencies until real requirements demand them. |

## What not to use

Do not use:

- Next.js,
- Remix,
- SSR,
- React Server Components,
- a separate Node server,
- external CDNs,
- Material UI,
- Ant Design,
- Redux,
- Monaco initially,
- ECharts or ApexCharts initially,
- web terminal,
- AI assistant,
- micro-frontends,
- external fonts.

Jul's Console should not become a web platform inside the product.

Nginx UI can afford a heavier frontend because it is a broader management product. Jul should stay lean and focused.

Copy the useful capability patterns from Nginx UI: validation, diff, restore, reload, certificate operations, logs, and responsive polish.

Do not copy the whole dependency profile.

## Runtime architecture

The runtime model should stay simple:

```text
Browser
  -> Jul admin listener
  -> embedded index.html / JS / CSS
  -> same-origin /api/*
```

There should be no external frontend server.

There should be no CDN dependency.

There should be no runtime package manager.

The built frontend bundle should be embedded with Go:

```go
//go:embed assets/dist/*
var consoleAssets embed.FS
```

The final user experience is still:

```bash
go build -tags console -o jul ./cmd/jul
./jul -config server.toml
open http://127.0.0.1:9090/
```

## Proposed repository structure

```text
internal/admin/
  api.go
  server.go
  assets/
    dist/                 # generated static bundle, embedded in Go
  ui/
    package.json
    vite.config.ts
    tsconfig.json
    tailwind.config.ts
    postcss.config.js
    src/
      main.tsx
      app/
        App.tsx
        routes.tsx
        providers.tsx
      api/
        client.ts
        types.ts
        queries.ts
      components/
        Button.tsx
        Card.tsx
        Drawer.tsx
        Field.tsx
        Badge.tsx
        StatusPill.tsx
        EmptyState.tsx
        ConfirmDialog.tsx
      features/
        overview/
        routes/
        apps/
        tls/
        security/
        traffic-controls/
        observability/
        config/
        history/
        wizard/
      styles/
        globals.css
```

## Build flow

Development:

```bash
pnpm --dir internal/admin/ui install
pnpm --dir internal/admin/ui dev
```

Production asset build:

```bash
pnpm --dir internal/admin/ui build
go generate ./internal/admin
go build -tags console ./cmd/jul
```

The generated bundle should be checked into the repo only if the project wants source builds without requiring Node. Otherwise, release builds must always run the frontend build before compiling the Go binary.

Recommended compromise:

- source code lives in `internal/admin/ui`,
- generated assets live in `internal/admin/assets/dist`,
- release CI verifies the generated assets are current,
- normal `go build -tags console` can work from a release tarball without Node.

## Frontend architecture rule

Use React for UI state, not for business truth.

The backend remains authoritative for:

- parsing TOML,
- validating config,
- generating final config,
- applying config,
- hot reload,
- rollback,
- runtime state,
- security-sensitive checks.

The frontend may help users create a draft, but the backend decides whether the draft is valid.

The apply path should remain:

```text
Draft config
-> backend validate
-> backend returns human errors + machine errors
-> UI shows diff
-> user confirms
-> backend snapshots old config
-> backend writes new config
-> backend hot reloads
-> backend returns runtime result
-> UI confirms active state
```

## TypeScript value

The Console will edit production-edge behavior:

- routes,
- TLS,
- certificates,
- upstreams,
- compression,
- caching,
- rate limiting,
- authentication,
- hot reload.

A typo in a field, a mistaken state transition, or a bad client-side assumption can create a dangerous UX.

TypeScript does not make the product safe by itself, but it helps model states explicitly.

Example:

```ts
type DraftState =
  | { kind: "clean"; config: Config }
  | { kind: "dirty"; draft: Config }
  | { kind: "validating"; draft: Config }
  | { kind: "invalid"; draft: Config; errors: ValidationError[] }
  | { kind: "readyToApply"; draft: Config; diff: ConfigDiff }
  | { kind: "applying"; draft: Config }
  | { kind: "applied"; runtime: RuntimeStatus; rollbackId: string }
  | { kind: "failed"; error: ApplyError };
```

This is exactly the kind of UI where TypeScript earns its keep.

## React usage guidelines

React should be used to create a clear, understandable Console, not to build a generic frontend platform.

Use React for:

- forms,
- wizard steps,
- route detail drawers,
- config preview,
- validation states,
- diff views,
- apply flows,
- rollback confirmation,
- live status polling,
- reusable components.

Avoid unnecessary complexity:

- no global state library at P0 unless absolutely needed,
- no Redux,
- no custom framework wrapper,
- no over-abstracted component factory,
- no magical form generator for everything.

Use local component state for simple screens.

Use TanStack Query for server data.

Use a small explicit store only if needed for draft config state.

## Tailwind CSS usage guidelines

Tailwind is the right styling choice because Jul needs a custom product feel, not a generic enterprise admin template.

But Tailwind must be used with discipline.

Do not scatter long utility-class strings everywhere.

Create a small owned component system.

Good:

```tsx
<Button intent="danger">Disable TLS</Button>
<StatusPill status="healthy">Healthy</StatusPill>
<Card tone="warning">Certificate expires in 8 days</Card>
```

Bad:

```tsx
<button className="px-3 py-2 rounded bg-red-500 hover:bg-red-600 text-white shadow-sm ...">
```

repeated across the codebase.

Rules:

- define design tokens,
- create reusable components,
- keep raw utility classes mostly inside components,
- use semantic variants such as `intent="danger"` or `status="healthy"`,
- design for accessibility,
- support light/dark later through tokens, not duplicated screens.

## Component system

P0 needs a small, high-quality component set:

```text
Button
Input
Select
Switch
Textarea
CodeEditor
Card
Table
Drawer
Modal
Tabs
Tooltip
Badge
StatusPill
Alert
EmptyState
ConfirmDialog
DiffView
WizardStepper
FieldHelp
ValidationMessage
ImpactSummary
```

The component system must make dangerous actions visually obvious.

Examples:

- disable TLS,
- expose admin publicly,
- remove authentication,
- enable caching on authenticated routes,
- lower timeouts aggressively,
- remove all healthy upstreams.

## Should Jul use shadcn/ui?

Maybe as inspiration, not as a product dependency philosophy.

The better approach is:

- use accessible primitives where needed,
- copy or own components in Jul's codebase,
- build a Jul-native design system,
- keep the component set small,
- avoid importing a heavy design system that makes Jul look generic.

Radix-style primitives are acceptable for accessibility-heavy components such as dialogs, popovers, tabs, select menus, and tooltips.

The final UI should feel like Jul, not like a template.

## Backend API shape required

The frontend stack only works well if the admin API is shaped well.

The UI should not reverse-engineer raw TOML for everything. It should receive structured summaries from the backend.

Recommended endpoints:

```http
GET  /api/runtime/overview
GET  /api/routes
GET  /api/apps
GET  /api/tls
GET  /api/security
GET  /api/traffic-controls

GET  /api/config
POST /api/config/validate
POST /api/config/diff
POST /api/config/apply
GET  /api/config/history
GET  /api/config/history/{id}
POST /api/config/rollback

POST /api/wizard/generate
GET  /api/events
```

## API response principles

Every mutating API should return:

- success/failure,
- human-readable message,
- machine-readable code,
- validation errors when relevant,
- affected routes/apps/listeners,
- rollback snapshot ID when relevant,
- reload result.

Validation error example:

```json
{
  "ok": false,
  "message": "Route `/api/` points to an upstream that does not exist.",
  "errors": [
    {
      "code": "unknown_upstream",
      "path": "servers[0].locations[2].proxy_pass",
      "summary": "Unknown upstream `api`",
      "detail": "Create an upstream named `api` or choose an existing app.",
      "severity": "error"
    }
  ]
}
```

## State model

The frontend should explicitly separate:

- draft config,
- persisted config,
- running config,
- runtime metrics,
- validation result,
- reload result,
- history snapshot,
- wizard-generated config.

Do not mix desired state with runtime state.

Example:

```text
Desired state:
  The config says route `/api/` points to app `backend`.

Runtime state:
  App `backend` currently has 2 healthy backends and 1 failing backend.
```

Both matter. The UI must show both clearly.

## Forms strategy

Do not build a giant TOML form for everything.

Build guided forms for common tasks:

- Add route.
- Add app/upstream.
- Enable TLS.
- Enable ACME.
- Configure compression.
- Configure cache.
- Configure rate limiting.
- Configure auth.
- Configure health checks.

Use raw TOML for advanced or uncommon fields.

The UI should generate TOML from the guided forms and always let users inspect it.

## Raw TOML editor

Use CodeMirror 6 for the raw editor.

Required features:

- TOML syntax highlighting,
- line numbers,
- validation markers,
- read-only generated preview mode,
- editable expert mode,
- keyboard shortcuts,
- formatting later if available,
- visible warning when editing raw TOML can override guided assumptions.

The raw editor should not be the primary beginner experience, but it must remain available.

Experts should never feel trapped.

## Diff view

Diff is non-negotiable.

Before apply, the user should see:

1. human summary,
2. affected routes/apps/listeners,
3. warnings,
4. raw TOML diff.

Example summary:

```text
This change will:
- add a new route `/graphql`,
- send it to app `apollo`,
- enable compression,
- leave the route public,
- hot reload the server.
```

Then show raw diff.

## Wizard architecture

The wizard should be goal-first.

Step 1:

```text
What do you want Jul to do?
```

Options:

- Serve a website folder.
- Put an app behind Jul.
- Add HTTPS to an app.
- Load balance multiple app instances.
- Protect a route.
- Expose a gRPC service.
- Create a development reverse proxy.

For "Put an app behind Jul", ask:

- What is the app type? Express, Apollo, FastAPI, Django, generic HTTP.
- Where is it running?
- What public path or domain should users use?
- Should HTTPS be enabled?
- Should compression be enabled?
- Should this route be public or protected?
- Is there more than one instance?
- Should Jul health-check it?

The final wizard step must show:

- plain-English summary,
- generated TOML,
- validation result,
- apply and reload action,
- rollback availability.

## Security model for the frontend

The frontend must not weaken the admin security model.

Rules:

- same-origin API only,
- no external scripts,
- no external styles,
- no external fonts,
- strict Content Security Policy,
- admin token stored only in session storage or memory,
- never log tokens,
- never include private keys in API responses,
- never display sensitive values by default,
- require confirmation for dangerous changes.

## Dependency discipline

Keep dependencies small.

P0 dependencies should be limited to:

```text
@vitejs/plugin-react
typescript
vite
react
react-dom
tailwindcss
postcss
autoprefixer
@tanstack/react-query
zod
codemirror packages
maybe radix primitives
```

Avoid adding dependencies for small utilities that can be implemented directly.

Do not add chart libraries unless simple SVG/canvas charts are not enough.

Do not add a full date library unless native date formatting is insufficient.

Do not add a global state library until the UI proves it needs one.

## Testing strategy

Frontend tests should cover:

- wizard generation,
- validation error rendering,
- diff rendering,
- apply flow states,
- rollback confirmation,
- dangerous change warnings,
- empty states,
- API failure states,
- route/app/TLS form behavior.

Recommended tools:

- Vitest for unit/component tests,
- React Testing Library,
- Playwright for end-to-end UI flows.

P0 end-to-end flows:

1. Add reverse proxy route.
2. Add Express/Apollo app.
3. Enable TLS.
4. Enable compression.
5. Validate invalid config and show useful error.
6. Apply and hot reload.
7. Roll back.

## Accessibility

The Console is an operations UI. It must be accessible.

Requirements:

- keyboard navigable,
- visible focus states,
- semantic buttons and form labels,
- dialogs trap focus,
- status is not color-only,
- errors are associated with fields,
- charts have textual summaries,
- contrast passes basic accessibility standards.

Accessibility is not polish. It is reliability for humans.

## Performance budget

The Console should feel instant on localhost and over tunnels.

Suggested P0 budget:

- initial JS bundle target: under 250 KB gzip if possible,
- no giant chart libraries initially,
- no Monaco initially,
- no icon mega-pack if avoidable,
- polling intervals controlled and visible,
- avoid re-rendering large tables unnecessarily.

If the UI becomes heavy, it is betraying Jul's lean identity.

## Release and development workflow

Development can require Node.

Runtime must not.

Recommended commands:

```bash
make console-dev
make console-build
make console-check
make build-console
```

Where:

```text
make console-dev
  starts Vite dev server and proxies API to local Jul admin listener

make console-build
  builds static assets into internal/admin/assets/dist

make console-check
  runs typecheck, lint, and tests

make build-console
  builds frontend assets and then builds Jul with -tags console
```

CI should verify:

- TypeScript typecheck,
- lint,
- frontend tests,
- production build,
- Go build with embedded assets,
- generated assets are current if checked in.

## Migration plan

### Phase 1 - Parallel UI

Keep current Console working.

Add new React app behind the same `console` build tag, possibly served under `/v2` or as replacement after parity.

### Phase 2 - API hardening

Formalize admin API contracts:

- overview,
- routes,
- apps,
- TLS,
- config validate,
- config diff,
- apply,
- history,
- rollback,
- events.

### Phase 3 - Guided core flows

Implement:

- overview,
- routes,
- apps/upstreams,
- TLS,
- config diff,
- history,
- setup wizard.

### Phase 4 - Replace old UI

Replace old embedded single-file UI once P0 flows are covered.

### Phase 5 - Polish and expand

Add traffic controls, observability details, route-level metrics, richer auth editors, event timeline, support bundle.

## Risks

### Risk: frontend stack bloats Jul

Mitigation:

- no Node runtime,
- embedded static assets,
- strict dependency budget,
- bundle-size checks,
- no heavy UI kit.

### Risk: UI hides too much

Mitigation:

- always show generated TOML,
- keep raw TOML expert mode,
- show diff before apply.

### Risk: frontend validation conflicts with backend validation

Mitigation:

- backend is source of truth,
- frontend validation is only convenience,
- all apply paths call backend validation.

### Risk: unsafe changes become too easy

Mitigation:

- danger confirmations,
- blast-radius preview,
- rollback,
- warnings,
- safe defaults.

### Risk: too much P0 scope

Mitigation:

- focus P0 on routes, apps, TLS, config diff, wizard, rollback.
- defer fleet, terminal, AI, RBAC, i18n.

## Final recommendation

Yes: TypeScript, React, Vite, and Tailwind CSS are the right stack for Console v2.

The decision should be written as:

> Console v2 will be implemented as a TypeScript React single-page application built with Vite and styled with Tailwind CSS. The built assets will be embedded into the Jul binary and served by the existing admin listener. Jul will not require Node.js, npm, pnpm, external assets, or a separate frontend server at runtime. Go remains the source of truth for config validation, hot reload, and rollback.

This gives Jul the best of both worlds:

- a serious modern UI implementation,
- maintainable frontend code,
- safer complex flows,
- better guided configuration,
- no runtime bloat,
- no compromise on the single-binary identity.

## Source links

- Jul repository: <https://github.com/victornife/jul>
- Jul Console docs: <https://github.com/victornife/jul/blob/main/docs/console.md>
- Jul admin backend: <https://github.com/victornife/jul/tree/main/internal/admin>
- Jul embedded UI assets: <https://github.com/victornife/jul/tree/main/internal/admin/assets>
- React TypeScript docs: <https://react.dev/learn/typescript>
- Vite guide: <https://vite.dev/guide/>
- Tailwind with Vite: <https://tailwindcss.com/docs/installation/using-vite>
- TanStack Query: <https://tanstack.com/query/latest/docs/framework/react/overview>
- Zod: <https://zod.dev/>
- CodeMirror docs: <https://codemirror.net/docs/>
- Nginx UI repository: <https://github.com/0xJacky/nginx-ui>

<!-- Engineering execution spec for Console v2 (roadmap Y2-09). Source-of-truth for the
     React/Vite/Tailwind substrate migration. Companion to ../roadmap/ and ../specs/year-2.md.
     Update when the design changes; bump Version + changelog. -->

# JUL Engineering Execution Plan — Console v2 (Y2-09)

> Version 1.0 · Updated 2026-06-23
>
> Maturity note: Console (Y1-07) is **Beta**. This spec drives the substrate
> migration that closes GA criteria ① (endpoint/panel matrix) and ⑦ (CSP/CSRF/auth)
> and moves Console to **GA — soak pending**. Governed by
> [ADR 0006](../adr/0006-console-v2-stack.md) (amends
> [ADR 0004](../adr/0004-console-ui-invariants.md) #4/#5). Source proposals:
the [operations & configuration UI spike](../reviews/previous_reviews/jul_console_v2_spike.md) and
the [frontend stack recommendation](../reviews/previous_reviews/jul_console_tech_stack_recommendation.md).

## Goal & non-negotiables

- Jul ships as **one self-contained Go binary** with embedded static assets and
  **no Node.js runtime requirement**. React/Vite/Tailwind are a **build-time UI
  implementation detail only**.
- **No external web assets at runtime:** every JS/CSS/font is embedded via
  `go:embed` and served same-origin. No CDN, no external fonts, no network fetch
  for the app shell.
- The lean build (`Core/OSS`, no `console` tag) is **unchanged** — it keeps serving
  the minimal admin page (`configUIPage`) with zero frontend weight.
- Goal: replace the single hand-written `internal/admin/assets/console.html` with a
  typed, testable SPA that scales to the Y2-09 cockpit (log tail, plugin manager,
  route designer, dashboards) without eroding the single-binary identity.

## Stack

| Concern | Choice | Notes |
| --- | --- | --- |
| Language | TypeScript (strict) | `strict` + `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride` |
| UI | React (CSR SPA) | `preact/compat` is the size-budget escape hatch |
| Bundler | Vite | output → `../assets/dist`, hashed assets, `manualChunks` lazy CodeMirror |
| Styling | Tailwind | utility-first + design tokens; `darkMode: 'class'` |
| Server state | TanStack Query | the only data-fetching layer; no Redux/global store |
| Boundary validation | Zod | validates **UI shape** only; Go remains source of truth |
| Config editor | CodeMirror 6 | lazily loaded; not in the initial-route budget |
| Charts | custom SVG | token-styled; no charting library |

Initial-route budget: **~250 KB gzip**, enforced in CI. Exceeding it triggers the
ADR 0006 review (preact/compat, more code-splitting, or trim scope).

## Repository layout

Frontend **source** (Console maintainers only; needs Node/pnpm):

```text
internal/admin/ui/
  package.json              # pnpm; scripts: dev, build, lint, typecheck, test, format
  pnpm-lock.yaml            # committed lockfile (reproducible installs)
  vite.config.ts            # base path, output → ../assets/dist, manualChunks (codemirror lazy)
  tsconfig.json             # strict; paths alias @/* → src/*
  tsconfig.node.json        # for vite config typechecking
  tailwind.config.ts        # design tokens, darkMode:'class', content globs
  postcss.config.cjs
  eslint.config.ts          # typescript-eslint strict + react-hooks + jsx-a11y
  .prettierrc
  index.html                # Vite entry (single root div, no inline scripts)
  vitest.config.ts
  src/
    main.tsx                # React root, providers (QueryClient, Router, Theme)
    app/                    # shell: layout, nav, routing table, error boundary, theme
    api/                    # typed fetch client, Zod schemas, TanStack Query hooks, auth-token store
    components/             # reusable primitives (Button, Card, Table, Badge, Toast, SVG charts)
    features/               # one folder per panel, each self-contained (panel = feature surface)
      overview/             # status rows (FeatureStatus) + live stats
      routes/               # server/location routing view + editor
      apps/                 # upstreams/pools/backends + health
      tls/                  # certs, issuance, inspection
      security/             # auth, rate-limit, headers, body limits
      traffic-controls/     # timeouts, retries, circuit, compression toggles
      config/               # raw TOML (CodeMirror) + validate + diff + apply
      history/              # snapshot list + compare + rollback
      wizard/               # guided serve/proxy config generation
      observability/        # metrics, event log (SSE), log tail
    styles/                 # tailwind entry css, design-token css vars, global base
    lib/                    # helpers (formatting, bytes, duration, blast-radius calc client-side)
    test/                   # test setup, fixtures
```

Built bundle (**committed + embedded**; Node-free `go build`):

```text
internal/admin/assets/dist/
  index.html                # hashed asset references
  assets/*.js               # hashed chunks (main + lazy codemirror/diff)
  assets/*.css              # single hashed Tailwind output
```

New Go backend files (Phase 2):

- `internal/admin/projections.go` — overview/routes/apps/tls/security/traffic JSON
  projections from `config.Config` + runtime (UI never re-parses TOML).
- `internal/admin/diff.go` — structured config differ (running vs candidate).
- `internal/admin/events.go` — SSE hub (`/api/events`); reload/apply/health now,
  log-tail later.
- `internal/admin/humanerrors.go` — maps `internal/config/validate.go` machine
  errors → human-readable messages.
- `internal/admin/console.go` change: `//go:embed assets/dist` → `embed.FS`; file
  server + SPA index fallback.

## API surface

All `/api/*` require a Bearer token (constant-time compare, header — not cookie).
The SPA is served at `/`.

**Existing v1** (kept until cutover, then retired / thin-aliased one release):
`/healthz`, `/readyz`, `/metrics`, `/`, `/config`, `/ui`, `POST /cache/purge`,
`POST /reload`, `GET /api/stats`, `GET /api/status`, `GET /api/config`,
`POST|PUT /api/config/raw`, `GET|POST /api/config/settings`, `GET /api/upstreams`,
`GET /api/certs`, `POST /api/wizard`, `GET /api/history`, `GET /api/history/get`,
`POST /api/history/rollback`.

**New v2** (additive; no `/api/v2/` prefix — Console is Beta, contract not frozen):

| Endpoint | Method | Mutating | Purpose |
| --- | --- | --- | --- |
| `/api/runtime/overview` | GET | no | Dashboard summary (status rows + live stats rollup) |
| `/api/routes` | GET | no | Structured server/location routing projection |
| `/api/apps` | GET | no | Upstreams/pools/backends + health |
| `/api/tls` | GET | no | Certs / issuers / expiry / inspection |
| `/api/security` | GET | no | Auth, rate-limit, headers, body-limit state |
| `/api/traffic-controls` | GET | no | Timeouts, retries, circuit, compression |
| `/api/config` | GET | no | Raw TOML + metadata (reshape of v1) |
| `/api/config/validate` | POST | no | Structured + human-readable validation errors |
| `/api/config/diff` | POST | no | Structured diff: candidate vs running |
| `/api/config/apply` | POST | **yes** | Validate → snapshot → reload; returns post-apply runtime delta |
| `/api/config/history` | GET | no | Snapshot list |
| `/api/config/history/{id}` | GET | no | One snapshot (path-traversal-safe id) |
| `/api/config/rollback` | POST | **yes** | Re-apply a snapshot via the validated path |
| `/api/wizard/generate` | POST | no | Non-mutating TOML generation (supersedes `/api/wizard`) |
| `/api/events` | GET | no | SSE stream (reload/apply/health; log tail later) |

The `FeatureStatus{Group,Name,Active,Detail}` 21-row backbone (asserted by
`TestStatusAPI`) is preserved and surfaces through `/api/runtime/overview`.

> **Shipped surface.** Additional v2 endpoints landed beyond this table
> (`/api/routes/test`, `/api/observability/*`, `/api/admin/{health,client-errors}`,
> `/api/audit`, `/api/audit/export`, `/api/search`, `/api/config/patch`,
> `/api/config/patch/apply`). The authoritative current list of endpoints and the
> panel that consumes each is the
> [API endpoint to panel map](../console.md#api-endpoint-to-panel-map) in
> `console.md`.

## TypeScript conventions

- `strict: true` (all strict flags) + `noUncheckedIndexedAccess`,
  `exactOptionalPropertyTypes`, `noImplicitOverride`.
- **No `any`** (eslint `no-explicit-any` = error); use `unknown` + narrowing. No
  non-null `!` except provably-safe and commented.
- **Zod at every boundary:** every API response is parsed by a Zod schema before
  use; inferred types (`z.infer`) are the UI types. Go remains the source of truth;
  Zod validates UI shape only, never re-implements business rules.
- **Server state via TanStack Query** only (no Redux/global store); local UI state
  via React hooks. Query keys are centralised in `api/`.
- A typed fetch client wraps `fetch`, injects the Bearer token, and normalises
  errors to a typed `ApiError`.
- Function components + hooks; props typed explicitly; **named exports** for
  components (no default exports).
- **Feature isolation:** a panel imports only from its own `features/<x>/`, shared
  `components/`, `api/`, and `lib/`. No cross-feature imports.
- Discriminated unions for view states (`loading | error | empty | ready`);
  exhaustive `switch` with a `never` guard.
- ESLint (typescript-eslint strict + react-hooks + jsx-a11y) and Prettier.
  `pnpm typecheck` (`tsc --noEmit`) + `pnpm lint` gate in CI.
- Tests: Vitest + Testing Library; test behaviour, not implementation;
  generated-TOML / validation / apply logic is unit-tested.

## Styling / Tailwind conventions

- **Utility-first:** compose styles with Tailwind classes in markup; avoid
  hand-written CSS except the token layer and rare base resets.
- **Design tokens:** colours/spacing/typography/radii are CSS variables mapped in
  `tailwind.config.ts`. No raw hex in components — use semantic tokens
  (`bg-surface`, `text-muted`, `border-subtle`).
- **No inline `style=` attributes** (CSP-friendly). CodeMirror/React-injected inline
  styles are handled via the style nonce.
- **Dark mode** via `darkMode: 'class'` + a theme toggle; tokens resolve per theme.
  Light and dark are both first-class.
- **No external fonts / no CDN:** a system font stack or a self-hosted (embedded)
  font subset only — everything ships in the bundle.
- Mobile-first responsive utilities; accessible focus states; respect
  `prefers-reduced-motion`.
- A single compiled, purged CSS output (Tailwind JIT) — counts against the 250 KB
  budget.
- Custom SVG/canvas charts styled with tokens (no charting library).

## Build & embed pipeline

`pnpm --dir internal/admin/ui build` → emits hashed JS/CSS into
`internal/admin/assets/dist/` (single CSS, lazy CodeMirror chunk) → the bundle is
**committed** → `console.go` embeds it via `//go:embed assets/dist` → `go build
-tags console` produces the binary with **no Node involvement**. The lean build is
untouched.

CI adds a `frontend` job (ubuntu, Node + pnpm): lint + typecheck + unit + build,
then a **drift guard** (fail if the committed `dist/` differs from a fresh build)
and a **size gate** (~250 KB gz initial route).

## Security

- Token in the `Authorization` header (constant-time compare; not a cookie) →
  **CSRF N/A**.
- CSP tightened to `script-src 'self'` (drop `unsafe-inline`); `style-src 'self'`
  with a per-response **nonce** for editor/React inline styles. Documented
  fallback: `style-src 'self' 'unsafe-inline'`.
- Closes GA criterion ⑦ (formalised CSP/CSRF/auth) and, via the endpoint/panel
  matrix above, criterion ① .

## Phased delivery

| Phase | Scope | Key verification |
| --- | --- | --- |
| **0 — Governance** | Commit spike → `docs/reviews/`; ADR 0006; this spec; reviews/README + roadmap + year-2 cross-links | Docs render; links valid; ADR front-matter correct |
| **1 — Toolchain & embed** | Scaffold `internal/admin/ui/`; Vite → `assets/dist/`; commit hello-shell bundle; rewrite `console.go` embed; serve v2 at a dev route; CI `frontend` job + drift + size gates | `go build -tags console` with **no Node**; lean build unaffected; gates green |
| **2 — Backend API v2** | Projections, `validate`/`diff`/`apply` split, history/rollback reuse, `wizard/generate`, `/api/events` SSE, human-error layer; CSP/auth formalised; v1 kept | Go tests per endpoint; `TestStatusAPI` still asserts every row; round-trips; race(full) green |
| **3 — Read panels** | Shell (router, Query, Zod, tokens, SVG charts); Overview, Routes, Apps, TLS, Security, Traffic Controls, Observability; Config read + diff + history + rollback | Vitest component tests; manual parity vs v1 at the dev route |
| **4 — Editors & write flows** | CodeMirror (lazy) raw TOML + guided forms; validate-on-edit; diff preview; apply + confirm + post-apply delta; wizard expansion; TLS/security editors | Vitest editor + generated-TOML; optional Playwright apply→reload→confirm |
| **5 — Cutover & GA** | Flip default to v2; remove `console.html` + dev route; retire v1; land deferred doc edits; a11y/responsive/size pass | full-tags build/test/race + size + drift green; single-binary/no-Node check; maturity ①–⑨ |
> **Status (2026-06-23, v1.9):** all six phases are complete. The Phase 5 cutover
> shipped — the v2 SPA is the default admin UI and the hand-written v1 is retired
> ([ga-push.md](../ga-push.md)) — and the console is **GA — soak pending**
> ([status.md](../status.md)). Per-feature panels now evolve continuously
> ([ADR 0004](../adr/0004-console-ui-invariants.md)); see the
> [capability matrix](../console.md#capability-matrix).
## Excluded for now (with reasons)

Each remains a candidate later, but must first clear the size budget and the
single-binary / no-Node-runtime invariant.

- **Next.js / SSR / RSC** — would reintroduce a Node runtime at serve time, breaking
  the single-binary identity. CSR is sufficient for an authenticated admin tool.
- **Component libraries (MUI / Ant / Chakra)** — bust the 250 KB budget and impose
  an external design system; we use Tailwind + small in-repo primitives.
- **Redux / MobX / Zustand global store** — server state is owned by TanStack Query;
  extra global state adds weight for no benefit at this scale.
- **Monaco editor** — too heavy; CodeMirror 6 (lazy) covers TOML editing in budget.
- **Charting libs (ECharts / Chart.js / Recharts / ApexCharts)** — custom token-styled
  SVG covers the few sparklines/gauges we need.
- **Web terminal / shell** — large surface + attack-surface risk; out of cockpit scope.
- **AI assistant panel** — a separate initiative, not part of the substrate migration.
- **i18n / localization** — English-only admin tool for now; premature overhead.
- **Micro-frontends / module federation** — over-engineering for one embedded SPA.
- **External fonts / CDN / runtime network fetches** — violate "no external web assets".
- **Cluster / multi-node, RBAC / multi-user** — separate roadmap items, not UI-substrate work.
- **PWA / offline / service worker** — unnecessary for a same-origin admin console.

## Verification & GA exit

- full-tags build/test/race green; size + drift gates green.
- Single-binary / no-Node-runtime check (no Node footprint in `go build` / Docker /
  release).
- Link-check passes; manual smoke of every panel.
- Maturity ①–⑨ satisfied; [docs/status.md](../status.md) Console row →
  **GA — soak pending**.

## Changelog

| Version | Date | Change |
| --- | --- | --- |
| 1.0 | 2026-06-23 | Initial spec from the Console v2 [operations & configuration UI spike](../reviews/previous_reviews/jul_console_v2_spike.md) and [frontend stack recommendation](../reviews/previous_reviews/jul_console_tech_stack_recommendation.md) + [ADR 0006](../adr/0006-console-v2-stack.md). |

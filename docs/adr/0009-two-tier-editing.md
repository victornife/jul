# ADR 0009 — Two-tier editing for complex route types (Quick vs. Designer)

- **Status:** Accepted
- **Date:** 2026-06-30
- **Deciders:** Jul.IA maintainers
- **Applies to:** every Console v2 route panel that carries enough surface area to justify a dedicated designer; first implemented for **grpc_transcode**
- **Source:** Console v2 route-design PR; reviewed against ADR 0004 invariant #3 (*power through clarity, not more knobs*) and #5 (*continuous, not big-bang*)

## Context

As the Console v2 route surface grows, a tension appears between **speed** and **expressiveness**:

- A user who only wants to tweak one field (e.g., change a gRPC descriptor path or flip TLS) should not be forced through a full-page wizard.
- A user who is *creating* a route, or who needs to touch many inter-dependent fields (annotations, streaming mode, max-message-size, TLS certs), needs screen real estate, inline help, and validation feedback that a compact drawer cannot provide.

The grpc_transcode route type is the first to hit this tension hard: it combines a protoc `FileDescriptorSet`, reflection toggles, TLS, preserve-proto-names, streaming mode, and size limits. A single monolithic form inside `RouteDetail` would either be too cramped (hurting clarity) or too long (hurting speed).

[ADR 0004](0004-console-ui-invariants.md) invariant #3 demands *progressive disclosure and curated forms*; invariant #5 requires that the Console grow *per-panel*, not as big-bang rewrites. A consistent editing paradigm that every heavy route type can reuse satisfies both.

## Decision

Adopt a **two-tier editing model** for complex route types in Console v2:

### Tier 1 — Quick Edit
- Lives **inside the existing `RouteDetail` drawer** (or equivalent inline surface).
- Exposes only the **most frequently mutated fields** (≤ 4–6 inputs).
- Uses the standard patch API (`PATCH /api/config`) with `op: "location_set_*"`.
- On save, the config diff is handed off to the existing `/config` review flow (`setPendingDraft`).
- A prominent "Open in Designer" deeplink takes the user to Tier 2, pre-filled with current values.

### Tier 2 — Designer (Deep Edit / Create)
- Lives on a **dedicated route/page** (e.g., `/transcode-designer`).
- Shows the **full field surface**, grouped logically, with inline help and validation.
- For **create**, starts empty; for **edit**, seeds from URL query parameters passed by Tier 1.
- Same save semantics: builds a patch, hands off to `/config` diff review.
- The designer page is **reusable across create and edit**; the only difference is initial seeding.

### Concrete first implementation (grpc_transcode)
| Concern | Tier 1 (Quick) | Tier 2 (Designer) |
|---|---|---|
| Fields shown | `target`, `UseReflection`, `DescriptorPath`, `PreserveProtoNames`, `Streaming`, `StreamMode`, `MaxMessageSize`, `TLS` | All of the above + grouped sections + file-upload + annotations help |
| Surface | `RouteDetail` drawer card | Full-page `TranscodeDesignerPanel` |
| Navigation | Drawer stays open; link to designer | Back-link to referring route list |
| Patch payload | `op: "location_set_transcode"` with `TranscodePatch` | Same payload shape; designer is a bigger form builder |
| Pre-fill | Reads `loc.transcode` from projection | Reads query params (`?edit=1&target=...`) seeded by Tier 1 |

### Rules for future route types
Any new route type that has **more than four primary editable fields** or **field interdependency** (e.g., mutual exclusion between reflection and descriptor path) should evaluate two-tier editing. If adopted, it must follow this contract:

1. **Backend:** a dedicated `*_set_*` patch operation in `patchRequest`, a typed `*Patch` struct, and a projection field.
2. **Frontend:** a `*QuickEdit` component inside `RouteDetail` + a `*DesignerPanel` page component. Both emit the **same patch payload shape**.
3. **URL contract:** Tier 1 passes current values to Tier 2 via query string; Tier 2 reads them on mount. No session state, no transient store.
4. **Save contract:** both tiers hand off to `/config` via `setPendingDraft` — never auto-apply, never bypass diff review.

## Consequences

- **Positive:**
  - Users who only tweak one field stay fast (drawer → change → save → diff).
  - Users who need depth get a focused, non-cramped surface.
  - The pattern is **reusable**; the next heavy route type (e.g., WAF rules, AI gateway prompts) copies the contract instead of inventing a new UX.
  - Backend patch operations stay small and testable; there is no "designer-only" save path.

- **Negative / trade-off:**
  - Two components to maintain per route type instead of one.
  - Field eligibility for Tier 1 is a product judgement call; getting it wrong (too much in quick, or too little) degrades the experience.
  - URL query strings for pre-fill have a length ceiling; very large values (e.g., inline descriptors) must be handled carefully (the first implementation avoids inline descriptors in the query by relying on the config file path).

## Related

- First implementation: grpc_transcode quick-edit (`TranscodeQuickEdit.tsx`) and designer (`TranscodeDesignerPanel.tsx`) shipped in commit `8548e10`.
- Informed by: [ADR 0004](0004-console-ui-invariants.md) invariants #3 and #5.
- Precedent: the existing `/config` diff-review flow (`setPendingDraft`) is reused unchanged; no new save semantics are introduced.

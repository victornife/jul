<!-- Concept-horizon and experiment specification. Not an active implementation plan. -->


> **Concept horizon — not committed.**
# JUL Technical Experiment Horizon — Year 4: AI Gateway and advanced edge capabilities

> Version 1.1 · Updated 2026-08-03 · **Concept horizon / bounded experiment only.**
>
> AI is not the automatic next phase. Entry is governed by
> [ADR 0013](../adr/0013-project-operating-model-and-completeness.md),
> the [roadmap](../roadmap/), and the experiment epic. Generic gateway trust,
> resilience, lifecycle, and observability remain prerequisites defined by
> [Core Gateway Completeness](core-gateway-completeness.md).

## Purpose

Year 4 preserves the hypothesis that Jul.IA may become a compelling
self-contained AI edge gateway **because** it already combines ordinary HTTP,
gRPC, L4, mTLS, WAF, egress policy, safe configuration lifecycle, observability,
and an embedded operational surface.

The experiment is not justified by adding an `ai` tag or matching the feature
list of existing AI gateways. It must prove that provider routing can reuse the
normal Jul.IA architecture without creating a second transport, retry, security,
configuration, or observability system inside the product.

## Technical hypothesis

> Jul.IA can expose a small OpenAI-compatible front door, route streaming model
> traffic across a bounded provider set, and apply existing trust, policy,
> resilience, lifecycle, and observability primitives while remaining coherent,
> removable, and within a defined dependency/binary-size budget.

## Entry prerequisites

The implementation issue remains `[DRAFT]` until all of the following are
accepted and sufficiently implemented or explicitly stubbed for the experiment:

- backend TLS/mTLS, SNI, and peer-identity model;
- generic request/connection limits, retry budget, backoff, and circuit behavior;
- streaming request/response generation ownership;
- secret references and redaction for provider credentials;
- bounded metric-cardinality policy for provider and model dimensions;
- configuration authority and lifecycle classification;
- build-tag, dependency, and binary-size budgets;
- explicit experiment cleanup path.

The experiment may prototype against narrow versions of these seams, but it may
not define incompatible AI-only alternatives.

## First-tranche scope

### Included

- One OpenAI-compatible inbound API for chat/completion-style requests.
- Two or three provider adapters, selected to exercise meaningful protocol
  differences without creating a provider catalogue.
- Streaming request/response handling.
- Model mapping and provider routing.
- Bounded fallback using the generic retry/circuit primitives.
- Token and estimated-cost observations with bounded labels.
- Existing authentication, egress, backend TLS, request IDs, tracing, logs,
  metrics, audit, and Console status integration.
- Explicit build-tag unavailable behavior.
- Recorded provider fixtures and deterministic mock-provider tests.

### Explicitly excluded

- A separate Jul-native public neutral API in the first tranche.
- Semantic cache.
- Broad prompt-injection or PII platform.
- Complex tenant billing or quota hierarchy.
- Autonomous configuration changes.
- AI-assisted incident diagnosis in the Console.
- WASM guardrail marketplace.
- Six-plus provider catalogue.
- Training, fine-tuning, image, audio, or batch APIs.
- Distributed provider state or fleet coordination.

These exclusions are not a claim that the features are undesirable. They keep
the experiment small enough to answer its architectural question.

## Proposed architecture direction

### Front door

The first experiment accepts a deliberately bounded OpenAI-compatible request
shape. Translation occurs into an internal request model sufficient for the
selected providers; that internal model is not automatically a stable public API.

A public provider-neutral schema is considered only after the experiment proves
that the internal model is stable and materially useful beyond the OpenAI
compatibility surface.

### Provider adapters

Adapters should use small HTTP clients and recorded fixtures rather than heavy
vendor SDKs where practical. Each adapter owns only:

- request translation;
- authentication/header injection;
- response and streaming-event normalization;
- provider-specific error classification;
- token/usage extraction.

Connection ownership, TLS, timeouts, retries, circuit state, egress, and
observability come from generic Jul.IA infrastructure.

### Routing and fallback

Provider selection may reuse upstream balancing concepts, but provider semantics
must not be forced into an incompatible existing abstraction merely to maximize
code reuse. The governing resilience contract is generic:

- bounded attempts and overall deadline;
- replayability rules;
- backoff and jitter;
- circuit state;
- explicit provider/model fallback chain;
- cancellation propagation;
- no retries after response streaming becomes externally visible.

### Streaming

The experiment must prove:

- bounded buffering;
- immediate cancellation;
- no handler/resource use after generation retirement;
- stable SSE framing and flushing;
- correct partial-response failure semantics;
- no duplicate retries once output starts;
- trace and request identity continuity.

### Credentials and privacy

- Provider credentials use the normal secret-reference model.
- Raw prompts, responses, keys, authorization headers, and provider error bodies
  are not logged by default.
- Metrics never label by user input, prompt, raw model string, tenant, URL, or
  credential.
- Optional sampling or diagnostic capture requires an explicit future decision.

## Configuration sketch

The exact public schema is decided by the experiment issue after prerequisites.
A likely direction is one location action containing:

- enabled provider definitions;
- secret references;
- base endpoints;
- supported model aliases;
- bounded routing/fallback policy;
- timeouts and streaming switch;
- observability-safe display names.

Configuration must fail before persistence when the `ai` tag is absent or a
provider type is unsupported.

## Console surface

Under [ADR 0014](../adr/0014-operability-surfaces.md), the runtime experiment
requires a clear Console status/configuration surface in the Full+AI profile:

- compiled and enabled state;
- providers and models by bounded configured name;
- health/circuit/fallback status;
- aggregate latency, errors, token, and cost observations;
- explicit Experimental maturity and limitations;
- no secret values or prompt/response content.

Provider contract testing and fixture tooling remain CLI/developer-native.

## Evidence to collect

- Provider-adapter fixture compatibility and drift rate.
- Streaming correctness under cancellation and mid-stream failure.
- Failover behavior before and after response commitment.
- Additional binary size and dependency count.
- Idle memory/goroutine cost when the tag is compiled but disabled.
- Hot-path latency and allocation overhead.
- Whether generic backend trust and resilience were genuinely reused.
- Complexity introduced into configuration, lifecycle, Console, and testing.
- Whether the feature remains understandable and removable.

External users are welcome evidence but are not required merely to run the
technical experiment.

## Success criteria

The first tranche succeeds only if:

- the selected providers pass deterministic request and streaming fixtures;
- fallback is bounded and uses generic resilience semantics;
- provider credentials and request content remain secret-safe;
- the build remains within the agreed dependency and size budget;
- disabled runtime cost is negligible and measured;
- configuration, lifecycle, observability, and Console behavior are coherent;
- no parallel gateway architecture is created;
- a maintainer can reasonably support or remove the experiment.

## Stop criteria

Freeze, extract, or remove the experiment when:

- provider differences require a large unstable universal schema;
- vendor SDK/dependency growth exceeds the budget;
- streaming requires incompatible resource ownership;
- generic trust/resilience cannot support provider behavior;
- prompt/privacy requirements require a separate product architecture;
- the feature materially complicates the non-AI core when disabled;
- maintenance cost exceeds its learning or product value.

## Required exit decision

After the fixed tranche, record one outcome:

- **Promote** into a supported experimental capability with a normal maturity
  path.
- **Continue experimentally** for another explicitly bounded question.
- **Freeze** with limited maintenance and no scope expansion.
- **Extract** into a plugin, module, or separate repository.
- **Remove** the tag, dependencies, schema, docs, and tests.
- **Defer** with the experiment branch/issue preserved but no active work.

## Later hypotheses, not first-tranche commitments

Only after a successful promotion decision should separate issues evaluate:

- provider-neutral public API;
- semantic cache;
- token budgets and multi-tenant accounting;
- guardrails and content policy;
- response-phase WASM integration;
- AI-assisted operational workflows;
- local inference or edge-model execution;
- distributed provider routing.

Each remains independently gated and must reuse the supported architecture.

## Other Year-4 horizon concepts

Advanced WASM response processing, templates, image optimization, WebTransport,
Early Hints, or post-quantum TLS remain separate technical hypotheses. Their
absence does not make the standalone gateway incomplete and they do not inherit
approval from the AI experiment.

## Changelog

| Date | Version | Change |
| --- | --- | --- |
| 2026-08-03 | 1.1 | Replaced the automatic full AI programme with a bounded experiment: fixed front door/provider scope, generic trust/resilience prerequisites, explicit exclusions, evidence budget, and promote/freeze/extract/remove exit decision. |
| 2026-06-21 | 1.0 | Initial Year-4 concept horizon. |

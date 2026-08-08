/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { Suspense, lazy, useCallback, useEffect, useState, useMemo, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  applyConfig,
  applyPatchBatch,
  patchConfigBatch,
  diffConfig,
  discardPendingRestart,
  fetchOverview,
  fetchPendingRestart,
  fetchPatchCandidate,
  fetchRawConfig,
  validateConfig,
  ApiError,
  ConfigRejectedError,
  ConfigConflictError,
  ConfigRestartRequiredError,
  ConfigAdminChangeError,
  ConfigApplyOutcomeError,
  type ConfigDiff,
  type PendingRestartStatus,
} from "@/api/client.ts";
import { deriveApplyOutcome, type ApplyOutcome } from "@/lib/applyOutcome.ts";
import { useManagedApplyRecord } from "@/lib/useManagedApplyRecord.ts";
import { deriveFinalizationAdvisory } from "@/lib/finalizationAdvisory.ts";
import { useConfigMutationMachine } from "@/features/config/useConfigMutationMachine.ts";
import { useDebouncedValue } from "@/lib/useDebouncedValue.ts";
import { takePendingDraft, type PendingPatchDraft } from "@/lib/configDraftHandoff.ts";
import { patchResultToPendingDraft } from "@/lib/useRunPatchBatch.ts";
import { decidePatchApplyAction } from "@/lib/patchPreviewAction.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading, Spinner } from "@/components/ui.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { ApplyOutcomeBanner } from "@/features/config/ApplyOutcomeBanner.tsx";
import { DiffView } from "@/features/config/DiffView.tsx";

const CodeEditor = lazy(() =>
  import("@/features/config/CodeEditor.tsx").then((m) => ({ default: m.CodeEditor })),
);

function EditorFallback() {
  return (
    <div className="flex h-full items-center justify-center text-xs text-jul-muted">
      Loading editor…
    </div>
  );
}

function ValidationPill({ state }: { readonly state: "idle" | "checking" | "valid" | "invalid" }) {
  const map = {
    idle: { label: "Not checked", cls: "bg-jul-border/40 text-jul-muted" },
    checking: { label: "Checking…", cls: "bg-jul-accent/15 text-jul-accent" },
    valid: { label: "Valid", cls: "bg-jul-success/15 text-jul-success" },
    invalid: { label: "Invalid", cls: "bg-jul-danger/15 text-jul-danger" },
  } as const;
  const { label, cls } = map[state];
  return <span className={`rounded-full px-2 py-0.5 text-xs font-medium ${cls}`}>{label}</span>;
}

/** Persistent banner shown at the top of the editor when a planned restart is pending. */
function PendingRestartBanner({
  status,
  onDiscard,
  discarding,
  discardError,
}: {
  readonly status: PendingRestartStatus;
  readonly onDiscard: () => void;
  readonly discarding: boolean;
  readonly discardError: Error | null;
}) {
  if (status.inconsistent) {
    return (
      <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-sm">
        <p className="font-semibold text-jul-danger">
          Inconsistent staged-restart state — manual recovery required
        </p>
        <p className="mt-1 text-xs text-jul-muted">
          The staged configuration and backup files are in an inconsistent state. Hot applies are
          blocked. See the server logs for details.
        </p>
      </div>
    );
  }
  if (status.external || !status.managed) {
    const subs = status.subsystems ?? [];
    return (
      <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-sm">
        <p className="font-semibold text-jul-warning">
          Configuration on disk differs from runtime — restart required
        </p>
        {subs.length > 0 && (
          <p className="mt-1 text-xs text-jul-muted">
            Affected: <span className="font-mono">{subs.join(", ")}</span>
          </p>
        )}
      </div>
    );
  }
  const subs = status.subsystems ?? [];
  return (
    <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-sm">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="font-semibold text-jul-warning">Restart required — configuration staged</p>
          {subs.length > 0 && (
            <p className="mt-1 text-xs text-jul-muted">
              Pending: <span className="font-mono">{subs.join(", ")}</span>
            </p>
          )}
          {status.staged_version && (
            <p className="mt-0.5 text-xs text-jul-muted">
              Staged: <span className="font-mono">{status.staged_version}</span>
              {status.serving_version && (
                <>
                  {" "}
                  · Serving: <span className="font-mono">{status.serving_version}</span>
                </>
              )}
            </p>
          )}
          <p className="mt-1 text-xs text-jul-muted">
            Hot applies are blocked until this is discarded or the process is restarted.
          </p>
        </div>
        {status.discard_available && (
          <button
            type="button"
            onClick={onDiscard}
            disabled={discarding}
            className="inline-flex flex-shrink-0 items-center gap-1.5 rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
          >
            {discarding && (
              <svg
                className="h-3 w-3 animate-spin"
                fill="none"
                viewBox="0 0 24 24"
                aria-hidden="true"
              >
                <circle
                  className="opacity-25"
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  strokeWidth="4"
                />
                <path
                  className="opacity-75"
                  fill="currentColor"
                  d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
                />
              </svg>
            )}
            {discarding ? "Discarding…" : "Discard staged configuration"}
          </button>
        )}
      </div>
      {discardError && (
        <p className="mt-2 text-xs text-jul-danger">Discard failed: {discardError.message}</p>
      )}
    </div>
  );
}

// The exact-ID managed-apply poll (cadence, grace, and deadline expiry) is
// centralized in useManagedApplyRecord; the panel consumes that shared hook
// rather than owning a second poll loop. POLL_FAST_INTERVAL_MS and
// LEGACY_OVERVIEW_MAX_POLLS bound only the best-effort runtime-overview poll used
// for pre-managed (legacy) hot applies (stream_status).
const POLL_FAST_INTERVAL_MS = 1000;
const LEGACY_OVERVIEW_MAX_POLLS = 3;

// formatLocalTime renders a server-provided ISO deadline in the operator's local
// timezone for the deadline-aware finalization display (AC-08), falling back to
// the raw string if it is unparseable so a bad timestamp never blanks the UI.
function formatLocalTime(iso: string): string {
  const parsed = new Date(iso);
  return Number.isNaN(parsed.getTime()) ? iso : parsed.toLocaleString();
}

function AssessmentPaths({
  label,
  paths,
}: {
  readonly label: string;
  readonly paths: readonly string[];
}) {
  if (paths.length === 0) return null;
  const visible = paths.slice(0, 12);
  const remaining = paths.length - visible.length;
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-jul-text">{label}</p>
      <ul className="space-y-0.5 pl-4 text-xs text-jul-muted">
        {visible.map((path) => (
          <li key={`${label}-${path}`} className="list-disc font-mono">
            {path}
          </li>
        ))}
      </ul>
      {remaining > 0 && (
        <p className="text-xs text-jul-muted">…and {String(remaining)} more paths</p>
      )}
    </div>
  );
}

/** Renders only the server's secret-safe preview projection. */
function PatchAssessment({
  draft,
  refreshing,
  refreshError,
}: {
  readonly draft: PendingPatchDraft;
  readonly refreshing: boolean;
  readonly refreshError: Error | null;
}) {
  const lifecycle = draft.lifecycle;
  return (
    <section
      aria-labelledby="patch-assessment-heading"
      className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3"
    >
      <div className="space-y-1">
        <h3
          id="patch-assessment-heading"
          className="text-xs font-semibold uppercase tracking-wider text-jul-muted"
        >
          Structured preview assessment
        </h3>
        <p className="text-sm text-jul-text">{draft.summary}</p>
      </div>

      <div className="space-y-1">
        <p className="text-xs font-medium text-jul-text">Ordered operations</p>
        <ol className="space-y-1 pl-5 text-xs text-jul-muted">
          {draft.operationSummaries.map((operation) => (
            <li key={`${String(operation.op_index)}-${operation.op}`} className="list-decimal">
              <span className="font-mono text-jul-text">{operation.op}</span>
              <span> — {operation.summary}</span>
            </li>
          ))}
        </ol>
      </div>

      {draft.validationErrors.length > 0 && (
        <div className="space-y-1 rounded-md border border-jul-danger/40 bg-jul-danger/5 p-2">
          <p className="text-xs font-medium text-jul-danger">Validation issues</p>
          {draft.validationErrors.map((issue, index) => (
            <p key={`${issue.code}-${String(index)}`} className="text-xs text-jul-text">
              {issue.path && <code className="mr-1 font-mono text-jul-danger">{issue.path}</code>}
              {issue.summary}
              {issue.detail ? ` — ${issue.detail}` : ""}
            </p>
          ))}
        </div>
      )}

      {lifecycle === undefined ? (
        <div className="rounded-md border border-jul-warning/40 bg-jul-warning/5 p-2 text-xs">
          <p className="font-medium text-jul-warning">Lifecycle preview required</p>
          <p className="mt-1 text-jul-muted">
            This draft predates the lifecycle-aware handoff. Apply remains disabled until the exact
            ordered operations are previewed again against their pinned base version.
          </p>
          {refreshing && <p className="mt-1 text-jul-muted">Refreshing preview…</p>}
          {refreshError && <p className="mt-1 text-jul-danger">{refreshError.message}</p>}
        </div>
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap gap-2 text-xs">
            <span
              className={`rounded-full px-2 py-0.5 ${
                lifecycle.can_apply_hot
                  ? "bg-jul-success/15 text-jul-success"
                  : "bg-jul-border/40 text-jul-muted"
              }`}
            >
              Hot apply: {lifecycle.can_apply_hot ? "available" : "unavailable"}
            </span>
            <span
              className={`rounded-full px-2 py-0.5 ${
                lifecycle.can_stage_restart
                  ? "bg-jul-warning/15 text-jul-warning"
                  : "bg-jul-border/40 text-jul-muted"
              }`}
            >
              Stage restart: {lifecycle.can_stage_restart ? "available" : "unavailable"}
            </span>
          </div>

          {lifecycle.changes.length > 0 && (
            <div className="space-y-1">
              <p className="text-xs font-medium text-jul-text">Lifecycle changes</p>
              <ul className="space-y-1 text-xs text-jul-muted">
                {lifecycle.changes.slice(0, 12).map((change, index) => (
                  <li key={`${change.path}-${String(index)}`} className="rounded bg-jul-bg/50 p-2">
                    <code className="font-mono text-jul-text">{change.path}</code>
                    <span> — {change.reason}</span>
                    <span className="block text-[0.7rem]">
                      {change.subsystem}: {change.declared} → {change.effective}
                    </span>
                  </li>
                ))}
              </ul>
              {lifecycle.changes.length > 12 && (
                <p className="text-xs text-jul-muted">
                  {String(lifecycle.changes.length - 12)} more lifecycle changes are summarized by
                  the path groups below.
                </p>
              )}
            </div>
          )}

          <div className="grid gap-3 sm:grid-cols-2">
            <AssessmentPaths label="Hot paths" paths={lifecycle.hot_paths} />
            <AssessmentPaths
              label="Restart-required paths"
              paths={lifecycle.restart_required_paths}
            />
            <AssessmentPaths
              label="New-listener-only paths"
              paths={lifecycle.new_listener_only_paths}
            />
            <AssessmentPaths
              label="Validation-rejected paths"
              paths={lifecycle.validation_rejected_paths}
            />
          </div>

          {lifecycle.pending_subsystems.length > 0 && (
            <p className="text-xs text-jul-muted">
              Pending subsystems: {" "}
              <span className="font-mono text-jul-text">
                {lifecycle.pending_subsystems.slice(0, 12).join(", ")}
                {lifecycle.pending_subsystems.length > 12
                  ? `, …and ${String(lifecycle.pending_subsystems.length - 12)} more`
                  : ""}
              </span>
            </p>
          )}
        </div>
      )}
    </section>
  );
}

export function ConfigPanel() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  // canApplyPerm proactively gates the primary apply action for principals that
  // lack config:apply, complementing the reactive 403 handling below. The server
  // remains authoritative; this only avoids leading the operator into a certain
  // rejection.
  const { has: hasPermission } = usePermission();
  const canApplyPerm = hasPermission("config:apply");
  const canWritePerm = hasPermission("config:write");
  const canRawPerm = hasPermission("config:raw");
  // rawForbidden is true when the current principal is authenticated but lacks
  // config:raw. Structured patch review (config:write) must still work in that
  // case, so the panel degrades gracefully instead of failing outright.
  const [rawForbidden, setRawForbidden] = useState(false);
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["raw-config"],
    queryFn: fetchRawConfig,
  });
  useEffect(() => {
    if (isError && error instanceof ApiError && error.status === 403) {
      setRawForbidden(true);
    }
  }, [isError, error]);

  // Pending-restart state: surfaced as a persistent banner and changes primary action.
  const pendingRestartQuery = useQuery({
    queryKey: ["pending-restart"],
    queryFn: fetchPendingRestart,
    staleTime: 5000,
    refetchOnWindowFocus: true,
  });
  const pendingRestartStatus: PendingRestartStatus | null =
    pendingRestartQuery.data?.status ?? null;
  // hasPendingRestart is true only for a managed staged restart. In that case
  // the primary action switches from a hot apply to an update of the staged
  // configuration.
  const hasPendingRestart = pendingRestartStatus !== null && pendingRestartStatus.staged;
  // restartBlocked is true whenever hot applies are not allowed: managed
  // staged, external disk/runtime divergence, or inconsistent marker state
  // (F-04).
  const restartBlocked =
    pendingRestartStatus !== null &&
    (pendingRestartStatus.staged ||
      pendingRestartStatus.external ||
      pendingRestartStatus.inconsistent);

  // Raw editor state
  const [draft, setDraft] = useState<string | null>(null);
  const [baseline, setBaseline] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [stageConfirming, setStageConfirming] = useState(false);

  // AC-13: every interlocking piece of write state — operation generation, the
  // correlated apply result + its kind, the pending patch draft, the base and
  // conflict versions, the admin-confirmation scope, and the bounded poll budget
  // — lives in the extracted, unit-tested mutation machine
  // (configMutationMachine.ts), bound to React by useConfigMutationMachine. The
  // panel keeps only editor-text and dialog/UI state locally. The synchronous
  // refs (operationIDRef / confirmedAdminOperationRef / postApplyPollAttemptsRef)
  // mirror the reducer so async closures can read them without a re-render.
  const machine = useConfigMutationMachine();
  const {
    operationIDRef,
    confirmedAdminOperationRef,
    postApplyPollAttemptsRef,
    appliedState,
    patchDraft,
    baseVersion,
    conflictVersion,
    setAppliedState,
    mergeTerminalRecord,
    setPatchDraft,
    setBaseVersion,
    setConflictVersion,
    startOperation: machineStartOperation,
    cancelOperation: machineCancelOperation,
    confirmAdminForOperation,
  } = machine;
  const applied = appliedState?.result ?? null;

  // Patch-reconcile status is pure editor-text UI concern, so it stays local.
  const [patchReconciling, setPatchReconciling] = useState(false);
  const [patchReconcileError, setPatchReconcileError] = useState<Error | null>(null);

  // Wrap the machine's operation-lifecycle transitions to also clear the local
  // reconcile UI state, preserving the panel's original startOperation/cancel
  // semantics.
  const startOperation = useCallback((): number => {
    setPatchReconcileError(null);
    return machineStartOperation();
  }, [machineStartOperation]);

  const cancelOperation = useCallback((): void => {
    setPatchReconciling(false);
    setPatchReconcileError(null);
    machineCancelOperation();
  }, [machineCancelOperation]);

  const refreshEditorAfterFailure = useCallback(
    (operationID: number): void => {
      if (rawForbidden) return;
      void qc
        .fetchQuery({ queryKey: ["raw-config"], queryFn: fetchRawConfig, staleTime: 0 })
        .then((fresh) => {
          if (operationIDRef.current !== operationID) return;
          setBaseline(fresh.raw ?? "");
          setDraft(fresh.raw ?? "");
          setBaseVersion(fresh.base_version);
        })
        .catch(() => undefined);
    },
    [qc, rawForbidden, operationIDRef, setBaseVersion],
  );

  const reconcilePatchEditor = useCallback(
    (operationID: number, fallbackVersion?: string): void => {
      if (rawForbidden) {
        void navigate("/routes");
        return;
      }
      setPatchReconciling(true);
      setPatchReconcileError(null);
      void qc
        .fetchQuery({ queryKey: ["raw-config"], queryFn: fetchRawConfig, staleTime: 0 })
        .then((fresh) => {
          if (operationIDRef.current !== operationID) return;
          const raw = fresh.raw ?? "";
          setPatchDraft(null);
          setBaseline(raw);
          setDraft(raw);
          setBaseVersion(fresh.base_version ?? fallbackVersion);
          setPatchReconciling(false);
        })
        .catch((error: unknown) => {
          if (operationIDRef.current !== operationID) return;
          setPatchReconciling(false);
          setPatchReconcileError(
            error instanceof Error
              ? error
              : new Error("Unable to reload the applied configuration."),
          );
        });
    },
    [navigate, qc, rawForbidden, operationIDRef, setBaseVersion, setPatchDraft],
  );

  // Seed the editor once the raw config arrives. A pending handoff, if present,
  // becomes the draft so the operator lands on a ready-to-review diff. When the
  // principal lacks config:raw, raw config is absent but a structured patch
  // handoff can still be reviewed and applied. The initialized ref prevents the
  // effect from re-taking the handoff if React re-runs it before state updates
  // have committed.
  const initializedRef = useRef(false);
  useEffect(() => {
    if (initializedRef.current) return;
    if (!data && !rawForbidden) return;
    initializedRef.current = true;
    const handoff = takePendingDraft();
    if (data) {
      const raw = data.raw ?? "";
      setBaseVersion(data.base_version);
      if (handoff) {
        if (handoff.kind === "toml") {
          setBaseline(raw);
          setDraft(handoff.toml);
        } else {
          setPatchDraft(handoff);
          setBaseline(raw);
          // Seed with the persisted baseline until the server candidate is
          // fetched (see the config-patch-candidate query below); the editor
          // then flips to the read-only proposed candidate.
          setDraft(handoff.candidate ?? raw);
        }
      } else {
        setBaseline(raw);
        setDraft(raw);
      }
    } else if (rawForbidden && handoff?.kind === "patch") {
      setPatchDraft(handoff);
      setBaseline("");
      setDraft(handoff.candidate ?? "");
    }
  }, [data, rawForbidden, setBaseVersion, setPatchDraft]);

  const current = draft ?? "";
  const isPatchMode = patchDraft !== null;
  const dirty = isPatchMode || (draft !== null && draft !== baseline);
  const debounced = useDebouncedValue(isPatchMode ? "" : current, 400);

  // Backward-compatible session migration: a pre-#78 draft does not carry the
  // authoritative lifecycle projection. Re-preview the exact ordered batch at
  // its pinned base version before any apply action can become available.
  const patchNeedsAssessmentRefresh =
    isPatchMode &&
    (patchDraft.lifecycle === undefined ||
      patchDraft.baseVersion === undefined ||
      patchDraft.baseVersion.trim() === "");
  const patchAssessmentRefresh = useQuery({
    queryKey: [
      "config-patch-assessment-refresh",
      patchDraft?.baseVersion,
      patchDraft?.ops ?? [],
    ],
    queryFn: async () => {
      if (patchDraft === null) throw new Error("No structured patch is available to preview.");
      const result = await patchConfigBatch(patchDraft.ops, patchDraft.baseVersion);
      return patchResultToPendingDraft(patchDraft.ops, result, patchDraft.baseVersion);
    },
    enabled: patchNeedsAssessmentRefresh && canWritePerm,
    retry: false,
    staleTime: Infinity,
  });

  useEffect(() => {
    const refreshed = patchAssessmentRefresh.data;
    if (refreshed === undefined || patchDraft === null || !patchNeedsAssessmentRefresh) return;
    setPatchDraft(refreshed);
  }, [patchAssessmentRefresh.data, patchDraft, patchNeedsAssessmentRefresh, setPatchDraft]);

  const validation = useQuery({
    queryKey: ["config-validate", debounced],
    queryFn: () => validateConfig(debounced),
    enabled: !isPatchMode && draft !== null && debounced.length > 0,
    staleTime: Infinity,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const patchLifecycleRejected =
    patchDraft?.lifecycle !== undefined &&
    patchDraft.lifecycle.validation_rejected_paths.length > 0;
  const valid = isPatchMode
    ? patchDraft.valid && patchDraft.validationErrors.length === 0 && !patchLifecycleRejected
    : validation.data?.ok === true;
  const patchAction = decidePatchApplyAction(
    patchDraft,
    hasPendingRestart,
    isPatchMode && conflictVersion !== undefined,
  );

  const rawDiff = useQuery({
    queryKey: ["config-diff", debounced],
    queryFn: () => diffConfig(debounced),
    enabled: !isPatchMode && dirty && valid,
    staleTime: Infinity,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  });

  // In patch mode the diff is pre-computed; in raw mode it is fetched.
  const previewDiff: ConfigDiff | undefined = useMemo(
    () => patchDraft?.previewDiff ?? rawDiff.data,
    [patchDraft, rawDiff.data],
  );

  // N-01 (WS05): when a config:raw operator is reviewing a structured patch,
  // fetch the true server-computed candidate TOML so the source view can show
  // the PROPOSED configuration instead of the current persisted bytes. The
  // endpoint is config:raw-gated and pins base_version, so a concurrent change
  // surfaces as a stale preview (409) rather than being silently clobbered.
  const patchCandidateQuery = useQuery({
    queryKey: ["config-patch-candidate", patchDraft?.baseVersion, patchDraft?.ops ?? []],
    queryFn: async () => {
      const expectedBaseVersion = patchDraft?.baseVersion;
      const result = await fetchPatchCandidate(patchDraft?.ops ?? [], expectedBaseVersion);
      if (expectedBaseVersion !== undefined && result.base_version !== expectedBaseVersion) {
        throw new Error("The candidate response did not match the reviewed base version.");
      }
      return result;
    },
    enabled:
      isPatchMode &&
      canRawPerm &&
      !rawForbidden &&
      patchDraft.baseVersion !== undefined &&
      patchDraft.lifecycle !== undefined &&
      patchDraft.candidate === undefined,
    retry: false,
    staleTime: Infinity,
  });
  const candidateError = patchCandidateQuery.error;
  // A 409 means the persisted config moved since the preview was prepared and
  // must block apply. A 403 or an ordinary source-view failure hides candidate
  // TOML and degrades to the already reviewed structured diff; config:raw is a
  // rendering permission, not an additional prerequisite for atomic apply.
  const candidateStale = candidateError instanceof ConfigConflictError;
  const candidateForbidden = candidateError instanceof ApiError && candidateError.status === 403;
  const candidateFailed = patchCandidateQuery.isError && !candidateStale && !candidateForbidden;
  // Wait only while the optional source view is actively loading. A stale
  // base remains blocking, but a non-conflict source failure degrades to the
  // structured diff because final apply recomputes the exact ops server-side.
  const candidatePending =
    isPatchMode &&
    canRawPerm &&
    !rawForbidden &&
    patchCandidateQuery.isFetching &&
    patchDraft.baseVersion !== undefined &&
    patchDraft.lifecycle !== undefined &&
    patchDraft.candidate === undefined;

  // Once the protected candidate arrives, pin it into the draft so the
  // read-only editor shows the proposed configuration and the source view
  // flips from "persisted-baseline" to "candidate-readonly".
  useEffect(() => {
    const candidate = patchCandidateQuery.data?.candidate;
    if (candidate === undefined) return;
    if (patchDraft === null || patchDraft.candidate !== undefined) return;
    setPatchDraft({ ...patchDraft, candidate });
    setDraft(candidate);
  }, [patchCandidateQuery.data, patchDraft, setPatchDraft]);

  const applyRaw = useMutation({
    mutationFn: ({ confirmAdmin }: { confirmAdmin: boolean; operationID: number }) =>
      applyConfig(current, baseVersion, confirmAdmin, "hot"),
    onSuccess: (res, variables) => {
      if (operationIDRef.current !== variables.operationID) return;
      setBaseline(current);
      setAppliedState({
        operationID: variables.operationID,
        result: res,
        errorKind: null,
        wasPatch: false,
        patchCandidate: null,
      });
      setConfirming(false);
      // Advance the token to the freshly-applied version so a follow-up edit
      // does not trip a spurious conflict.
      setBaseVersion(res.version ?? undefined);
      setConflictVersion(undefined);
      void qc.invalidateQueries({ queryKey: ["pending-restart"] });
      void qc.invalidateQueries();
    },
    onError: (err, variables) => {
      if (operationIDRef.current !== variables.operationID) return;
      if (!(err instanceof ConfigAdminChangeError)) setConfirming(false);
      if (err instanceof ConfigConflictError) {
        setConflictVersion(err.currentVersion);
      }
      if (err instanceof ConfigApplyOutcomeError) {
        setAppliedState({
          operationID: variables.operationID,
          result: err.result,
          errorKind: err.kind,
          wasPatch: false,
          patchCandidate: null,
        });
        if (err.result.restored || err.result.reload?.outcome === "not_applied") {
          refreshEditorAfterFailure(variables.operationID);
        }
      }
    },
  });

  const applyPatch = useMutation({
    mutationFn: ({ confirmAdmin }: { confirmAdmin: boolean; operationID: number }) => {
      if (!patchDraft) throw new Error("no patch draft to apply");
      if (patchDraft.baseVersion === undefined || patchDraft.baseVersion.trim() === "") {
        throw new Error("The structured patch has no pinned base version; generate a fresh preview.");
      }
      // This mutation is reached only when the authoritative preview permits
      // hot apply. Restart-bound and update-staged patches use applyStage
      // directly, avoiding a known-doomed hot submission.
      return applyPatchBatch(
        patchDraft.ops,
        patchDraft.baseVersion,
        "hot",
        confirmAdmin,
      );
    },
    onSuccess: (res, variables) => {
      if (operationIDRef.current !== variables.operationID) return;
      // Reconcile the raw-editor state with the freshly-applied patch: the
      // candidate is now the persisted config and res.version is its
      // fingerprint. Without this, exiting patch mode leaves the editor looking
      // dirty (draft still the candidate, baseline still the old config) and a
      // follow-up raw apply trips a spurious 409 on the stale baseVersion.
      const candidate = patchDraft?.candidate ?? null;
      if (res.reload?.outcome !== "saved_not_live") {
        reconcilePatchEditor(variables.operationID, res.version);
      }
      setBaseVersion(res.version ?? undefined);
      // D3 (H-06): use reload.outcome when present for authoritative result.
      setAppliedState({
        operationID: variables.operationID,
        result: res,
        errorKind: null,
        wasPatch: true,
        patchCandidate: candidate,
      });
      setConfirming(false);
      setConflictVersion(undefined);
      void qc.invalidateQueries({ queryKey: ["pending-restart"] });
      void qc.invalidateQueries();
    },
    onError: (err, variables) => {
      if (operationIDRef.current !== variables.operationID) return;
      if (!(err instanceof ConfigAdminChangeError)) setConfirming(false);
      if (err instanceof ConfigConflictError) {
        setConflictVersion(err.currentVersion);
      }
      if (err instanceof ConfigApplyOutcomeError) {
        setAppliedState({
          operationID: variables.operationID,
          result: err.result,
          errorKind: err.kind,
          wasPatch: true,
          patchCandidate: patchDraft?.candidate ?? current,
        });
        if (err.result.restored || err.result.reload?.outcome === "not_applied") {
          refreshEditorAfterFailure(variables.operationID);
        }
      }
    },
  });

  // Stage-restart apply: sends mode=stage_restart; no live reload, just saves for next boot.
  const applyStage = useMutation({
    mutationFn: ({ confirmAdmin }: { confirmAdmin: boolean; operationID: number }) => {
      if (patchDraft) {
        if (patchDraft.baseVersion === undefined || patchDraft.baseVersion.trim() === "") {
          throw new Error(
            "The structured patch has no pinned base version; generate a fresh preview.",
          );
        }
        return applyPatchBatch(
          patchDraft.ops,
          patchDraft.baseVersion,
          "stage_restart",
          confirmAdmin,
        );
      }
      return applyConfig(current, baseVersion, confirmAdmin, "stage_restart");
    },
    onSuccess: (res, variables) => {
      if (operationIDRef.current !== variables.operationID) return;
      const wasPatch = patchDraft !== null;
      const candidate = patchDraft?.candidate ?? null;
      setAppliedState({
        operationID: variables.operationID,
        result: res,
        errorKind: null,
        wasPatch,
        patchCandidate: wasPatch ? candidate : null,
      });
      if (wasPatch) reconcilePatchEditor(variables.operationID, res.version);
      else setBaseline(current);
      setStageConfirming(false);
      setBaseVersion(res.version ?? undefined);
      setConflictVersion(undefined);
      applyRaw.reset();
      applyPatch.reset();
      void qc.invalidateQueries({ queryKey: ["pending-restart"] });
      void qc.invalidateQueries();
    },
    onError: (err, variables) => {
      if (operationIDRef.current !== variables.operationID) return;
      if (err instanceof ConfigConflictError) {
        setConflictVersion(err.currentVersion);
      }
      if (err instanceof ConfigApplyOutcomeError) {
        setAppliedState({
          operationID: variables.operationID,
          result: err.result,
          errorKind: err.kind,
          wasPatch: patchDraft !== null,
          patchCandidate: patchDraft?.candidate ?? null,
        });
      }
    },
  });

  // Discard the staged restart (returns the live config, not the staged one).
  const discard = useMutation({
    mutationFn: discardPendingRestart,
    onSuccess: () => {
      cancelOperation();
      setAppliedState(null);
      void qc.invalidateQueries({ queryKey: ["pending-restart"] });
      void qc.invalidateQueries({ queryKey: ["raw-config"] });
      void qc.invalidateQueries();
    },
  });

  // Finding 13 (H-03): when a structured patch is applied while a managed staged
  // restart is pending, the backend routes it to stage_restart — it replaces the
  // STAGED configuration and does NOT touch the running runtime. The UI must say
  // so explicitly rather than showing the ordinary "apply now / apply patch"
  // copy, which implies a live change. One derived flag drives the button label,
  // dialog title, confirm label, explanatory copy, and success wording.
  const updatingStagedPatch = isPatchMode && patchAction.action === "update_staged";
  const patchUsesStage =
    isPatchMode &&
    (patchAction.action === "stage_restart" || patchAction.action === "update_staged");
  const patchHasNoAction = isPatchMode && patchAction.action === "none";

  const applyActive = isPatchMode ? applyPatch : applyRaw;
  const applyError = applyActive.error;
  // A raw apply that would change how the operator reaches the admin console is
  // rejected with a 409 the first time; the same confirm modal then re-applies
  // with confirm_admin=true. Derived from the error so no extra state is needed.
  const adminChangeError = applyError instanceof ConfigAdminChangeError ? applyError : null;
  const restartError = applyError instanceof ConfigRestartRequiredError ? applyError : null;
  const pendingApplyID =
    applied?.reload?.outcome === "saved_not_live" ? applied.apply_id : undefined;
  // AC-14: every managed result carries an exact apply id — not only a
  // saved-not-live (202) one. An immediate terminal result (200/409/503) is
  // finalized synchronously, but its finalization provenance (history snapshot
  // id, history/finalization errors) lives only on the ledger record, so it is
  // fetched too. pendingApplyID still gates the "pending/expired" UI; the
  // supplemental immediate fetch never changes the already-shown runtime banner.
  const managedApplyID = applied?.apply_id;
  const legacyApplyPending =
    applied !== null &&
    (applied.mode ?? "hot") === "hot" &&
    applied.reload === undefined &&
    applied.pending_reload !== false;
  // Legacy (pre-managed) hot applies carry no per-ID ledger record, so the only
  // supplemental signal is the runtime overview's async stream_status. This is a
  // best-effort, short-lived poll — it never upgrades an uncorrelated result to
  // "live" (AC-11); it only surfaces the L4 stream reload state.
  const legacyOverview = useQuery({
    queryKey: ["config-apply-legacy-overview", appliedState?.operationID],
    queryFn: async () => {
      postApplyPollAttemptsRef.current += 1;
      return fetchOverview();
    },
    enabled: legacyApplyPending,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchInterval: () =>
      postApplyPollAttemptsRef.current >= LEGACY_OVERVIEW_MAX_POLLS ? false : POLL_FAST_INTERVAL_MS,
  });
  // AC-09/AC-14: a managed apply's terminal ledger record is retrieved by its
  // EXACT apply id at GET /api/config/applies/{id} — never the runtime
  // overview's global last_managed_apply, which a newer unrelated apply could
  // overwrite. The exact-ID poll (immediate first read, deadline-bounded
  // cadence, read-after-write 404 grace, and expiry) is owned by the shared
  // useManagedApplyRecord hook so this panel and HistoryPanel observe one
  // lifecycle. A missing record is NEVER treated as success. Both a
  // saved-not-live (202) and an immediate terminal result (200/409/503) carry an
  // exact apply id whose finalization provenance lives only on the ledger record,
  // so the hook is enabled for any managed apply id.
  const managedApply = useManagedApplyRecord(managedApplyID);
  const applyRecord = managedApply.record;
  // The pending managed apply did not reach a terminal result by its deadline
  // (deadline-driven, not attempt-count-driven). The console must not claim the
  // new configuration is serving. Gated on pendingApplyID so an immediate
  // result's supplemental fetch never trips the pending/expired UI.
  const pollingExpired = pendingApplyID !== undefined && managedApply.status === "expired";

  // AC-14: finalization provenance is advisory, never readiness-affecting. It is
  // derived from the retained full terminal record (Slice 02) and rendered as a
  // non-blocking banner alongside — never replacing — the apply outcome. A
  // committed apply can be ok=true while its history/finalization sidecar
  // degraded; that is surfaced here, never as an apply failure.
  const finalizationAdvisory = deriveFinalizationAdvisory(appliedState?.terminalRecord ?? null);
  // AC-08: while a saved-not-live apply is still being finalized, tell the
  // operator when the result is expected. The absolute deadline comes from the
  // exact-ID ledger record (pending 202 records carry it); it is dropped once
  // the poll expires so the UI switches to the past-deadline message instead.
  const pendingDeadline =
    pendingApplyID !== undefined && !pollingExpired ? applyRecord?.deadline : undefined;

  useEffect(() => {
    if (managedApplyID === undefined || !appliedState) return;
    const record = applyRecord;
    // AC-09: only a *terminal* record for the EXACT awaited id is retained. A
    // pending (202) record, a missing record (null/404), or a record for any
    // other id is never treated as success.
    if (!record || record.state !== "terminal" || record.id !== managedApplyID) return;
    if (appliedState.result.apply_id !== managedApplyID) return;
    // Idempotent: once the exact terminal record is retained, do not re-merge —
    // this also prevents the merge from re-triggering the effect indefinitely.
    if (appliedState.terminalRecord !== null) return;
    const operationID = appliedState.operationID;
    const wasPatch = appliedState.wasPatch;
    const priorVersion = appliedState.result.version;
    const terminal = record.result;
    // Retain the FULL terminal record (finalization provenance included) and
    // merge its apply result into the applied result.
    mergeTerminalRecord(record);
    // Editor reconciliation is only for a polled saved-not-live finalization; an
    // immediate terminal result already reconciled in the mutation's onSuccess,
    // and its supplemental fetch must not re-drive the editor or the banner.
    if (pendingApplyID === undefined) return;
    if (operationIDRef.current !== operationID) return;
    if (terminal.ok && wasPatch) {
      reconcilePatchEditor(operationID, priorVersion);
    }
    if (terminal.restored || !terminal.ok) {
      refreshEditorAfterFailure(operationID);
    }
  }, [
    appliedState,
    managedApplyID,
    pendingApplyID,
    applyRecord,
    reconcilePatchEditor,
    refreshEditorAfterFailure,
    operationIDRef,
    mergeTerminalRecord,
  ]);

  // Fold the raw apply signals into one explicit, severity-tagged outcome so the
  // operator can tell a fully-live apply from one that still needs a restart or
  // that only partially reloaded (AUX-02). Restart-required is an accepted=false
  // outcome routed through the same renderer for consistent wording.
  const outcome: ApplyOutcome | null = useMemo(() => {
    if (restartError) {
      return deriveApplyOutcome({
        accepted: false,
        pendingReload: false,
        runtimeObserved: false,
        restartMessage: restartError.message,
      });
    }
    if (applied) {
      const reload = applied.reload;
      const mode = applied.mode ?? "hot";
      if (appliedState?.errorKind === "pending-restart") {
        const pending = applied.pending_restart;
        const managedStaged =
          pending?.state === "managed_staged" ||
          (pending?.state === undefined && pending?.managed === true && pending.staged);
        if (!managedStaged) return null;
        return deriveApplyOutcome({
          accepted: false,
          pendingReload: false,
          runtimeObserved: false,
          pendingRestartBlocksHot: true,
        });
      }
      if (appliedState?.errorKind === "timeout") {
        // AC-08: a pre-persistence preflight timeout (504). Nothing was
        // persisted; render the dedicated preflight-timeout outcome naming the
        // phase from the top-level timed_out_phase.
        return deriveApplyOutcome({
          accepted: false,
          pendingReload: false,
          runtimeObserved: false,
          preflightTimedOut: true,
          ...(applied.timed_out_phase !== undefined
            ? { timedOutPhase: applied.timed_out_phase }
            : {}),
        });
      }
      if (
        appliedState?.errorKind !== null &&
        appliedState?.errorKind !== "not-applied" &&
        appliedState?.errorKind !== "enqueue"
      ) {
        return null;
      }
      if (mode === "stage_restart") {
        return deriveApplyOutcome({
          accepted: applied.ok,
          pendingReload: false,
          runtimeObserved: false,
          mode: "stage_restart",
          isStagedUpdate: applied.staged_restart_is_update ?? false,
          http: reload?.http,
          stream: reload?.stream,
          admin: reload?.admin,
          persisted: applied.persisted ?? reload?.persisted,
          failedPhase: reload?.failed_phase,
          timedOutPhase: reload?.timed_out_phase,
        });
      }
      // AC-11: A legacy (pre-managed) hot apply returns no correlated per-ID
      // reload record. Mark it uncorrelated so the outcome projection refuses to
      // claim "Applied and live" on trust alone; it may only reach the live
      // branch when the persisted and serving versions demonstrably match. The
      // versions come from the response itself (serving_version/persisted_version)
      // and, failing that, the best-effort overview's last_reload. A managed
      // apply (reload present) stays fully correlated and is unaffected.
      const legacyUncorrelated = reload === undefined;
      const legacyPersistedVersion = applied.persisted_version ?? applied.version;
      const legacyServingVersion =
        applied.serving_version ?? legacyOverview.data?.last_reload?.serving_version;
      return deriveApplyOutcome({
        accepted: applied.ok,
        pendingReload:
          reload?.outcome === "saved_not_live" ||
          (reload === undefined ? (applied.pending_reload ?? true) : false),
        runtimeObserved:
          reload !== undefined
            ? reload.outcome !== "saved_not_live"
            : applied.pending_reload === false,
        reloadTimedOut: reload?.timed_out === true,
        savedNotLive: reload?.outcome === "saved_not_live",
        ...(reload?.outcome !== undefined ? { reloadOutcome: reload.outcome } : {}),
        ...(reload?.published !== undefined ? { published: reload.published } : {}),
        ...(applied.restored !== undefined ? { restored: applied.restored } : {}),
        ...(applied.restore_error !== undefined ? { restoreError: applied.restore_error } : {}),
        ...(reload?.error !== undefined ? { reloadError: reload.error } : {}),
        enqueueFailed: reload?.failed_phase === "enqueue",
        ...(reload === undefined && legacyOverview.data?.stream_status !== undefined
          ? { streamStatus: legacyOverview.data.stream_status }
          : {}),
        ...(legacyUncorrelated
          ? {
              correlated: false,
              ...(legacyPersistedVersion !== undefined
                ? { persistedVersion: legacyPersistedVersion }
                : {}),
              ...(legacyServingVersion !== undefined
                ? { servingVersion: legacyServingVersion }
                : {}),
            }
          : {}),
        http: reload?.http,
        stream: reload?.stream,
        admin: reload?.admin,
        persisted: applied.persisted ?? reload?.persisted,
        failedPhase: reload?.failed_phase,
        timedOutPhase: reload?.timed_out_phase,
      });
    }
    return null;
  }, [restartError, applied, appliedState?.errorKind, legacyOverview.data]);

  // AC-11: In development builds, warn once when a legacy uncorrelated apply is
  // surfaced. The pre-managed hot-apply path returns no per-ID reload record and
  // cannot prove the runtime went live; it is retained only for backward
  // compatibility and should be replaced by the managed apply flow.
  useEffect(() => {
    if (import.meta.env.DEV && outcome?.kind === "saved-uncorrelated") {
      console.warn(
        "[jul-console] Deprecated: a configuration apply returned without a correlated " +
          "per-operation reload record. The console cannot confirm the runtime went live and " +
          'is showing "Saved; runtime status uncorrelated". This legacy apply path is deprecated; ' +
          "prefer the managed apply flow that emits a per-ID ledger record.",
      );
    }
  }, [outcome?.kind]);

  const appliedCapabilities = applied?.status
    ? { active: applied.status.filter((s) => s.active).length, total: applied.status.length }
    : undefined;

  // AC-12 / N-01 (WS05): label the editor's source truthfully so an operator is
  // never misled about what they are looking at. There are four distinct states:
  //   - persisted-editable: the bytes stored on disk, shown editable in raw mode
  //                 (config:raw operators authoring a raw change). The runtime
  //                 may differ from disk, so this is never called "live".
  //   - candidate-readonly: a server-computed PROPOSED candidate (structured
  //                 patch), shown read-only — it is NOT applied until confirm.
  //   - persisted-baseline: patch mode where the candidate is still loading or
  //                 stale; the editor shows the current persisted bytes and the
  //                 structured diff is the change.
  //   - diff-only:  candidate source is unavailable or not authorized, so only
  //                 the secret-safe structured diff/summary is shown.
  // The candidate is computed server-side against a pinned base_version, which is
  // echoed back on apply so a concurrent change is rejected, not silently
  // clobbered.
  const sourceView: {
    readonly tone: "persisted-editable" | "candidate-readonly" | "persisted-baseline" | "diff-only";
    readonly label: string;
    readonly detail: string;
  } = !isPatchMode
    ? {
        tone: "persisted-editable",
        label: "Persisted configuration — editable",
        detail:
          "These are the bytes currently stored in the configuration file. The runtime may differ while a staged restart or external divergence exists.",
      }
    : rawForbidden || !canRawPerm || candidateForbidden || candidateFailed
      ? {
          tone: "diff-only",
          label: "Proposed change — diff only",
          detail: candidateFailed
            ? "Candidate source is temporarily unavailable. The server-reviewed structured diff remains the proposed change."
            : "The full candidate is hidden because this principal lacks config:raw. The structured diff is the proposed change.",
        }
      : patchDraft.candidate !== undefined
        ? {
            tone: "candidate-readonly",
            label: "Proposed candidate — read-only",
            detail:
              "This candidate was generated server-side from the reviewed patch and pinned base version. It is not applied until confirmation.",
          }
        : {
            tone: "persisted-baseline",
            label: "Current persisted baseline",
            detail:
              "The structured diff is the proposed change. Candidate TOML is not currently available.",
          };

  if (isLoading) return <Loading label="Loading configuration…" />;
  if ((isError || !data) && !(rawForbidden && isPatchMode))
    return <PanelError error={error} resource="the configuration" onRetry={() => void refetch()} />;

  if (data?.raw === undefined && draft === null && !rawForbidden) {
    return (
      <div className="space-y-2">
        <h1 className="text-xl font-semibold">Configuration</h1>
        <p className="text-sm text-jul-muted">Raw config not available (read hook not wired).</p>
      </div>
    );
  }

  const pill: "idle" | "checking" | "valid" | "invalid" = isPatchMode
    ? patchAssessmentRefresh.isFetching
      ? "checking"
      : valid
        ? "valid"
        : "invalid"
    : validation.isFetching
      ? "checking"
      : validation.data === undefined
        ? "idle"
        : valid
          ? "valid"
          : "invalid";
  const issues = isPatchMode ? patchDraft.validationErrors : (validation.data?.errors ?? []);

  return (
    <div className="flex h-full flex-col gap-4">
      {/* Persistent planned-restart banner */}
      {restartBlocked && (
        <PendingRestartBanner
          status={pendingRestartStatus}
          onDiscard={() => {
            discard.mutate();
          }}
          discarding={discard.isPending}
          discardError={discard.error instanceof Error ? discard.error : null}
        />
      )}

      <div className="flex flex-wrap items-center gap-3">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">Configuration</h1>
          <p className="max-w-3xl text-sm text-jul-muted">
            Live TOML editor with validation, diff previews, and atomic patch support. Review every
            change before it is applied to make sure the configuration remains sound.
          </p>
        </div>
        {data?.path && <span className="font-mono text-xs text-jul-muted">{data.path}</span>}
        <ValidationPill state={pill} />
        {dirty && <span className="text-xs text-jul-warning">● unsaved changes</span>}
        {isPatchMode && (
          <span className="rounded-full border border-jul-accent/30 bg-jul-accent/10 px-2 py-0.5 text-xs text-jul-accent">
            atomic patch
          </span>
        )}
        <div className="ml-auto flex gap-2">
          <button
            type="button"
            onClick={() => {
              void navigate(-1);
            }}
            className="rounded-md border border-jul-border px-3 py-1 text-sm text-jul-text hover:bg-jul-surface"
          >
            ← Go back
          </button>
          <button
            type="button"
            onClick={() => {
              cancelOperation();
              setDraft(baseline);
              setPatchDraft(null);
              setAppliedState(null);
              setConflictVersion(undefined);
              applyRaw.reset();
              applyPatch.reset();
              applyStage.reset();
            }}
            disabled={
              !dirty ||
              applyActive.isPending ||
              applyStage.isPending ||
              patchReconciling ||
              patchReconcileError !== null ||
              pendingApplyID !== undefined
            }
            className="rounded-md border border-jul-border px-3 py-1 text-sm text-jul-muted hover:text-jul-text disabled:opacity-40"
          >
            Reset
          </button>
          <button
            type="button"
            onClick={() => {
              startOperation();
              // Route a known restart-bound patch directly to stage_restart;
              // never submit it once in hot mode merely to learn what preview
              // already established. Managed pending raw edits keep their
              // established update-staged behavior.
              if (patchUsesStage || (hasPendingRestart && !isPatchMode && !rawForbidden)) {
                setStageConfirming(true);
              } else {
                setConfirming(true);
              }
            }}
            disabled={
              !dirty ||
              !valid ||
              pendingApplyID !== undefined ||
              applyActive.isPending ||
              applyStage.isPending ||
              patchReconciling ||
              patchReconcileError !== null ||
              candidatePending ||
              candidateStale ||
              patchHasNoAction ||
              (restartBlocked && !hasPendingRestart) ||
              (rawForbidden && !isPatchMode) ||
              !canApplyPerm
            }
            className="inline-flex items-center gap-2 rounded-md bg-jul-accent px-3 py-1 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {(applyActive.isPending || applyStage.isPending) && <Spinner />}
            {applyActive.isPending || applyStage.isPending
              ? patchUsesStage
                ? "Saving…"
                : "Applying…"
              : hasPendingRestart && !isPatchMode && !rawForbidden
                ? "Update staged configuration"
                : patchAction.action === "update_staged"
                  ? "Update staged configuration"
                  : patchAction.action === "stage_restart"
                    ? "Save for next restart"
                    : patchAction.action === "hot"
                      ? "Apply patch"
                      : isPatchMode
                        ? "No safe apply action"
                        : "Apply changes"}
          </button>
        </div>
      </div>
      <ForbiddenAction permission="config:apply" className="justify-end" />

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[3fr_2fr]">
        <div className="flex min-h-0 flex-col overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
          {/* AC-12: truthful source-of-truth label — persisted-editable vs proposed candidate vs persisted baseline vs diff-only. */}
          <div
            data-source-view={sourceView.tone}
            className={`flex flex-shrink-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 border-b px-3 py-1.5 text-xs ${
              sourceView.tone === "persisted-editable"
                ? "border-jul-border bg-jul-bg/40"
                : "border-jul-accent/30 bg-jul-accent/5"
            }`}
          >
            <span
              className={`font-semibold ${
                sourceView.tone === "persisted-editable" ? "text-jul-text" : "text-jul-accent"
              }`}
            >
              {sourceView.label}
            </span>
            <span className="text-jul-muted">{sourceView.detail}</span>
          </div>
          <Suspense fallback={<EditorFallback />}>
            {draft !== null && !(rawForbidden && isPatchMode && !patchDraft.candidate) && (
              <CodeEditor
                value={draft}
                readOnly={
                  isPatchMode ||
                  rawForbidden ||
                  applyActive.isPending ||
                  applyStage.isPending ||
                  patchReconciling ||
                  patchReconcileError !== null ||
                  pendingApplyID !== undefined
                }
                onChange={(next) => {
                  cancelOperation();
                  setDraft(next);
                  if (applied) setAppliedState(null);
                  setConflictVersion(undefined);
                  applyRaw.reset();
                  applyPatch.reset();
                  applyStage.reset();
                }}
              />
            )}
            {rawForbidden && isPatchMode && !patchDraft.candidate && (
              <div className="flex h-full items-center justify-center p-6 text-sm text-jul-muted">
                <p>
                  Raw configuration preview is hidden because you do not have the{" "}
                  <span className="font-mono">config:raw</span> permission. The diff and a summary
                  of the structured change are shown on the right.
                </p>
              </div>
            )}
          </Suspense>
        </div>

        <div className="min-h-0 space-y-4 overflow-auto">
          {outcome && (
            <ApplyOutcomeBanner
              outcome={outcome}
              {...(appliedCapabilities ? { capabilities: appliedCapabilities } : {})}
            />
          )}
          {/* N-01 (WS05): the pinned base_version moved before the candidate
              could be computed. This is not a success — apply is disabled and the
              operator must regenerate the preview from the originating editor. */}
          {isPatchMode && candidateStale && (
            <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-sm">
              <p className="font-medium text-jul-warning">Preview is stale</p>
              <p className="mt-1 text-xs text-jul-muted">
                The persisted configuration changed since this patch was prepared, so the proposed
                candidate could not be generated. Return to the originating editor and regenerate the
                preview before applying.
              </p>
            </div>
          )}
          {isPatchMode && candidateFailed && (
            <div className="rounded-md border border-jul-danger/40 bg-jul-danger/5 p-3 text-sm">
              <p className="font-medium text-jul-danger">Candidate unavailable</p>
              <p className="mt-1 text-xs text-jul-muted">
                Candidate source could not be loaded. The structured diff below remains the reviewed
                proposed change, and final apply will recompute the exact ordered operations
                server-side.
              </p>
            </div>
          )}
          {isPatchMode && (
            <PatchAssessment
              draft={patchDraft}
              refreshing={patchAssessmentRefresh.isFetching}
              refreshError={
                patchAssessmentRefresh.error instanceof Error
                  ? patchAssessmentRefresh.error
                  : null
              }
            />
          )}
          {isPatchMode && patchHasNoAction && (
            <div
              role="status"
              className="rounded-md border border-jul-warning/40 bg-jul-warning/5 p-3 text-sm"
            >
              <p className="font-medium text-jul-warning">No apply action is available</p>
              <p className="mt-1 text-xs text-jul-muted">{patchAction.reason}</p>
              {patchAction.requiresFreshPreview && !canWritePerm && (
                <p className="mt-1 text-xs text-jul-muted">
                  Refreshing this preview requires the <span className="font-mono">config:write</span>{" "}
                  permission.
                </p>
              )}
            </div>
          )}
          {/* AC-08: deadline-aware finalization hint while a saved-not-live apply
              is still resolving. This is neutral status, not a success claim. */}
          {pendingApplyID !== undefined && !pollingExpired && pendingDeadline && (
            <p className="text-xs text-jul-muted">
              Finalization expected by {formatLocalTime(pendingDeadline)}.
            </p>
          )}
          {/* AC-14: finalization/audit provenance advisory. Non-blocking and
              independent of the apply outcome above — never a success or failure
              claim, only a heads-up that recovery/audit finalization degraded. */}
          {finalizationAdvisory && (
            <div
              role="status"
              className="rounded-md border border-jul-warning/40 bg-jul-warning/5 p-3 text-sm"
            >
              <p className="font-medium text-jul-warning">{finalizationAdvisory.title}</p>
              {finalizationAdvisory.messages.map((m, i) => (
                <p key={`fin-${String(i)}`} className="mt-1 text-xs text-jul-muted">
                  {m}
                </p>
              ))}
              {finalizationAdvisory.historySnapshotID && (
                <p className="mt-1 text-xs text-jul-muted">
                  History snapshot:{" "}
                  <span className="font-mono text-jul-text">
                    {finalizationAdvisory.historySnapshotID}
                  </span>
                </p>
              )}
            </div>
          )}
          {pollingExpired && applied?.reload?.outcome === "saved_not_live" && (
            <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-sm">
              <p className="font-medium text-jul-warning">Final result still unavailable</p>
              <p className="mt-1 text-xs text-jul-muted">
                The transaction result was not available by its deadline. This is not a success
                confirmation. Check Runtime Overview before making another configuration change.
              </p>
              {pendingApplyID && (
                <p className="mt-1 text-xs text-jul-muted">
                  Apply ID: <span className="font-mono text-jul-text">{pendingApplyID}</span>
                </p>
              )}
              <div className="mt-2 flex flex-wrap gap-2">
                <button
                  type="button"
                  onClick={() => {
                    // Re-check the exact-ID ledger record without inventing a new
                    // operation, so the original result is preserved.
                    managedApply.retry();
                  }}
                  className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
                >
                  Retry status
                </button>
                <button
                  type="button"
                  onClick={() => {
                    void navigate("/");
                  }}
                  className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
                >
                  Open Runtime Overview
                </button>
                {!rawForbidden && appliedState && (
                  <button
                    type="button"
                    onClick={() => {
                      // Reload the editor from the authoritative on-disk config
                      // without clearing the original apply result.
                      refreshEditorAfterFailure(appliedState.operationID);
                    }}
                    className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
                  >
                    Reload authoritative configuration
                  </button>
                )}
              </div>
            </div>
          )}
          {patchReconcileError && appliedState?.wasPatch && (
            <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-sm">
              <p className="font-medium text-jul-danger">Applied, but editor refresh failed</p>
              <p className="mt-1 text-xs text-jul-muted">
                The patch result is authoritative, but the editor is blocked until it reloads the
                persisted configuration. {patchReconcileError.message}
              </p>
              <button
                type="button"
                onClick={() => {
                  reconcilePatchEditor(appliedState.operationID, appliedState.result.version);
                }}
                className="mt-2 rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
              >
                Retry editor refresh
              </button>
            </div>
          )}

          {/* Inline offer to stage the change after a restart-required rejection. */}
          {restartError && restartError.canStage && (
            <div className="rounded-md border border-jul-warning/40 bg-jul-warning/5 p-3 text-sm">
              <p className="font-medium text-jul-warning">Save for next restart?</p>
              {restartError.subsystems.length > 0 && (
                <p className="mt-1 text-xs text-jul-muted">
                  Affected: <span className="font-mono">{restartError.subsystems.join(", ")}</span>
                </p>
              )}
              <p className="mt-1 text-xs text-jul-muted">
                This change is valid but cannot be applied live. Save it now and it will take effect
                on the next process restart; the running server will not change.
              </p>
              <button
                type="button"
                onClick={() => {
                  setStageConfirming(true);
                }}
                disabled={applyStage.isPending}
                className="mt-2 inline-flex items-center gap-1.5 rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg disabled:opacity-40"
              >
                {applyStage.isPending && <Spinner />}
                {applyStage.isPending ? "Saving…" : "Save for next restart"}
              </button>
              {applyStage.isError && (
                <p className="mt-1 text-xs text-jul-danger">
                  {applyStage.error instanceof Error ? applyStage.error.message : "Stage failed."}
                </p>
              )}
            </div>
          )}

          {applyError &&
            !adminChangeError &&
            !restartError &&
            (!(applyError instanceof ConfigApplyOutcomeError) || outcome === null) && (
              <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-sm">
                <p className="font-medium text-jul-danger">
                  {applyError instanceof ConfigRejectedError
                    ? applyError.message
                    : applyError instanceof ConfigConflictError
                      ? "Conflict — another change was applied while you were editing."
                      : applyError instanceof ConfigApplyOutcomeError
                        ? applyError.message
                        : "Apply failed."}
                </p>
                {applyError instanceof ConfigRejectedError &&
                  applyError.issues.map((iss, i) => (
                    <p key={`ae-${String(i)}`} className="mt-1 text-xs text-jul-muted">
                      {iss.path ? `${iss.path}: ` : ""}
                      {iss.summary}
                      {iss.detail ? ` — ${iss.detail}` : ""}
                    </p>
                  ))}
                {applyError instanceof ConfigConflictError && (
                  <div className="mt-2 flex gap-2">
                    <button
                      type="button"
                      onClick={() => {
                        if (isPatchMode) {
                          // A structured patch may be retried only after the
                          // originating editor produces a new preview. Never
                          // substitute the current version into the old ops.
                          cancelOperation();
                          setPatchDraft(null);
                          setConflictVersion(undefined);
                          applyPatch.reset();
                          applyStage.reset();
                          void navigate(-1);
                          return;
                        }

                        // Raw editor flow: discard the stale text and re-seed
                        // from the latest persisted config and version.
                        const operationID = startOperation();
                        setConflictVersion(undefined);
                        applyRaw.reset();
                        void qc
                          .fetchQuery({ queryKey: ["raw-config"], queryFn: fetchRawConfig })
                          .then((fresh) => {
                            if (operationIDRef.current !== operationID) return;
                            setBaseline(fresh.raw ?? "");
                            setDraft(fresh.raw ?? "");
                            setBaseVersion(fresh.base_version);
                          });
                      }}
                      className="rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-text hover:bg-jul-bg"
                    >
                      {isPatchMode ? "Discard stale patch and preview again" : "Reload latest config"}
                    </button>
                  </div>
                )}
              </div>
            )}

          {!valid && issues.length > 0 && (
            <div className="space-y-2 rounded-md border border-jul-danger/40 bg-jul-danger/5 p-3">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-jul-danger">
                Validation errors
              </h3>
              {issues.map((iss, i) => (
                <div key={`iss-${String(i)}`} className="text-xs">
                  <p className="text-jul-text">
                    {iss.path && (
                      <code className="mr-1.5 rounded bg-jul-danger/10 px-1 py-0.5 font-mono text-[0.7rem] text-jul-danger">
                        {iss.path}
                      </code>
                    )}
                    {iss.summary}
                  </p>
                  {iss.detail && <p className="text-jul-muted">{iss.detail}</p>}
                </div>
              ))}
            </div>
          )}

          {!valid && issues.length === 0 && validation.data && (
            <div className="rounded-md border border-jul-danger/40 bg-jul-danger/5 p-3 text-xs text-jul-danger">
              {validation.data.message ?? "The draft configuration is invalid."}
            </div>
          )}

          {dirty && valid && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Pending changes
              </h3>
              {!isPatchMode && rawDiff.isFetching && (
                <p className="text-xs text-jul-muted">Computing diff…</p>
              )}
              {previewDiff && <DiffView diff={previewDiff} />}
              {!isPatchMode && rawDiff.isError && (
                <p className="text-xs text-jul-danger">Unable to compute diff.</p>
              )}
            </div>
          )}

          {!dirty && !outcome && (
            <p className="text-xs text-jul-muted">
              Edit the configuration to preview a validated diff before applying.
            </p>
          )}
        </div>
      </div>

      {confirming && (
        <ConfirmDialog
          title={
            adminChangeError
              ? "Confirm admin access change?"
              : updatingStagedPatch
                ? "Update staged configuration?"
                : isPatchMode
                  ? "Apply atomic patch?"
                  : "Apply configuration?"
          }
          confirmLabel={
            adminChangeError
              ? "Apply and change admin access"
              : updatingStagedPatch
                ? "Update staged config"
                : "Apply now"
          }
          busy={applyActive.isPending}
          onConfirm={() => {
            const operationID = operationIDRef.current;
            if (adminChangeError) {
              confirmAdminForOperation(operationID);
              if (isPatchMode) applyPatch.mutate({ confirmAdmin: true, operationID });
              else applyRaw.mutate({ confirmAdmin: true, operationID });
            } else if (isPatchMode) {
              applyPatch.mutate({ confirmAdmin: false, operationID });
            } else {
              applyRaw.mutate({ confirmAdmin: false, operationID });
            }
          }}
          onCancel={() => {
            cancelOperation();
            setConfirming(false);
            applyRaw.reset();
            applyPatch.reset();
          }}
        >
          {adminChangeError ? (
            <>
              <p>
                This edit changes how you reach the admin console. Review the effect before
                continuing — you may need to re-authenticate or use a new address, and an incorrect
                change can lock you out of the console. Nothing has been saved yet.
              </p>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-jul-text">
                {adminChangeError.changes.map((c, i) => (
                  <li key={`adm-${String(i)}`}>{c}</li>
                ))}
              </ul>
            </>
          ) : updatingStagedPatch ? (
            <>
              <p>
                A configuration is staged for the next process restart. This structured patch
                replaces that staged configuration; it is validated and persisted atomically, but
                the running runtime is not modified and will keep serving the current configuration
                until the process is restarted. If another operator changed the staged config since
                this edit was prepared, the update will be rejected so no change is lost.
              </p>
            </>
          ) : isPatchMode ? (
            <>
              <p>
                This applies the structured edit atomically server-side. The config is validated and
                persisted; if another operator changed config since this edit was prepared, the
                apply will be rejected so no change is lost.
              </p>
            </>
          ) : (
            <>
              <p>
                This validates the new configuration, writes it, and triggers a live reload of the
                proxy. The draft is fully preflighted before it is saved, so a config that is
                accepted here is guaranteed to build; the reload that swaps it into the live runtime
                happens moments later. The current configuration is snapshotted first, so you can
                roll back from the History panel.
              </p>
            </>
          )}
          {previewDiff && <p className="mt-2 text-jul-text">{previewDiff.summary}</p>}
        </ConfirmDialog>
      )}

      {/* Stage-restart confirm dialog */}
      {stageConfirming && (
        <ConfirmDialog
          title={
            hasPendingRestart
              ? "Update staged configuration?"
              : isPatchMode
                ? "Save structured patch for next restart?"
                : "Save for next restart?"
          }
          confirmLabel={hasPendingRestart ? "Update staged config" : "Save for next restart"}
          busy={applyStage.isPending}
          confirmDisabled={applyStage.error instanceof ConfigConflictError}
          onConfirm={() => {
            const operationID = operationIDRef.current;
            const confirmAdmin =
              applyStage.error instanceof ConfigAdminChangeError ||
              confirmedAdminOperationRef.current === operationID;
            if (confirmAdmin) confirmAdminForOperation(operationID);
            applyStage.mutate({ confirmAdmin, operationID });
          }}
          onCancel={() => {
            cancelOperation();
            setStageConfirming(false);
            applyStage.reset();
          }}
        >
          {applyStage.error instanceof ConfigAdminChangeError ? (
            <>
              <p>
                This staged change affects admin access. Confirm the same staged operation to
                proceed.
              </p>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-jul-text">
                {applyStage.error.changes.map((change) => (
                  <li key={change}>{change}</li>
                ))}
              </ul>
            </>
          ) : hasPendingRestart ? (
            <p>
              The staged configuration will be replaced with this draft. The running server will
              remain unchanged until the process is restarted.
            </p>
          ) : isPatchMode ? (
            <>
              <p>
                The authoritative preview marked this exact ordered patch as restart-bound. It will
                be validated and saved for the next process restart; no hot-apply attempt is made and
                the running server remains unchanged.
              </p>
              {patchDraft.lifecycle?.pending_subsystems &&
                patchDraft.lifecycle.pending_subsystems.length > 0 && (
                  <p className="mt-2 text-xs text-jul-muted">
                    Pending subsystems:{" "}
                    <span className="font-mono">
                      {patchDraft.lifecycle.pending_subsystems.join(", ")}
                    </span>
                  </p>
                )}
            </>
          ) : (
            <>
              <p>
                The configuration will be validated and saved. The running server will not change
                until the process is restarted. You can discard this staged change at any time from
                the Overview panel.
              </p>
              {restartError?.subsystems && restartError.subsystems.length > 0 && (
                <p className="mt-2 text-xs text-jul-muted">
                  Affected subsystems:{" "}
                  <span className="font-mono">{restartError.subsystems.join(", ")}</span>
                </p>
              )}
            </>
          )}
          {previewDiff && <p className="mt-2 text-jul-text">{previewDiff.summary}</p>}
          {applyStage.error && !(applyStage.error instanceof ConfigAdminChangeError) && (
            <p className="mt-2 text-xs text-jul-danger">
              {applyStage.error instanceof ConfigConflictError
                ? "The base configuration changed. Cancel this dialog and generate a fresh preview; the stale patch cannot be forced."
                : applyStage.error instanceof Error
                  ? applyStage.error.message
                  : "Stage failed."}
            </p>
          )}
        </ConfirmDialog>
      )}
    </div>
  );
}

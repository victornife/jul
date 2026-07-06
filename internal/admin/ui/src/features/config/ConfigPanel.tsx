/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { Suspense, lazy, useEffect, useState, useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  applyConfig,
  applyPatchBatch,
  diffConfig,
  fetchRawConfig,
  validateConfig,
  ConfigRejectedError,
  ConfigConflictError,
  ConfigRestartRequiredError,
  ConfigAdminChangeError,
  type FeatureStatus,
  type ConfigDiff,
} from "@/api/client.ts";
import type { PendingDraft } from "@/lib/configDraftHandoff.ts";
import { useDebouncedValue } from "@/lib/useDebouncedValue.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading, Spinner } from "@/components/ui.tsx";
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

function AppliedSummary({ status }: { readonly status: FeatureStatus[] }) {
  const active = status.filter((s) => s.active).length;
  return (
    <div className="rounded-md border border-jul-success/40 bg-jul-success/10 p-3 text-sm text-jul-text">
      <p className="font-medium text-jul-success">Configuration validated and saved.</p>
      <p className="text-xs text-jul-muted">
        The live runtime is reloading to apply it. {active} of {status.length} capabilities are
        active in the saved configuration.
      </p>
    </div>
  );
}

export function ConfigPanel() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["raw-config"],
    queryFn: fetchRawConfig,
  });

  // Raw editor state
  const [draft, setDraft] = useState<string | null>(null);
  const [baseline, setBaseline] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [applied, setApplied] = useState<FeatureStatus[] | null>(null);

  // Patch draft state: when a structured patch is handed off, the editor shows
  // the candidate read-only and the diff is pre-computed; applying uses the
  // atomic patch endpoint rather than raw apply.
  const [patchDraft, setPatchDraft] = useState<PendingDraft & { kind: "patch" } | null>(null);
  const [conflictVersion, setConflictVersion] = useState<string | undefined>();
  // baseVersion is the optimistic-concurrency token for the raw editor: the
  // version the loaded config was read at. It is sent on raw apply so a stale
  // edit is rejected with 409 instead of clobbering a concurrent change.
  const [baseVersion, setBaseVersion] = useState<string | undefined>();

  // Seed the editor once the raw config arrives. A pending handoff, if present,
  // becomes the draft so the operator lands on a ready-to-review diff.
  useEffect(() => {
    if (data && draft === null && patchDraft === null) {
      const raw = data.raw ?? "";
      setBaseVersion(data.base_version);
      const handoff = takePendingDraft();
      if (handoff) {
        if (handoff.kind === "toml") {
          setBaseline(raw);
          setDraft(handoff.toml);
        } else {
          setPatchDraft(handoff);
          setBaseline(raw);
          // Seed the editor with the candidate so the operator can review the
          // full context while knowing the apply will be atomic.
          setDraft(handoff.candidate ?? raw);
        }
      } else {
        setBaseline(raw);
        setDraft(raw);
      }
    }
  }, [data, draft, patchDraft]);

  const current = draft ?? "";
  const isPatchMode = patchDraft !== null;
  const dirty = isPatchMode || (draft !== null && draft !== baseline);
  const debounced = useDebouncedValue(isPatchMode ? "" : current, 400);

  const validation = useQuery({
    queryKey: ["config-validate", debounced],
    queryFn: () => validateConfig(debounced),
    enabled: !isPatchMode && draft !== null && debounced.length > 0,
    staleTime: Infinity,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const valid = isPatchMode || validation.data?.ok === true;

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

  const applyRaw = useMutation({
    mutationFn: (confirmAdmin: boolean) => applyConfig(current, baseVersion, confirmAdmin),
    onSuccess: (res) => {
      setBaseline(current);
      setApplied(res.status);
      setConfirming(false);
      // Advance the token to the freshly-applied version so a follow-up edit
      // does not trip a spurious conflict.
      setBaseVersion(res.version ?? undefined);
      setConflictVersion(undefined);
      void qc.invalidateQueries();
    },
    onError: (err) => {
      if (err instanceof ConfigConflictError) {
        setConflictVersion(err.currentVersion);
      }
    },
  });

  const applyPatch = useMutation({
    mutationFn: () => {
      if (!patchDraft) throw new Error("no patch draft to apply");
      return applyPatchBatch(patchDraft.ops, patchDraft.baseVersion ?? conflictVersion);
    },
    onSuccess: (res) => {
      // Reconcile the raw-editor state with the freshly-applied patch: the
      // candidate is now the persisted config and res.version is its
      // fingerprint. Without this, exiting patch mode leaves the editor looking
      // dirty (draft still the candidate, baseline still the old config) and a
      // follow-up raw apply trips a spurious 409 on the stale baseVersion.
      const candidate = patchDraft?.candidate ?? current;
      setPatchDraft(null);
      setBaseline(candidate);
      setDraft(candidate);
      setBaseVersion(res.version ?? undefined);
      setApplied(res.status ?? []);
      setConfirming(false);
      setConflictVersion(undefined);
      void qc.invalidateQueries();
    },
    onError: (err) => {
      if (err instanceof ConfigConflictError) {
        setConflictVersion(err.currentVersion);
      }
    },
  });

  const applyActive = isPatchMode ? applyPatch : applyRaw;
  const applyError = applyActive.error;
  // A raw apply that would change how the operator reaches the admin console is
  // rejected with a 409 the first time; the same confirm modal then re-applies
  // with confirm_admin=true. Derived from the error so no extra state is needed.
  const adminChangeError = applyError instanceof ConfigAdminChangeError ? applyError : null;

  if (isLoading) return <Loading label="Loading configuration…" />;
  if (isError || !data)
    return <PanelError error={error} resource="the configuration" onRetry={() => void refetch()} />;

  if (data.raw === undefined && draft === null) {
    return (
      <div className="space-y-2">
        <h1 className="text-xl font-semibold">Configuration</h1>
        <p className="text-sm text-jul-muted">Raw config not available (read hook not wired).</p>
      </div>
    );
  }

  const pill: "idle" | "checking" | "valid" | "invalid" = isPatchMode
    ? "valid"
    : validation.isFetching
      ? "checking"
      : validation.data === undefined
        ? "idle"
        : valid
          ? "valid"
          : "invalid";
  const issues = validation.data?.errors ?? [];

  return (
    <div className="flex h-full flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">Configuration</h1>
          <p className="max-w-3xl text-sm text-jul-muted">
            Live TOML editor with validation, diff previews, and atomic patch support.
            Review every change before it is applied to make sure the configuration remains sound.
          </p>
        </div>
        {data.path && <span className="font-mono text-xs text-jul-muted">{data.path}</span>}
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
              setDraft(baseline);
              setPatchDraft(null);
              setApplied(null);
              setConflictVersion(undefined);
              applyRaw.reset();
              applyPatch.reset();
            }}
            disabled={!dirty || applyActive.isPending}
            className="rounded-md border border-jul-border px-3 py-1 text-sm text-jul-muted hover:text-jul-text disabled:opacity-40"
          >
            Reset
          </button>
          <button
            type="button"
            onClick={() => {
              setConfirming(true);
            }}
            disabled={!dirty || !valid || applyActive.isPending}
            className="inline-flex items-center gap-2 rounded-md bg-jul-accent px-3 py-1 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {applyActive.isPending && <Spinner />}
            {applyActive.isPending
              ? "Applying…"
              : isPatchMode
                ? "Apply patch"
                : "Apply changes"}
          </button>
        </div>
      </div>

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[3fr_2fr]">
        <div className="min-h-0 overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
          <Suspense fallback={<EditorFallback />}>
            {draft !== null && (
              <CodeEditor
                value={draft}
                readOnly={isPatchMode}
                onChange={(next) => {
                  setDraft(next);
                  if (applied) setApplied(null);
                }}
              />
            )}
          </Suspense>
        </div>

        <div className="min-h-0 space-y-4 overflow-auto">
          {applied && <AppliedSummary status={applied} />}

          {applyError && !adminChangeError && (
            <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-sm">
              <p className="font-medium text-jul-danger">
                {applyError instanceof ConfigRejectedError
                  ? applyError.message
                  : applyError instanceof ConfigRestartRequiredError
                    ? applyError.message
                    : applyError instanceof ConfigConflictError
                      ? "Conflict — another change was applied while you were editing."
                      : "Apply failed."}
              </p>
              {applyError instanceof ConfigRestartRequiredError && (
                <p className="mt-1 text-xs text-jul-muted">
                  Nothing was saved. Update the configuration file and restart the
                  server for this change to take effect.
                </p>
              )}
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
                      // Discard the stale draft and re-seed from the latest
                      // persisted config so the editor text and the base_version
                      // token both reflect the concurrent change.
                      setPatchDraft(null);
                      setApplied(null);
                      setConflictVersion(undefined);
                      applyRaw.reset();
                      applyPatch.reset();
                      void qc
                        .fetchQuery({ queryKey: ["raw-config"], queryFn: fetchRawConfig })
                        .then((fresh) => {
                          setBaseline(fresh.raw ?? "");
                          setDraft(fresh.raw ?? "");
                          setBaseVersion(fresh.base_version);
                        });
                    }}
                    className="rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-text hover:bg-jul-bg"
                  >
                    Reload latest config
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

          {!dirty && !applied && (
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
              : isPatchMode
                ? "Apply atomic patch?"
                : "Apply configuration?"
          }
          confirmLabel={adminChangeError ? "Apply and change admin access" : "Apply now"}
          busy={applyActive.isPending}
          onConfirm={() => {
            if (adminChangeError) {
              applyRaw.mutate(true);
            } else if (isPatchMode) {
              applyPatch.mutate();
            } else {
              applyRaw.mutate(false);
            }
          }}
          onCancel={() => {
            setConfirming(false);
            applyRaw.reset();
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
          ) : isPatchMode ? (
            <>
              <p>
                This applies the structured edit atomically server-side. The config is validated
                and persisted; if another operator changed config since this edit was prepared,
                the apply will be rejected so no change is lost.
              </p>
            </>
          ) : (
            <>
              <p>
                This validates the new configuration, writes it, and triggers a live reload of the
                proxy. The draft is fully preflighted before it is saved, so a config that is
                accepted here is guaranteed to build; the reload that swaps it into the live
                runtime happens moments later. The current configuration is snapshotted first, so
                you can roll back from the History panel.
              </p>
            </>
          )}
          {previewDiff && <p className="mt-2 text-jul-text">{previewDiff.summary}</p>}
        </ConfirmDialog>
      )}
    </div>
  );
}

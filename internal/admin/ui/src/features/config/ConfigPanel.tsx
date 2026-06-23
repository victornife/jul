import { Suspense, lazy, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  applyConfig,
  diffConfig,
  fetchRawConfig,
  validateConfig,
  ConfigRejectedError,
  type FeatureStatus,
} from "@/api/client.ts";
import { useDebouncedValue } from "@/lib/useDebouncedValue.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
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

function ValidationPill({
  state,
}: {
  readonly state: "idle" | "checking" | "valid" | "invalid";
}) {
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
      <p className="font-medium text-jul-success">Configuration applied.</p>
      <p className="text-xs text-jul-muted">
        {active} of {status.length} capabilities active in the new runtime.
      </p>
    </div>
  );
}

export function ConfigPanel() {
  const qc = useQueryClient();
  const { data, isLoading, isError } = useQuery({
    queryKey: ["raw-config"],
    queryFn: fetchRawConfig,
  });

  const [draft, setDraft] = useState<string | null>(null);
  const [baseline, setBaseline] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [applied, setApplied] = useState<FeatureStatus[] | null>(null);

  // Seed the editor once the raw config arrives. A pending wizard handoff, if
  // present, becomes the draft so the operator lands on a ready-to-review diff
  // against the running config.
  useEffect(() => {
    if (data && draft === null) {
      const raw = data.raw ?? "";
      const handoff = takePendingDraft();
      setBaseline(raw);
      setDraft(handoff ?? raw);
    }
  }, [data, draft]);

  const current = draft ?? "";
  const dirty = draft !== null && draft !== baseline;
  const debounced = useDebouncedValue(current, 400);

  const validation = useQuery({
    queryKey: ["config-validate", debounced],
    queryFn: () => validateConfig(debounced),
    enabled: draft !== null && debounced.length > 0,
    staleTime: Infinity,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const valid = validation.data?.ok === true;

  const diff = useQuery({
    queryKey: ["config-diff", debounced],
    queryFn: () => diffConfig(debounced),
    enabled: dirty && valid,
    staleTime: Infinity,
    refetchInterval: false,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const apply = useMutation({
    mutationFn: () => applyConfig(current),
    onSuccess: (res) => {
      setBaseline(current);
      setApplied(res.status);
      setConfirming(false);
      void qc.invalidateQueries();
    },
  });

  if (isLoading) return <div className="text-jul-muted">Loading configuration…</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load configuration.</div>;

  if (data.raw === undefined && draft === null) {
    return (
      <div className="space-y-2">
        <h1 className="text-xl font-semibold">Configuration</h1>
        <p className="text-sm text-jul-muted">
          Raw config not available (read hook not wired).
        </p>
      </div>
    );
  }

  const pill: "idle" | "checking" | "valid" | "invalid" = validation.isFetching
    ? "checking"
    : validation.data === undefined
      ? "idle"
      : valid
        ? "valid"
        : "invalid";
  const issues = validation.data?.errors ?? [];
  const applyError = apply.error;

  return (
    <div className="flex h-full flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Configuration</h1>
        {data.path && <span className="font-mono text-xs text-jul-muted">{data.path}</span>}
        <ValidationPill state={pill} />
        {dirty && <span className="text-xs text-jul-warning">● unsaved changes</span>}
        <div className="ml-auto flex gap-2">
          <button
            type="button"
            onClick={() => {
              setDraft(baseline);
              setApplied(null);
              apply.reset();
            }}
            disabled={!dirty || apply.isPending}
            className="rounded-md border border-jul-border px-3 py-1 text-sm text-jul-muted hover:text-jul-text disabled:opacity-40"
          >
            Reset
          </button>
          <button
            type="button"
            onClick={() => {
              setConfirming(true);
            }}
            disabled={!dirty || !valid || apply.isPending}
            className="rounded-md bg-jul-accent px-3 py-1 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            Apply changes
          </button>
        </div>
      </div>

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[3fr_2fr]">
        <div className="min-h-0 overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
          <Suspense fallback={<EditorFallback />}>
            {draft !== null && (
              <CodeEditor
                value={draft}
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

          {applyError && (
            <div className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-sm">
              <p className="font-medium text-jul-danger">
                {applyError instanceof ConfigRejectedError
                  ? applyError.message
                  : "Apply failed."}
              </p>
              {applyError instanceof ConfigRejectedError &&
                applyError.issues.map((iss, i) => (
                  <p key={`ae-${String(i)}`} className="mt-1 text-xs text-jul-muted">
                    {iss.summary}
                    {iss.detail ? ` — ${iss.detail}` : ""}
                  </p>
                ))}
            </div>
          )}

          {!valid && issues.length > 0 && (
            <div className="space-y-2 rounded-md border border-jul-danger/40 bg-jul-danger/5 p-3">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-jul-danger">
                Validation errors
              </h3>
              {issues.map((iss, i) => (
                <div key={`iss-${String(i)}`} className="text-xs">
                  <p className="text-jul-text">{iss.summary}</p>
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
              {diff.isFetching && <p className="text-xs text-jul-muted">Computing diff…</p>}
              {diff.data && <DiffView diff={diff.data} />}
              {diff.isError && (
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
          title="Apply configuration?"
          confirmLabel="Apply now"
          busy={apply.isPending}
          onConfirm={() => {
            apply.mutate();
          }}
          onCancel={() => {
            setConfirming(false);
          }}
        >
          <p>
            This writes the new configuration and triggers a live reload of the proxy.
            The current configuration is snapshotted first, so you can roll back from
            the History panel.
          </p>
          {diff.data && <p className="mt-2 text-jul-text">{diff.data.summary}</p>}
        </ConfirmDialog>
      )}
    </div>
  );
}

/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useMemo, useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import {
  fetchRoutes,
  fetchStats,
  type ConfigPatch,
  type QuotaPolicyProjection,
  type RouteProjection,
  type RouteTarget,
  type TrafficControls,
} from "@/api/client.ts";
import { useQuery } from "@tanstack/react-query";
import { useRunPatchBatch, describePatchBatchError } from "@/lib/useRunPatchBatch.ts";
import { NumberField, TextField } from "./TrafficFormFields.tsx";

interface QuotaLimitDraft {
  rate: number;
  burst: number;
}

interface QuotaClassDraft {
  name: string;
  shared: QuotaLimitDraft;
  perCaller: QuotaLimitDraft;
  principalCount: number;
}

interface QuotaOperationDraft {
  name: string;
  shared: QuotaLimitDraft;
  perCaller: QuotaLimitDraft;
}

interface QuotaPolicyDraft {
  name: string;
  mode: "enforce" | "observe";
  rate: number;
  burst: number;
  maxCallers: number;
  classes: QuotaClassDraft[];
  operations: QuotaOperationDraft[];
}

interface RouteChoice {
  key: string;
  label: string;
  target: RouteTarget;
  quotaPolicy: string;
  quotaOperation: string;
}

function seedPolicy(policy?: QuotaPolicyProjection): QuotaPolicyDraft {
  if (policy === undefined) {
    return {
      name: "",
      mode: "enforce",
      rate: 100,
      burst: 100,
      maxCallers: 1024,
      classes: [
        {
          name: "public",
          shared: { rate: 100, burst: 100 },
          perCaller: { rate: 100, burst: 100 },
          principalCount: 0,
        },
      ],
      operations: [],
    };
  }
  return {
    name: policy.name,
    mode: policy.mode,
    rate: policy.rate,
    burst: policy.burst,
    maxCallers: policy.max_callers,
    classes: policy.classes.map((item) => ({
      name: item.name,
      shared: { ...item.shared },
      perCaller: { ...item.per_caller },
      principalCount: item.principal_count,
    })),
    operations: (policy.operations ?? []).map((item) => ({
      name: item.name,
      shared: { ...item.shared },
      perCaller: { ...item.per_caller },
    })),
  };
}

function routeChoices(routes: readonly RouteProjection[]): RouteChoice[] {
  const choices: RouteChoice[] = [];
  for (const route of routes) {
    for (const location of route.locations) {
      const target: RouteTarget = {
        listen: route.listen,
        server_names: route.server_names ?? [],
        match_type: location.type,
        path: location.match,
        ...(location.match_ordinal !== undefined ? { match_ordinal: location.match_ordinal } : {}),
        ...(location.selector_version !== undefined
          ? { selector_version: location.selector_version }
          : {}),
      };
      const key = JSON.stringify([
        target.listen,
        target.server_names,
        target.match_type,
        target.path,
        target.match_ordinal,
      ]);
      const hosts = target.server_names.length > 0 ? target.server_names.join(", ") : "(any host)";
      choices.push({
        key,
        label: `${route.listen} — ${hosts} — ${location.type} ${location.match}`,
        target,
        quotaPolicy: location.quota_policy ?? "",
        quotaOperation: location.quota_operation ?? "",
      });
    }
  }
  return choices;
}

function policyPatch(draft: QuotaPolicyDraft, previous?: QuotaPolicyProjection): ConfigPatch {
  const classes = Object.fromEntries(
    draft.classes.map((item) => [
      item.name.trim(),
      {
        shared: item.shared,
        per_caller: item.perCaller,
      },
    ]),
  );
  const operations = Object.fromEntries(
    draft.operations.map((item) => [
      item.name.trim(),
      {
        shared: item.shared,
        per_caller: item.perCaller,
      },
    ]),
  );
  const classesBefore = new Set((previous?.classes ?? []).map((item) => item.name));
  const operationsBefore = new Set((previous?.operations ?? []).map((item) => item.name));
  const classesAfter = new Set(Object.keys(classes));
  const operationsAfter = new Set(Object.keys(operations));
  return {
    op: "quota_policy_set",
    quota_policy: {
      name: draft.name.trim(),
      mode: draft.mode,
      rate: draft.rate,
      burst: draft.burst,
      max_callers: draft.maxCallers,
      classes,
      remove_classes: [...classesBefore].filter((name) => !classesAfter.has(name)),
      operations,
      remove_operations: [...operationsBefore].filter((name) => !operationsAfter.has(name)),
    },
  };
}

function positiveDraft(draft: QuotaPolicyDraft): boolean {
  const classNames = draft.classes.map((item) => item.name.trim());
  const operationNames = draft.operations.map((item) => item.name.trim());
  const uniqueNames = (names: readonly string[]) => new Set(names).size === names.length;
  return (
    draft.name.trim().length > 0 &&
    draft.rate > 0 &&
    draft.burst > 0 &&
    draft.maxCallers > 0 &&
    uniqueNames(classNames) &&
    uniqueNames(operationNames) &&
    draft.classes.some(
      (item) =>
        item.name.trim() === "public" &&
        item.shared.rate > 0 &&
        item.shared.burst > 0 &&
        item.perCaller.rate > 0 &&
        item.perCaller.burst > 0,
    ) &&
    draft.classes.every(
      (item) =>
        item.name.trim().length > 0 &&
        item.shared.rate > 0 &&
        item.shared.burst > 0 &&
        item.perCaller.rate > 0 &&
        item.perCaller.burst > 0,
    ) &&
    draft.operations.every(
      (item) =>
        item.name.trim().length > 0 &&
        item.shared.rate > 0 &&
        item.shared.burst > 0 &&
        item.perCaller.rate > 0 &&
        item.perCaller.burst > 0,
    )
  );
}

export function QuotaPolicyEditor({
  current,
  onClose,
}: {
  readonly current: TrafficControls;
  readonly onClose: () => void;
}) {
  const { has } = usePermission();
  const canWrite = has("config:write");
  const batch = useRunPatchBatch();
  const policyList = current.quota_policies ?? [];
  const initialPolicy = policyList[0];
  const [selectedPolicy, setSelectedPolicy] = useState(initialPolicy?.name ?? "");
  const [baseline, setBaseline] = useState(() => seedPolicy(initialPolicy));
  const [draft, setDraft] = useState(() => seedPolicy(initialPolicy));
  const [routeKey, setRouteKey] = useState("");
  const [routePolicy, setRoutePolicy] = useState("");
  const [routeOperation, setRouteOperation] = useState("");
  const [removing, setRemoving] = useState(false);

  const routes = useQuery({ queryKey: ["routes"], queryFn: fetchRoutes });
  const stats = useQuery({
    queryKey: ["stats"],
    queryFn: fetchStats,
    refetchInterval: 5_000,
  });
  const choices = useMemo(() => routeChoices(routes.data ?? []), [routes.data]);
  const selectedRoute = choices.find((item) => item.key === routeKey);
  const previous = policyList.find((item) => item.name === selectedPolicy);
  const selectedPolicyRoutes = choices.filter((item) => item.quotaPolicy === selectedPolicy);
  const draftDirty = JSON.stringify(draft) !== JSON.stringify(baseline);
  const valid =
    positiveDraft(draft) &&
    (selectedPolicy !== "" ||
      !policyList.some((item) => item.name === draft.name.trim()));
  const attachDirty =
    selectedRoute !== undefined &&
    (selectedRoute.quotaPolicy !== routePolicy || selectedRoute.quotaOperation !== routeOperation);
  const operations: ConfigPatch[] = [];
  if (draftDirty && valid) operations.push(policyPatch(draft, previous));
  if (selectedRoute !== undefined &&
    (selectedRoute.quotaPolicy !== routePolicy || selectedRoute.quotaOperation !== routeOperation)) {
    operations.push({
      op: "route_set_quota_policy",
      ...selectedRoute.target,
      route_quota: { policy: routePolicy, operation: routeOperation },
    });
  }
  const busy = batch.busy;

  function selectPolicy(name: string): void {
    setSelectedPolicy(name);
    const policy = policyList.find((item) => item.name === name);
    const next = seedPolicy(policy);
    setBaseline(next);
    setDraft(next);
    setRemoving(false);
    batch.clearError();
  }

  function selectRoute(key: string): void {
    setRouteKey(key);
    const choice = choices.find((item) => item.key === key);
    setRoutePolicy(choice?.quotaPolicy ?? "");
    setRouteOperation(choice?.quotaOperation ?? "");
    batch.clearError();
  }

  const availableOperations =
    routePolicy === draft.name
      ? draft.operations.map((item) => item.name.trim()).filter(Boolean)
      : (policyList.find((item) => item.name === routePolicy)?.operations ?? []).map(
          (item) => item.name,
        );
  const referencedRoute = selectedPolicyRoutes.length > 0;

  return (
    <Drawer title="Application quotas" subtitle="Review each policy and route change before applying." onClose={onClose}>
      <div className="space-y-5 p-4">
        <p className="text-sm text-jul-muted">
          A policy shares one application ceiling across attached routes and hosts. Class allocations
          are fixed; operation limits tighten a route without reserving extra application capacity.
          Verified-principal mappings take precedence over CIDR pools; pools use the longest matching
          prefix and only apply to attributed client addresses. Principal and CIDR mappings remain
          server-owned configuration and are preserved by these edits. Principal values are never
          projected into the Console.
        </p>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Policy</span>
          <select
            value={selectedPolicy}
            onChange={(event) => {
              selectPolicy(event.target.value);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text"
          >
            <option value="">Create a policy</option>
            {policyList.map((policy) => (
              <option key={policy.name} value={policy.name}>
                {policy.name} ({policy.mode})
              </option>
            ))}
          </select>
        </label>
        {routes.isLoading && <p className="text-xs text-jul-muted">Loading configured routes…</p>}
        {routes.isError && (
          <p role="alert" className="text-xs text-jul-danger">
            Routes could not be loaded. Route attachment and policy removal are unavailable.
          </p>
        )}

        <div className="space-y-3 rounded-md border border-jul-border p-3">
          {selectedPolicy === "" ? (
            <TextField
              label="Policy name"
              value={draft.name}
              onChange={(name) => {
                setDraft((previousDraft) => ({ ...previousDraft, name }));
                batch.clearError();
              }}
            />
          ) : (
            <p className="text-sm text-jul-text">
              Policy name: <span className="font-mono">{draft.name}</span>
            </p>
          )}
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Mode</span>
            <select
              value={draft.mode}
              onChange={(event) => {
                const mode = event.target.value;
                if (mode === "enforce" || mode === "observe") {
                  setDraft((previousDraft) => ({ ...previousDraft, mode }));
                  batch.clearError();
                }
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text"
            >
              <option value="observe">Observe (measure; allow requests)</option>
              <option value="enforce">Enforce (reject over-budget requests)</option>
            </select>
          </label>
          <NumberField
            label="Application requests per second"
            value={draft.rate}
            onChange={(rate) => {
              setDraft((previousDraft) => ({ ...previousDraft, rate }));
              batch.clearError();
            }}
          />
          <NumberField
            label="Application burst"
            value={draft.burst}
            onChange={(burst) => {
              setDraft((previousDraft) => ({ ...previousDraft, burst }));
              batch.clearError();
            }}
          />
          <NumberField
            label="Maximum callers per bucket"
            value={draft.maxCallers}
            onChange={(maxCallers) => {
              setDraft((previousDraft) => ({ ...previousDraft, maxCallers }));
              batch.clearError();
            }}
          />
        </div>

        <section className="space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-jul-text">Class allocations</h2>
            <button
              type="button"
              onClick={() => {
                setDraft((value) => ({
                  ...value,
                  classes: [
                    ...value.classes,
                    {
                      name: "",
                      shared: { rate: 10, burst: 10 },
                      perCaller: { rate: 2, burst: 2 },
                      principalCount: 0,
                    },
                  ],
                }));
              }}
              className="rounded border border-jul-border px-2 py-1 text-xs text-jul-text"
            >
              Add class
            </button>
          </div>
          {draft.classes.map((item, index) => (
            <div key={`${item.name}-${String(index)}`} className="space-y-3 rounded-md border border-jul-border p-3">
              <div className="flex items-end gap-2">
                <div className="min-w-0 flex-1">
                  <TextField
                    label="Class name"
                    value={item.name}
                    onChange={(name) => {
                      setDraft((value) => ({
                        ...value,
                        classes: value.classes.map((classDraft, classIndex) =>
                          classIndex === index ? { ...classDraft, name } : classDraft,
                        ),
                      }));
                    }}
                  />
                </div>
                {item.name !== "public" && (
                  <button
                    type="button"
                    onClick={() => {
                      setDraft((value) => ({
                        ...value,
                        classes: value.classes.filter((_, classIndex) => classIndex !== index),
                      }));
                    }}
                    className="rounded border border-jul-border px-2 py-1.5 text-xs text-jul-text"
                  >
                    Remove
                  </button>
                )}
              </div>
              {item.principalCount > 0 && (
                <p className="text-xs text-jul-muted">
                  {String(item.principalCount)} verified identity mapping(s) are configured and will be preserved.
                </p>
              )}
              <div className="grid gap-3 sm:grid-cols-2">
                <NumberField
                  label="Shared rate"
                  value={item.shared.rate}
                  onChange={(rate) => {
                    setDraft((value) => ({
                      ...value,
                      classes: value.classes.map((classDraft, classIndex) =>
                        classIndex === index
                          ? { ...classDraft, shared: { ...classDraft.shared, rate } }
                          : classDraft,
                      ),
                    }));
                  }}
                />
                <NumberField
                  label="Shared burst"
                  value={item.shared.burst}
                  onChange={(burst) => {
                    setDraft((value) => ({
                      ...value,
                      classes: value.classes.map((classDraft, classIndex) =>
                        classIndex === index
                          ? { ...classDraft, shared: { ...classDraft.shared, burst } }
                          : classDraft,
                      ),
                    }));
                  }}
                />
                <NumberField
                  label="Per-caller rate"
                  value={item.perCaller.rate}
                  onChange={(rate) => {
                    setDraft((value) => ({
                      ...value,
                      classes: value.classes.map((classDraft, classIndex) =>
                        classIndex === index
                          ? { ...classDraft, perCaller: { ...classDraft.perCaller, rate } }
                          : classDraft,
                      ),
                    }));
                  }}
                />
                <NumberField
                  label="Per-caller burst"
                  value={item.perCaller.burst}
                  onChange={(burst) => {
                    setDraft((value) => ({
                      ...value,
                      classes: value.classes.map((classDraft, classIndex) =>
                        classIndex === index
                          ? { ...classDraft, perCaller: { ...classDraft.perCaller, burst } }
                          : classDraft,
                      ),
                    }));
                  }}
                />
              </div>
            </div>
          ))}
        </section>

        <section className="space-y-3">
          <div className="flex items-center justify-between">
            <h2 className="text-sm font-semibold text-jul-text">Operation limits</h2>
            <button
              type="button"
              onClick={() => {
                setDraft((value) => ({
                  ...value,
                  operations: [
                    ...value.operations,
                    { name: "", shared: { rate: 5, burst: 5 }, perCaller: { rate: 2, burst: 2 } },
                  ],
                }));
              }}
              className="rounded border border-jul-border px-2 py-1 text-xs text-jul-text"
            >
              Add operation
            </button>
          </div>
          {draft.operations.map((item, index) => (
            <div key={`${item.name}-${String(index)}`} className="space-y-3 rounded-md border border-jul-border p-3">
              <div className="flex items-end gap-2">
                <div className="min-w-0 flex-1">
                  <TextField
                    label="Operation name"
                    value={item.name}
                    onChange={(name) => {
                      if (routePolicy === draft.name && routeOperation === item.name.trim()) {
                        setRouteOperation(name.trim());
                      }
                      setDraft((value) => ({
                        ...value,
                        operations: value.operations.map((operation, operationIndex) =>
                          operationIndex === index ? { ...operation, name } : operation,
                        ),
                      }));
                    }}
                  />
                </div>
                <button
                  type="button"
                  onClick={() => {
                    if (routePolicy === draft.name && routeOperation === item.name.trim()) {
                      setRouteOperation("");
                    }
                    setDraft((value) => ({
                      ...value,
                      operations: value.operations.filter((_, operationIndex) => operationIndex !== index),
                    }));
                  }}
                  className="rounded border border-jul-border px-2 py-1.5 text-xs text-jul-text"
                >
                  Remove
                </button>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <NumberField
                  label="Shared operation rate"
                  value={item.shared.rate}
                  onChange={(rate) => {
                    setDraft((value) => ({
                      ...value,
                      operations: value.operations.map((operation, operationIndex) =>
                        operationIndex === index
                          ? { ...operation, shared: { ...operation.shared, rate } }
                          : operation,
                      ),
                    }));
                  }}
                />
                <NumberField
                  label="Shared operation burst"
                  value={item.shared.burst}
                  onChange={(burst) => {
                    setDraft((value) => ({
                      ...value,
                      operations: value.operations.map((operation, operationIndex) =>
                        operationIndex === index
                          ? { ...operation, shared: { ...operation.shared, burst } }
                          : operation,
                      ),
                    }));
                  }}
                />
                <NumberField
                  label="Per-caller operation rate"
                  value={item.perCaller.rate}
                  onChange={(rate) => {
                    setDraft((value) => ({
                      ...value,
                      operations: value.operations.map((operation, operationIndex) =>
                        operationIndex === index
                          ? { ...operation, perCaller: { ...operation.perCaller, rate } }
                          : operation,
                      ),
                    }));
                  }}
                />
                <NumberField
                  label="Per-caller operation burst"
                  value={item.perCaller.burst}
                  onChange={(burst) => {
                    setDraft((value) => ({
                      ...value,
                      operations: value.operations.map((operation, operationIndex) =>
                        operationIndex === index
                          ? { ...operation, perCaller: { ...operation.perCaller, burst } }
                          : operation,
                      ),
                    }));
                  }}
                />
              </div>
            </div>
          ))}
        </section>

        <section className="space-y-3 rounded-md border border-jul-border p-3">
          <h2 className="text-sm font-semibold text-jul-text">Attach a route</h2>
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Route</span>
            <select
              value={routeKey}
              disabled={!routes.isSuccess}
              onChange={(event) => {
                selectRoute(event.target.value);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-xs text-jul-text"
            >
              <option value="">Do not change a route</option>
              {choices.map((choice) => (
                <option key={choice.key} value={choice.key}>
                  {choice.label}
                </option>
              ))}
            </select>
          </label>
          {selectedRoute !== undefined && (
            <>
              <label className="block space-y-1">
                <span className="text-sm font-medium text-jul-text">Application policy</span>
                <select
                  value={routePolicy}
                  onChange={(event) => {
                    setRoutePolicy(event.target.value);
                    setRouteOperation("");
                    batch.clearError();
                  }}
                  className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text"
                >
                  <option value="">No policy (detach)</option>
                  {policyList.map((policy) => (
                    <option key={policy.name} value={policy.name}>
                      {policy.name}
                    </option>
                  ))}
                  {draft.name.trim() !== "" && !policyList.some((item) => item.name === draft.name.trim()) && (
                    <option value={draft.name.trim()}>{draft.name.trim()} (new)</option>
                  )}
                </select>
              </label>
              {routePolicy !== "" && (
                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">Operation (optional)</span>
                  <select
                    value={routeOperation}
                    onChange={(event) => {
                      setRouteOperation(event.target.value);
                      batch.clearError();
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text"
                  >
                    <option value="">No operation ceiling</option>
                    {availableOperations.map((operation) => (
                      <option key={operation} value={operation}>
                        {operation}
                      </option>
                    ))}
                  </select>
                </label>
              )}
            </>
          )}
        </section>

        {stats.data?.quotaDecisions && Object.keys(stats.data.quotaDecisions).length > 0 && (
          <section className="space-y-1 rounded-md border border-jul-border p-3">
            <h2 className="text-sm font-semibold text-jul-text">Quota decisions</h2>
            {Object.entries(stats.data.quotaDecisions).map(([name, decisions]) => (
              <p key={name} className="text-xs text-jul-muted">
                <span className="font-mono">{name}</span>:{" "}
                {Object.entries(decisions)
                  .map(([reason, count]) => `${reason} ${String(count)}`)
                  .join(", ")}
              </p>
            ))}
          </section>
        )}

        {previous !== undefined && (
          <p className="text-xs text-jul-muted">
            Address pools:{" "}
            {(previous.pools ?? [])
              .map((pool) => `${pool.name} → ${pool.class} (${pool.cidrs.join(", ")})`)
              .join("; ") || "none"}.
            Identity and CIDR mappings remain unchanged. Reducing a limit is validated by preview and
            preserves depleted state across reload and rollback.
          </p>
        )}
        {selectedPolicy !== "" && previous !== undefined && (
          <div className="space-y-2">
            <button
              type="button"
              disabled={
                !canWrite || busy || !routes.isSuccess || referencedRoute || draftDirty || attachDirty
              }
              onClick={() => {
                setRemoving(true);
                void batch.run(
                  [{ op: "quota_policy_remove", quota_policy_name: selectedPolicy }],
                  current.base_version,
                );
              }}
              className="rounded border border-jul-danger/50 px-3 py-1.5 text-sm text-jul-danger disabled:opacity-40"
            >
              Remove this policy
            </button>
            {referencedRoute && (
              <p className="text-xs text-jul-muted">
                Detach this policy from its {String(selectedPolicyRoutes.length)} route(s) before removing it.
              </p>
            )}
          </div>
        )}
        {removing && <p className="text-xs text-jul-muted">Policy removal is being previewed.</p>}
        {!valid && (
          <p className="text-xs text-jul-warning">
            Policy/class/operation names must be unique, rates and bursts positive, and a public class
            is required. A new policy name cannot duplicate an existing policy.
          </p>
        )}
        {(batch.error !== null) && (
          <p role="alert" className="text-xs text-jul-danger">
            {describePatchBatchError(batch.error)}
          </p>
        )}

        <div className="flex flex-wrap items-center justify-end gap-3 border-t border-jul-border pt-4">
          <ForbiddenAction permission="config:write" />
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text"
          >
            Cancel
          </button>
          <button
            type="button"
            disabled={!canWrite || busy || operations.length === 0 || !valid}
            onClick={() => {
              void batch.run(operations, current.base_version);
            }}
            className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg disabled:opacity-40"
          >
            {busy ? "Reviewing…" : "Review changes"}
          </button>
        </div>
      </div>
    </Drawer>
  );
}

/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { testRoute, type RouteTestInput, type RouteTestResult } from "@/api/client.ts";

function Field({
  label,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
    </label>
  );
}

function Flag({ on, label }: { readonly on: boolean; readonly label: string }) {
  if (!on) return null;
  return (
    <span className="inline-block rounded-full bg-jul-accent/15 px-2 py-0.5 text-xs text-jul-accent">
      {label}
    </span>
  );
}

function Result({ res }: { readonly res: RouteTestResult }) {
  return (
    <div className="space-y-3">
      <div
        className={`rounded-md border p-3 text-sm ${
          res.matched
            ? "border-jul-success/40 bg-jul-success/10 text-jul-text"
            : "border-jul-warning/40 bg-jul-warning/10 text-jul-text"
        }`}
      >
        <p className="font-medium">{res.matched ? "Route matched" : "No match"}</p>
        <p className="mt-1 text-xs text-jul-muted">{res.explanation}</p>
      </div>

      {res.matched && (
        <div className="rounded-md border border-jul-border bg-jul-surface px-4 py-3 text-sm">
          <div className="grid grid-cols-[120px_1fr] gap-2 py-1">
            <span className="text-xs uppercase tracking-wider text-jul-muted">Server</span>
            <span className="font-mono text-jul-text">{res.listen}</span>
          </div>
          <div className="grid grid-cols-[120px_1fr] gap-2 py-1">
            <span className="text-xs uppercase tracking-wider text-jul-muted">Route</span>
            <span className="font-mono text-jul-text">
              {res.match_type} {res.match}
            </span>
          </div>
          <div className="grid grid-cols-[120px_1fr] gap-2 py-1">
            <span className="text-xs uppercase tracking-wider text-jul-muted">Action</span>
            <span className="text-jul-text">{res.action}</span>
          </div>
          {res.target && (
            <div className="grid grid-cols-[120px_1fr] gap-2 py-1">
              <span className="text-xs uppercase tracking-wider text-jul-muted">Target</span>
              <span className="font-mono text-jul-text">{res.target}</span>
            </div>
          )}
          {res.upstream && (
            <div className="grid grid-cols-[120px_1fr] gap-2 py-1">
              <span className="text-xs uppercase tracking-wider text-jul-muted">Upstream</span>
              <span className="font-mono text-jul-text">{res.upstream}</span>
            </div>
          )}
          <div className="mt-2 flex flex-wrap gap-2">
            <Flag on={res.auth} label="auth" />
            <Flag on={res.cache} label="cache" />
            <Flag on={res.compression} label="compression" />
            <Flag on={res.rate_limit} label="rate limit" />
            <Flag on={res.secure} label="TLS" />
          </div>
        </div>
      )}

      {res.quota_classification && (
        <div className="rounded-md border border-jul-accent/40 bg-jul-accent/5 p-3 text-sm">
          <p className="font-medium text-jul-text">Application quota classification</p>
          <div className="mt-2 grid grid-cols-[120px_1fr] gap-2">
            <span className="text-xs uppercase tracking-wider text-jul-muted">Policy</span>
            <span className="font-mono text-jul-text">
              {res.quota_classification.policy}
              {res.quota_classification.operation
                ? ` · ${res.quota_classification.operation}`
                : ""}
            </span>
            <span className="text-xs uppercase tracking-wider text-jul-muted">Class</span>
            <span className="text-jul-text">{res.quota_classification.class}</span>
            <span className="text-xs uppercase tracking-wider text-jul-muted">Winning rule</span>
            <span className="text-jul-text">{res.quota_classification.winning_rule}</span>
            <span className="text-xs uppercase tracking-wider text-jul-muted">Precedence</span>
            <span className="text-xs text-jul-text">{res.quota_classification.precedence}</span>
          </div>
          <p className="mt-2 text-xs text-jul-muted">{res.quota_classification.note}</p>
          {res.quota_classification.warnings?.map((warning) => (
            <p key={warning} className="mt-1 text-xs text-jul-warning">
              {warning}
            </p>
          ))}
        </div>
      )}

      {res.warnings && res.warnings.length > 0 && (
        <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
          {res.warnings.map((wn, i) => (
            <p key={`tw-${String(i)}`} className="text-xs text-jul-text">
              {wn}
            </p>
          ))}
        </div>
      )}
    </div>
  );
}

export interface RouteTesterProps {
  readonly onClose: () => void;
}

/** Route testing drawer (Milestone 2.3): a dry-run matcher that shows how Jul
 * would resolve a request before sending real traffic. */
export function RouteTester({ onClose }: RouteTesterProps) {
  const [method, setMethod] = useState("GET");
  const [path, setPath] = useState("/");
  const [host, setHost] = useState("");
  const [clientAddress, setClientAddress] = useState("");
  const [principalMethod, setPrincipalMethod] = useState("");
  const [principalDomain, setPrincipalDomain] = useState("");
  const [principalIssuer, setPrincipalIssuer] = useState("");
  const [principalSubject, setPrincipalSubject] = useState("");
  const [simulationError, setSimulationError] = useState<string | null>(null);

  const run = useMutation({
    mutationFn: (input: RouteTestInput) => testRoute(input),
  });

  function onRun(): void {
    const principalFields = [principalMethod, principalDomain, principalIssuer, principalSubject];
    const hasPrincipalInput = principalFields.some((value) => value.trim() !== "");
    if (
      hasPrincipalInput &&
      (!principalMethod.trim() || !principalDomain.trim() || !principalSubject.trim())
    ) {
      setSimulationError("Provide the principal method, domain, and subject together.");
      run.reset();
      return;
    }
    setSimulationError(null);
    const input: RouteTestInput = { method, path: path.trim() || "/" };
    if (host.trim()) input.host = host.trim();
    if (clientAddress.trim()) input.client_address = clientAddress.trim();
    if (principalMethod.trim() && principalDomain.trim() && principalSubject.trim()) {
      input.quota_principal = {
        method: principalMethod.trim(),
        domain: principalDomain.trim(),
        subject: principalSubject.trim(),
      };
      if (principalIssuer.trim()) input.quota_principal.issuer = principalIssuer.trim();
    }
    run.mutate(input);
  }

  return (
    <Drawer
      title="Test a route"
      subtitle="See how Jul will route a request without sending real traffic."
      onClose={onClose}
    >
      <div className="space-y-5">
        <p className="text-xs text-jul-muted">
          Route Test is a dry run. Quota identity inputs below are hypothetical and are not
          authenticated; client-supplied headers are never trusted for classification.
        </p>
        <div className="grid grid-cols-[100px_1fr] gap-3">
          <Field label="Method" value={method} placeholder="GET" onChange={setMethod} />
          <Field label="Path" value={path} placeholder="/api/users" onChange={setPath} />
        </div>
        <Field label="Host (optional)" value={host} placeholder="example.com" onChange={setHost} />
        <details className="rounded-md border border-jul-border p-3">
          <summary className="cursor-pointer text-sm font-medium text-jul-text">
            Simulate quota classification inputs
          </summary>
          <div className="mt-3 space-y-3">
            <Field
              label="Attributed client address (optional)"
              value={clientAddress}
              placeholder="192.0.2.10"
              onChange={setClientAddress}
            />
            <p className="text-xs text-jul-muted">
              The address is simulated as an attributed canonical client address for CIDR-pool
              matching.
            </p>
            <Field
              label="Hypothetical verified principal method"
              value={principalMethod}
              placeholder="forward, basic, or jwt"
              onChange={setPrincipalMethod}
            />
            <Field
              label="Principal domain"
              value={principalDomain}
              placeholder="services"
              onChange={setPrincipalDomain}
            />
            <Field
              label="Principal issuer (JWT)"
              value={principalIssuer}
              placeholder="https://issuer.example"
              onChange={setPrincipalIssuer}
            />
            <Field
              label="Principal subject"
              value={principalSubject}
              placeholder="account identifier"
              onChange={setPrincipalSubject}
            />
          </div>
        </details>

        <button
          type="button"
          onClick={onRun}
          disabled={run.isPending}
          className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
        >
          {run.isPending ? "Testing…" : "Test route"}
        </button>

        {run.isError && (
          <p className="text-xs text-jul-danger">
            {run.error instanceof Error ? run.error.message : "Test failed."}
          </p>
        )}
        {simulationError && <p className="text-xs text-jul-danger">{simulationError}</p>}
        {run.data && <Result res={run.data} />}
      </div>
    </Drawer>
  );
}
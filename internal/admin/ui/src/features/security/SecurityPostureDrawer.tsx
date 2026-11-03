/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchSecurityPosture } from "@/api/client.ts";

export interface SecurityPostureDrawerProps {
  readonly onClose: () => void;
}

export function SecurityPostureDrawer({ onClose }: SecurityPostureDrawerProps) {
  const [selectedArea, setSelectedArea] = useState<string>("all");
  const query = useQuery({
    queryKey: ["security-posture"],
    queryFn: fetchSecurityPosture,
    retry: false,
  });

  const data = query.data;
  const findings = (data?.findings ?? []).filter(
    (f) => selectedArea === "all" || f.area === selectedArea,
  );

  return (
    <Drawer
      title="Gateway security posture review"
      subtitle="Finite 7-area exposure & configuration review with explicit epistemic state (#633)."
      onClose={onClose}
    >
      <div className="space-y-4 text-xs">
        {query.isPending && <p className="text-jul-muted">Evaluating gateway security posture…</p>}
        {query.isError && (
          <p className="text-jul-danger">
            Failed to load security posture:{" "}
            {query.error instanceof Error ? query.error.message : "unknown error"}
          </p>
        )}

        {data && (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-jul-border bg-jul-surface px-3 py-2">
              <span>
                Overall status: <strong className="uppercase">{data.overall_status}</strong>
              </span>
              <span className="text-jul-muted">
                {String(data.error_count)} errors · {String(data.warning_count)} warnings ·{" "}
                {String(data.info_count)} informational notes
              </span>
            </div>

            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {data.areas.map((a) => (
                <button
                  key={a.area}
                  type="button"
                  onClick={() => {
                    setSelectedArea(selectedArea === a.area ? "all" : a.area);
                  }}
                  className={`rounded-md border p-2.5 text-left transition ${
                    selectedArea === a.area
                      ? "border-jul-accent bg-jul-accent/10"
                      : "border-jul-border bg-jul-surface hover:bg-jul-bg/40"
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-semibold uppercase tracking-wider text-jul-text">
                      {a.area}
                    </span>
                    <span
                      className={`rounded-full px-2 py-0.5 font-medium ${
                        a.status === "error"
                          ? "bg-jul-danger/15 text-jul-danger"
                          : a.status === "warning"
                            ? "bg-jul-warning/15 text-jul-warning"
                            : "bg-jul-success/15 text-jul-success"
                      }`}
                    >
                      {a.status} · {a.epistemic_state}
                    </span>
                  </div>
                  <p className="mt-1 text-jul-muted">{a.summary}</p>
                </button>
              ))}
            </div>

            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="font-semibold text-jul-text">
                  Findings ({String(findings.length)})
                </span>
                {selectedArea !== "all" && (
                  <button
                    type="button"
                    onClick={() => {
                      setSelectedArea("all");
                    }}
                    className="text-jul-accent hover:underline"
                  >
                    Show all areas
                  </button>
                )}
              </div>

              {findings.length === 0 ? (
                <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-jul-muted">
                  No findings for this area.
                </p>
              ) : (
                findings.map((f, idx) => (
                  <div
                    key={`${f.code}-${String(idx)}`}
                    className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <code className="font-mono font-semibold text-jul-text">{f.code}</code>
                      <div className="flex items-center gap-1.5">
                        <span
                          className={`rounded-full px-2 py-0.5 font-medium ${
                            f.severity === "error"
                              ? "bg-jul-danger/15 text-jul-danger"
                              : f.severity === "warning"
                                ? "bg-jul-warning/15 text-jul-warning"
                                : "bg-jul-border/40 text-jul-muted"
                          }`}
                        >
                          {f.severity}
                        </span>
                        <span className="rounded border border-jul-border px-1.5 py-0.5 text-jul-muted">
                          {f.epistemic_state}
                        </span>
                      </div>
                    </div>
                    <p className="text-jul-text">{f.summary}</p>
                    {f.remediation && (
                      <p className="text-jul-muted">Remediation: {f.remediation}</p>
                    )}
                  </div>
                ))
              )}
            </div>
          </>
        )}
      </div>
    </Drawer>
  );
}

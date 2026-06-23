import { useQuery } from "@tanstack/react-query";
import { fetchRawConfig } from "@/api/client.ts";

export function ConfigPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["raw-config"],
    queryFn: fetchRawConfig,
  });

  if (isLoading) return <div className="text-jul-muted">Loading configuration…</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load configuration.</div>;

  return (
    <div className="space-y-6">
      <div className="flex items-baseline gap-3">
        <h1 className="text-xl font-semibold">Configuration</h1>
        {data.path && (
          <span className="text-xs text-jul-muted font-mono">{data.path}</span>
        )}
      </div>

      {data.raw ? (
        <div className="rounded-lg border border-jul-border bg-jul-surface">
          <div className="flex items-center justify-between border-b border-jul-border px-4 py-2">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
              Raw TOML (read-only)
            </span>
            <span className="text-xs text-jul-muted">
              {data.raw.split("\n").length} lines
            </span>
          </div>
          <pre className="max-h-[calc(100vh-280px)] overflow-auto p-4 text-xs text-jul-text leading-relaxed">
            {data.raw}
          </pre>
        </div>
      ) : (
        <p className="text-jul-muted text-sm">Raw config not available (read hook not wired).</p>
      )}
    </div>
  );
}

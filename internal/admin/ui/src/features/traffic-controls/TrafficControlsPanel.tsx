import { useQuery } from "@tanstack/react-query";
import { fetchTrafficControls } from "@/api/client.ts";

function SectionCard({
  title,
  active,
  children,
}: {
  readonly title: string;
  readonly active: boolean;
  readonly children: React.ReactNode;
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="flex items-center gap-3 border-b border-jul-border px-4 py-3">
        <span className="font-medium text-jul-text">{title}</span>
        <span
          className={`rounded-full px-2 py-0.5 text-xs font-medium ${
            active
              ? "bg-jul-success/15 text-jul-success"
              : "bg-jul-border text-jul-muted"
          }`}
        >
          {active ? "enabled" : "disabled"}
        </span>
      </div>
      <div className="px-4 py-3">{children}</div>
    </div>
  );
}

function KV({ k, v }: { readonly k: string; readonly v: string | number | undefined }) {
  if (v === undefined || v === "") return null;
  return (
    <div className="flex gap-3 text-sm">
      <span className="w-28 shrink-0 text-jul-muted">{k}</span>
      <span className="font-mono text-jul-text">{v}</span>
    </div>
  );
}

export function TrafficControlsPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["traffic-controls"],
    queryFn: fetchTrafficControls,
  });

  if (isLoading) return <div className="text-jul-muted">Loading traffic controls…</div>;
  if (isError || !data)
    return <div className="text-jul-danger">Failed to load traffic controls.</div>;

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">Traffic Controls</h1>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {/* Compression */}
        <SectionCard title="Compression" active={data.compression?.enabled ?? false}>
          {data.compression?.enabled ? (
            <div className="space-y-1">
              <KV
                k="encoders"
                v={
                  data.compression.encoders && data.compression.encoders.length > 0
                    ? data.compression.encoders.join(", ")
                    : "gzip"
                }
              />
            </div>
          ) : (
            <p className="text-xs text-jul-muted">No compression configured.</p>
          )}
        </SectionCard>

        {/* Rate Limiting */}
        <SectionCard title="Rate Limiting" active={data.rate_limit?.enabled ?? false}>
          {data.rate_limit?.enabled ? (
            <div className="space-y-1">
              <KV k="key" v={data.rate_limit.key || "ip"} />
              <KV k="rate" v={data.rate_limit.rate !== undefined ? `${String(data.rate_limit.rate)}/s` : undefined} />
              <KV k="burst" v={data.rate_limit.burst} />
            </div>
          ) : (
            <p className="text-xs text-jul-muted">No rate limit configured.</p>
          )}
        </SectionCard>

        {/* Cache */}
        <SectionCard title="Cache" active={data.cache?.enabled ?? false}>
          {data.cache?.enabled ? (
            <div className="space-y-1">
              <KV k="default TTL" v={data.cache.default_ttl} />
              <KV k="memory max" v={data.cache.memory_max} />
              {data.cache.disk_path && <KV k="disk" v="enabled" />}
            </div>
          ) : (
            <p className="text-xs text-jul-muted">No cache configured.</p>
          )}
        </SectionCard>
      </div>
    </div>
  );
}

import { useQuery } from "@tanstack/react-query";
import { fetchOverview, type FeatureStatus } from "@/api/client.ts";

// Group status rows by their `group` field.
function groupBy<T>(items: T[], key: (item: T) => string): Map<string, T[]> {
  const m = new Map<string, T[]>();
  for (const item of items) {
    const k = key(item);
    const existing = m.get(k) ?? [];
    existing.push(item);
    m.set(k, existing);
  }
  return m;
}

function StatusBadge({ active }: { readonly active: boolean }) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${
        active
          ? "bg-jul-success/15 text-jul-success"
          : "bg-jul-border text-jul-muted"
      }`}
    >
      {active ? "active" : "inactive"}
    </span>
  );
}

function StatusGroup({
  name,
  rows,
}: {
  readonly name: string;
  readonly rows: FeatureStatus[];
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      <div className="border-b border-jul-border px-4 py-2">
        <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
          {name}
        </span>
      </div>
      <ul>
        {rows.map((row) => (
          <li
            key={row.name}
            className="flex items-center gap-3 border-b border-jul-border px-4 py-3 last:border-b-0"
          >
            <StatusBadge active={row.active} />
            <span className="flex-1 text-sm text-jul-text">{row.name}</span>
            {row.detail !== undefined && (
              <span className="text-xs text-jul-muted">{row.detail}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function OverviewPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["overview"],
    queryFn: fetchOverview,
  });

  if (isLoading) {
    return <div className="text-jul-muted">Loading overview…</div>;
  }
  if (isError || !data) {
    return <div className="text-jul-danger">Failed to load overview.</div>;
  }

  const groups = groupBy(data.status, (r) => r.group);

  return (
    <div className="space-y-6">
      <div className="flex items-baseline gap-3">
        <h1 className="text-xl font-semibold">{data.product}</h1>
        {data.version && (
          <span className="text-xs text-jul-muted">v{data.version}</span>
        )}
      </div>

      {groups.size === 0 ? (
        <p className="text-jul-muted text-sm">No status rows available.</p>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {Array.from(groups.entries()).map(([group, rows]) => (
            <StatusGroup key={group} name={group} rows={rows} />
          ))}
        </div>
      )}
    </div>
  );
}


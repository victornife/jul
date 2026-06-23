import { useQuery } from "@tanstack/react-query";
import { fetchOverview } from "@/api/client.ts";

export function OverviewPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["overview"],
    queryFn: fetchOverview,
  });

  if (isLoading) return <div className="text-jul-muted">Loading overview...</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load overview.</div>;

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Overview</h1>
      <pre className="rounded-md border border-jul-border bg-jul-surface p-4 text-xs text-jul-muted">
        {JSON.stringify(data, null, 2)}
      </pre>
    </div>
  );
}

import { useQuery } from "@tanstack/react-query";
import { fetchTLS, type CertProjection } from "@/api/client.ts";

function daysLeftColor(days: number | undefined): string {
  if (days === undefined) return "text-jul-muted";
  if (days <= 7) return "text-jul-danger";
  if (days <= 30) return "text-jul-warning";
  return "text-jul-success";
}

function CertCard({ cert }: { readonly cert: CertProjection }) {
  const daysLeft = cert.days_left;

  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface p-4 space-y-3">
      {/* server names */}
      <div className="flex flex-wrap items-center gap-2">
        {cert.server_names.map((sn) => (
          <span key={sn} className="font-mono text-sm font-semibold text-jul-text">
            {sn}
          </span>
        ))}
        <span
          className={`ml-auto rounded-full px-2 py-0.5 text-xs font-medium ${
            cert.source === "acme"
              ? "bg-jul-accent/15 text-jul-accent"
              : "bg-jul-border text-jul-muted"
          }`}
        >
          {cert.source}
        </span>
      </div>

      {/* metadata */}
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
        {cert.issuer && (
          <>
            <dt className="text-jul-muted">Issuer</dt>
            <dd className="col-span-1 sm:col-span-2 font-mono text-jul-text">
              {cert.issuer}
            </dd>
          </>
        )}
        {cert.not_after && (
          <>
            <dt className="text-jul-muted">Expires</dt>
            <dd className="col-span-1 sm:col-span-2 font-mono text-jul-text">
              {cert.not_after}
            </dd>
          </>
        )}
        {daysLeft !== undefined && (
          <>
            <dt className="text-jul-muted">Days left</dt>
            <dd className={`col-span-1 sm:col-span-2 font-semibold ${daysLeftColor(daysLeft)}`}>
              {daysLeft}
            </dd>
          </>
        )}
      </dl>
    </div>
  );
}

export function TLSPanel() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ["tls"],
    queryFn: fetchTLS,
  });

  if (isLoading) return <div className="text-jul-muted">Loading TLS certificates…</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load TLS info.</div>;

  const expiringSoon = data.filter(
    (c) => c.days_left !== undefined && c.days_left <= 30,
  );

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">TLS &amp; Certificates</h1>

      {expiringSoon.length > 0 && (
        <div className="rounded-lg border border-jul-warning/40 bg-jul-warning/10 px-4 py-3 text-sm text-jul-warning">
          ⚠ {expiringSoon.length} certificate{expiringSoon.length > 1 ? "s" : ""} expiring
          within 30 days.
        </div>
      )}

      {data.length === 0 ? (
        <p className="text-jul-muted text-sm">No TLS-enabled server blocks.</p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {data.map((cert, i) => (
            <CertCard key={`${cert.server_names.join(",")}-${String(i)}`} cert={cert} />
          ))}
        </div>
      )}
    </div>
  );
}

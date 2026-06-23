import type { ConfigDiff } from "@/api/client.ts";

function Section({
  title,
  entries,
  tone,
}: {
  readonly title: string;
  readonly entries: NonNullable<ConfigDiff["additions"]>;
  readonly tone: "add" | "remove" | "modify";
}) {
  if (entries.length === 0) return null;
  const toneClass =
    tone === "add"
      ? "text-jul-success"
      : tone === "remove"
        ? "text-jul-danger"
        : "text-jul-warning";
  const sign = tone === "add" ? "+" : tone === "remove" ? "−" : "~";
  return (
    <div className="space-y-1">
      <h4 className={`text-xs font-semibold uppercase tracking-wider ${toneClass}`}>
        {title} ({entries.length})
      </h4>
      <ul className="space-y-1">
        {entries.map((e, i) => (
          <li
            key={`${e.kind}-${e.name ?? String(i)}`}
            className="flex gap-2 font-mono text-xs text-jul-text"
          >
            <span className={`shrink-0 ${toneClass}`}>{sign}</span>
            <span className="text-jul-muted">{e.kind}</span>
            {e.name && <span>{e.name}</span>}
            {e.detail && <span className="text-jul-muted">— {e.detail}</span>}
            {(e.before ?? e.after) && (
              <span className="text-jul-muted">
                {e.before ?? "∅"} → {e.after ?? "∅"}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Renders a structured config diff (summary, warnings, and grouped changes). */
export function DiffView({ diff }: { readonly diff: ConfigDiff }) {
  const additions = diff.additions ?? [];
  const removals = diff.removals ?? [];
  const modifications = diff.modifications ?? [];
  const warnings = diff.warnings ?? [];
  const empty =
    additions.length === 0 && removals.length === 0 && modifications.length === 0;

  return (
    <div className="space-y-3 rounded-md border border-jul-border bg-jul-bg p-4">
      <p className="text-sm text-jul-text">{diff.summary}</p>
      {diff.affected && diff.affected.length > 0 && (
        <p className="text-xs text-jul-muted">
          Affected: {diff.affected.join(", ")}
        </p>
      )}
      {warnings.length > 0 && (
        <ul className="space-y-1">
          {warnings.map((w, i) => (
            <li key={`warn-${String(i)}`} className="text-xs text-jul-warning">
              ⚠ {w}
            </li>
          ))}
        </ul>
      )}
      {empty ? (
        <p className="text-xs text-jul-muted">No structural changes detected.</p>
      ) : (
        <div className="space-y-3">
          <Section title="Added" entries={additions} tone="add" />
          <Section title="Removed" entries={removals} tone="remove" />
          <Section title="Modified" entries={modifications} tone="modify" />
        </div>
      )}
    </div>
  );
}

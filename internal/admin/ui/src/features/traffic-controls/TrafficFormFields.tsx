/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Lightweight form-field primitives and the AffectedRoutes display component
 * used by TrafficControlEditor. Scoped to the traffic-controls feature; not
 * part of the global design-system ui.tsx (these are opinionated for this
 * drawer's compact form layout).
 */

export function TextField({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
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
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

export function NumberField({
  label,
  value,
  onChange,
}: {
  readonly label: string;
  readonly value: number;
  readonly onChange: (v: number) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="number"
        min={0}
        value={value}
        onChange={(e) => {
          onChange(Math.max(0, Number(e.target.value) || 0));
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
    </label>
  );
}

export function Toggle({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-center gap-2">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border accent-jul-accent"
      />
      <span className="text-sm text-jul-text">{label}</span>
    </label>
  );
}

export function CheckboxGroup({
  label,
  options,
  selected,
  onToggle,
}: {
  readonly label: string;
  readonly options: string[];
  readonly selected: string[];
  readonly onToggle: (value: string, on: boolean) => void;
}) {
  return (
    <div className="space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <div className="flex flex-wrap gap-3">
        {options.map((o) => (
          <Toggle
            key={o}
            label={o}
            checked={selected.includes(o)}
            onChange={(on) => {
              onToggle(o, on);
            }}
          />
        ))}
      </div>
    </div>
  );
}

/**
 * Lists the route paths that opt into a given edge feature so an operator can
 * preview which routes a global change touches (Milestones 3.1–3.3).
 */
export function AffectedRoutes({
  title,
  paths,
  emptyHint,
}: {
  readonly title: string;
  readonly paths: string[];
  readonly emptyHint: string;
}) {
  return (
    <div className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3">
      <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">{title}</span>
      {paths.length === 0 ? (
        <p className="text-xs text-jul-muted">{emptyHint}</p>
      ) : (
        <ul className="flex flex-wrap gap-1.5">
          {paths.map((p) => (
            <li
              key={p}
              className="rounded-full bg-jul-accent/15 px-2 py-0.5 font-mono text-xs text-jul-accent"
            >
              {p}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

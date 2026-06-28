import { useRef, useState, type ReactNode } from "react";
import { useFocusTrap } from "@/lib/useFocusTrap.ts";

// Shared component system for Console v2 (Milestone 4.5). These primitives wrap
// the semantic jul-* design tokens so every screen looks consistent in both
// themes and individual panels stop re-implementing inputs, buttons, and cards.

// ── PageHeader (Milestone 4.3) ───────────────────────────────────────────────

export function PageHeader({
  title,
  description,
  actions,
}: {
  readonly title: string;
  readonly description?: string;
  readonly actions?: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold text-jul-text">{title}</h1>
        {description && <p className="max-w-3xl text-sm text-jul-muted">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

// ── Button ───────────────────────────────────────────────────────────────────

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

const BUTTON_VARIANTS: Record<ButtonVariant, string> = {
  primary: "bg-jul-accent text-jul-bg hover:brightness-110",
  secondary: "border border-jul-border text-jul-text hover:bg-jul-bg",
  ghost: "text-jul-muted hover:text-jul-text",
  danger: "bg-jul-danger text-jul-bg hover:brightness-110",
};

export function Button({
  children,
  variant = "secondary",
  type = "button",
  disabled = false,
  onClick,
  title,
  ariaLabel,
}: {
  readonly children: ReactNode;
  readonly variant?: ButtonVariant;
  readonly type?: "button" | "submit";
  readonly disabled?: boolean;
  readonly onClick?: () => void;
  readonly title?: string;
  readonly ariaLabel?: string;
}) {
  return (
    <button
      type={type}
      disabled={disabled}
      onClick={onClick}
      title={title}
      aria-label={ariaLabel}
      className={`rounded-md px-3 py-1.5 text-sm font-medium transition-[filter,background-color,color] focus:outline-none focus:ring-2 focus:ring-jul-accent disabled:cursor-not-allowed disabled:opacity-50 ${BUTTON_VARIANTS[variant]}`}
    >
      {children}
    </button>
  );
}

// ── Badge ──────────────────────────────────────────────────────────────────--

type BadgeTone = "neutral" | "success" | "warning" | "danger" | "accent";

const BADGE_TONES: Record<BadgeTone, string> = {
  neutral: "bg-jul-border text-jul-muted",
  success: "bg-jul-success/15 text-jul-success",
  warning: "bg-jul-warning/15 text-jul-warning",
  danger: "bg-jul-danger/15 text-jul-danger",
  accent: "bg-jul-accent/15 text-jul-accent",
};

export function Badge({
  children,
  tone = "neutral",
}: {
  readonly children: ReactNode;
  readonly tone?: BadgeTone;
}) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${BADGE_TONES[tone]}`}
    >
      {children}
    </span>
  );
}

// ── MaturityBadge ───────────────────────────────────────────────────────────-
// Honest feature-maturity labeling per ADR 0003: a feature that is implemented
// but not yet at the GA bar is marked Beta so operators set correct
// expectations. Tooltip explains the level; the link to the maturity model is in
// the Console guide.
type Maturity = "beta" | "best-effort" | "experimental";

const MATURITY_HINT: Record<Maturity, string> = {
  beta: "Beta — usable, with known limitations; config/API may change before GA.",
  "best-effort": "Best-effort — no stability or completeness guarantees.",
  experimental: "Experimental — may change or be removed.",
};

export function MaturityBadge({ level = "beta" }: { readonly level?: Maturity }) {
  return (
    <span
      title={MATURITY_HINT[level]}
      className="inline-block rounded-full bg-jul-warning/15 px-2 py-0.5 text-xs font-medium uppercase tracking-wide text-jul-warning"
    >
      {level}
    </span>
  );
}

// ── Card ───────────────────────────────────────────────────────────────────--

export function Card({
  title,
  actions,
  children,
}: {
  readonly title?: ReactNode;
  readonly actions?: ReactNode;
  readonly children: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface">
      {(title ?? actions) && (
        <div className="flex items-center gap-3 border-b border-jul-border px-4 py-3">
          {typeof title === "string" ? (
            <span className="font-medium text-jul-text">{title}</span>
          ) : (
            title
          )}
          {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className="px-4 py-3">{children}</div>
    </div>
  );
}

// ── Alert ──────────────────────────────────────────────────────────────────--

type AlertTone = "info" | "warning" | "danger";

const ALERT_TONES: Record<AlertTone, string> = {
  info: "border-jul-border bg-jul-surface text-jul-muted",
  warning: "border-jul-warning/40 bg-jul-warning/10 text-jul-warning",
  danger: "border-jul-danger/40 bg-jul-danger/10 text-jul-danger",
};

export function Alert({
  tone = "info",
  title,
  children,
}: {
  readonly tone?: AlertTone;
  readonly title?: string;
  readonly children: ReactNode;
}) {
  return (
    <div className={`space-y-1 rounded-md border p-3 text-xs ${ALERT_TONES[tone]}`} role="status">
      {title && <div className="font-semibold uppercase tracking-wider">{title}</div>}
      <div>{children}</div>
    </div>
  );
}

// ── EmptyState (Milestone 4.4) ───────────────────────────────────────────────

export function EmptyState({
  title,
  description,
  action,
}: {
  readonly title: string;
  readonly description: string;
  readonly action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-jul-border bg-jul-surface px-6 py-12 text-center">
      <h3 className="text-sm font-semibold text-jul-text">{title}</h3>
      <p className="max-w-md text-sm text-jul-muted">{description}</p>
      {action}
    </div>
  );
}

// ── Spinner / Loading (Phase 2 — consistent async feedback) ──────────────────

// Spinner is a dependency-free animated indicator shown wherever an async action
// is in flight (button presses, inline fetches). It inherits the current text
// colour via `border-current`, so it tints itself to whatever it sits inside.
// It is decorative (aria-hidden); pair it with visible or screen-reader text —
// as Loading does below — when the state needs to be announced.
export function Spinner({ className = "" }: { readonly className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`inline-block h-4 w-4 shrink-0 animate-spin rounded-full border-2 border-current border-r-transparent ${className}`}
    />
  );
}

// Loading is the standard full-panel "still fetching" state: a spinner beside a
// short label, so every screen reports progress the same way instead of each
// panel inventing its own bare "Loading X…" text. role="status" announces the
// label to assistive technology.
export function Loading({ label = "Loading…" }: { readonly label?: string }) {
  return (
    <div role="status" className="flex items-center gap-2 text-sm text-jul-muted">
      <Spinner />
      <span>{label}</span>
    </div>
  );
}

// ── Form fields ──────────────────────────────────────────────────────────────

export function TextField({
  label,
  hint,
  value,
  placeholder,
  mono = true,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly mono?: boolean;
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
        className={`w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent ${
          mono ? "font-mono" : ""
        }`}
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
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
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

// ── Select ───────────────────────────────────────────────────────────────────

export function Select({
  label,
  hint,
  value,
  options,
  onChange,
}: {
  readonly label?: string;
  readonly hint?: string;
  readonly value: string;
  readonly options: { value: string; label: string }[];
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      {label && <span className="text-sm font-medium text-jul-text">{label}</span>}
      <select
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

// ── Switch ───────────────────────────────────────────────────────────────────

// Switch is a styled on/off control. Unlike Toggle (a labelled checkbox) it
// renders the familiar sliding pill and is used where the on/off state is the
// primary affordance.
export function Switch({
  label,
  checked,
  onChange,
}: {
  readonly label?: string;
  readonly checked: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => {
          onChange(!checked);
        }}
        className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-jul-accent ${
          checked ? "bg-jul-accent" : "bg-jul-border"
        }`}
      >
        <span
          className={`inline-block h-4 w-4 transform rounded-full bg-jul-bg transition-transform ${
            checked ? "translate-x-4" : "translate-x-0.5"
          }`}
        />
      </button>
      {label}
    </label>
  );
}

// ── StatusPill ─────────────────────────────────────────────────────────────--

// StatusPill is a Badge with a leading status dot for active/inactive (or
// healthy/unhealthy) state, standardising how the Console signals liveness.
export function StatusPill({
  active,
  labels,
}: {
  readonly active: boolean;
  readonly labels?: { on: string; off: string };
}) {
  const on = labels?.on ?? "active";
  const off = labels?.off ?? "inactive";
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${
        active ? "bg-jul-success/15 text-jul-success" : "bg-jul-border text-jul-muted"
      }`}
    >
      <span
        className={`inline-block h-1.5 w-1.5 rounded-full ${
          active ? "bg-jul-success" : "bg-jul-muted"
        }`}
      />
      {active ? on : off}
    </span>
  );
}

// ── Tooltip ──────────────────────────────────────────────────────────────────

// Tooltip wraps children with a hover/focus description. It uses the native
// title attribute for accessibility plus a styled popover on hover.
export function Tooltip({
  text,
  children,
}: {
  readonly text: string;
  readonly children: ReactNode;
}) {
  return (
    <span className="group relative inline-flex" title={text}>
      {children}
      <span
        role="tooltip"
        className="pointer-events-none absolute bottom-full left-1/2 z-30 mb-1 -translate-x-1/2 whitespace-nowrap rounded-md border border-jul-border bg-jul-surface px-2 py-1 text-xs text-jul-text opacity-0 shadow-lg transition-opacity group-hover:opacity-100"
      >
        {text}
      </span>
    </span>
  );
}

// ── Modal ──────────────────────────────────────────────────────────────────--

export function Modal({
  title,
  children,
  onClose,
  footer,
}: {
  readonly title: string;
  readonly children: ReactNode;
  readonly onClose: () => void;
  readonly footer?: ReactNode;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef);
  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center p-4">
      <button
        type="button"
        aria-label="Close"
        className="absolute inset-0 cursor-default bg-black/50"
        onClick={onClose}
      />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="relative z-50 w-full max-w-lg space-y-4 rounded-lg border border-jul-border bg-jul-surface p-5 shadow-xl outline-none"
      >
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold text-jul-text">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close dialog"
            className="rounded-md px-2 py-1 text-jul-muted hover:text-jul-text"
          >
            ✕
          </button>
        </div>
        <div>{children}</div>
        {footer && <div className="flex justify-end gap-2">{footer}</div>}
      </div>
    </div>
  );
}

// ── Tabs ───────────────────────────────────────────────────────────────────--

export function Tabs({
  tabs,
  initial,
}: {
  readonly tabs: { id: string; label: string; content: ReactNode }[];
  readonly initial?: string;
}) {
  const [active, setActive] = useState(initial ?? tabs[0]?.id ?? "");
  const current = tabs.find((t) => t.id === active) ?? tabs[0];
  return (
    <div className="space-y-3">
      <div role="tablist" className="flex gap-1 border-b border-jul-border">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={t.id === active}
            onClick={() => {
              setActive(t.id);
            }}
            className={`-mb-px border-b-2 px-3 py-1.5 text-sm transition-colors ${
              t.id === active
                ? "border-jul-accent text-jul-text"
                : "border-transparent text-jul-muted hover:text-jul-text"
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div role="tabpanel">{current?.content}</div>
    </div>
  );
}

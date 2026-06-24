import type { ReactNode } from "react";

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
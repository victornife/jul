import { useState } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";
import { useTheme, type ThemePreference } from "@/lib/theme.ts";
import { usePersistentState, resetPreferences } from "@/lib/usePersistentState.ts";
import { ConsoleHealthBadge } from "@/features/observability/ConsoleHealthBadge.tsx";

const NAV = [
  { to: "/", label: "Overview", exact: true },
  { to: "/routes", label: "Routes" },
  { to: "/apps", label: "Apps" },
  { to: "/tls", label: "TLS" },
  { to: "/security", label: "Security" },
  { to: "/traffic", label: "Traffic" },
  { to: "/search", label: "Search" },
  { to: "/observability", label: "Events" },
  { to: "/operations", label: "Operations" },
  { to: "/timeline", label: "Timeline" },
  { to: "/audit", label: "Audit" },
  { to: "/config", label: "Config" },
  { to: "/history", label: "History" },
  { to: "/wizard", label: "Wizard" },
];

function isActive(navTo: string, pathname: string, exact = false): boolean {
  return exact ? pathname === navTo : pathname.startsWith(navTo);
}

const THEME_LABEL: Record<ThemePreference, string> = {
  system: "🖥 System",
  light: "☀ Light",
  dark: "🌙 Dark",
};

const THEME_ORDER: ThemePreference[] = ["system", "light", "dark"];

type NavLayout = "top" | "side";

function isNavLayout(v: unknown): v is NavLayout {
  return v === "top" || v === "side";
}

// PreferenceMenu (Milestone 4.5/4.6) collects the View preferences — theme,
// navigation layout — into one discoverable popover and offers a single
// "Reset to defaults" action that clears every persisted preference.
function PreferenceMenu({
  layout,
  onLayout,
}: {
  readonly layout: NavLayout;
  readonly onLayout: (v: NavLayout) => void;
}) {
  const { preference, setPreference } = useTheme();
  const [open, setOpen] = useState(false);

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => {
          setOpen((o) => !o);
        }}
        aria-haspopup="menu"
        aria-expanded={open}
        title="View & preferences"
        className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent"
      >
        ⚙ View
      </button>
      {open && (
        <>
          <button
            type="button"
            aria-label="Close preferences"
            className="fixed inset-0 z-10 cursor-default"
            onClick={() => {
              setOpen(false);
            }}
          />
          <div
            role="menu"
            className="absolute right-0 z-20 mt-1 w-60 space-y-3 rounded-md border border-jul-border bg-jul-surface p-3 shadow-lg"
          >
            <div className="space-y-1">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Theme
              </span>
              <div className="flex gap-1">
                {THEME_ORDER.map((t) => (
                  <button
                    key={t}
                    type="button"
                    onClick={() => {
                      setPreference(t);
                    }}
                    className={`flex-1 rounded-md border px-2 py-1 text-xs ${
                      preference === t
                        ? "border-jul-accent bg-jul-accent/15 text-jul-accent"
                        : "border-jul-border text-jul-text hover:bg-jul-bg"
                    }`}
                  >
                    {THEME_LABEL[t]}
                  </button>
                ))}
              </div>
            </div>

            <div className="space-y-1">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Navigation
              </span>
              <div className="flex gap-1">
                <button
                  type="button"
                  onClick={() => {
                    onLayout("top");
                  }}
                  className={`flex-1 rounded-md border px-2 py-1 text-xs ${
                    layout === "top"
                      ? "border-jul-accent bg-jul-accent/15 text-jul-accent"
                      : "border-jul-border text-jul-text hover:bg-jul-bg"
                  }`}
                >
                  ⬒ Top bar
                </button>
                <button
                  type="button"
                  onClick={() => {
                    onLayout("side");
                  }}
                  className={`flex-1 rounded-md border px-2 py-1 text-xs ${
                    layout === "side"
                      ? "border-jul-accent bg-jul-accent/15 text-jul-accent"
                      : "border-jul-border text-jul-text hover:bg-jul-bg"
                  }`}
                >
                  ◧ Sidebar
                </button>
              </div>
            </div>

            <div className="border-t border-jul-border pt-2">
              <button
                type="button"
                onClick={() => {
                  resetPreferences();
                  window.location.reload();
                }}
                className="w-full rounded-md border border-jul-danger/50 px-2 py-1 text-xs font-medium text-jul-danger hover:bg-jul-danger/10"
              >
                Reset to defaults
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function NavLinks({
  pathname,
  orientation,
}: {
  readonly pathname: string;
  readonly orientation: "row" | "col";
}) {
  return (
    <nav
      className={
        orientation === "row" ? "flex flex-wrap gap-1" : "flex flex-col gap-1"
      }
      aria-label="Primary"
    >
      {NAV.map((n) => (
        <Link
          key={n.to}
          to={n.to}
          aria-current={isActive(n.to, pathname, n.exact) ? "page" : undefined}
          className={`rounded-md px-3 py-1 text-sm transition-colors ${
            isActive(n.to, pathname, n.exact)
              ? "bg-jul-accent text-jul-bg font-medium"
              : "text-jul-muted hover:text-jul-text"
          }`}
        >
          {n.label}
        </Link>
      ))}
    </nav>
  );
}

export function Layout() {
  const loc = useLocation();
  const [layout, setLayout] = usePersistentState<NavLayout>("nav_layout", "top", isNavLayout);

  const controls = (
    <div className="flex items-center gap-2">
      <ConsoleHealthBadge />
      <PreferenceMenu layout={layout} onLayout={setLayout} />
    </div>
  );

  if (layout === "side") {
    return (
      <div className="flex h-screen bg-jul-bg text-jul-text">
        <aside className="flex w-56 shrink-0 flex-col gap-4 border-r border-jul-border bg-jul-surface px-4 py-4">
          <div className="flex items-baseline gap-2">
            <span className="font-bold tracking-wide text-jul-accent">Jul.IA</span>
            <span className="text-xs text-jul-muted">v2</span>
          </div>
          <NavLinks pathname={loc.pathname} orientation="col" />
          <div className="mt-auto">{controls}</div>
        </aside>
        <main className="flex-1 overflow-auto p-6">
          <Outlet />
        </main>
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col bg-jul-bg text-jul-text">
      <header className="flex items-center gap-4 border-b border-jul-border bg-jul-surface px-6 py-3">
        <span className="font-bold tracking-wide text-jul-accent">Jul.IA</span>
        <span className="text-xs text-jul-muted">Console v2</span>
        <div className="ml-auto flex items-center gap-4">
          <NavLinks pathname={loc.pathname} orientation="row" />
          {controls}
        </div>
      </header>
      <main className="flex-1 overflow-auto p-6">
        <Outlet />
      </main>
    </div>
  );
}
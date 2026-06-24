import { useState } from "react";
import { Link, Outlet, useLocation } from "react-router-dom";
import { useTheme, type ThemePreference } from "@/lib/theme.ts";
import { usePersistentState, resetPreferences } from "@/lib/usePersistentState.ts";
import { ConsoleHealthBadge } from "@/features/observability/ConsoleHealthBadge.tsx";

const NAV = [
  { to: "/", label: "Overview", glyph: "▣", exact: true },
  { to: "/routes", label: "Routes", glyph: "⇄" },
  { to: "/apps", label: "Apps", glyph: "▦" },
  { to: "/tls", label: "TLS", glyph: "🔒" },
  { to: "/security", label: "Security", glyph: "🛡" },
  { to: "/traffic", label: "Traffic", glyph: "📈" },
  { to: "/search", label: "Search", glyph: "🔍" },
  { to: "/observability", label: "Events", glyph: "⚡" },
  { to: "/operations", label: "Operations", glyph: "🛠" },
  { to: "/timeline", label: "Timeline", glyph: "🕒" },
  { to: "/audit", label: "Audit", glyph: "📋" },
  { to: "/config", label: "Config", glyph: "⚙" },
  { to: "/history", label: "History", glyph: "↩" },
  { to: "/wizard", label: "Wizard", glyph: "✨" },
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

function isBool(v: unknown): v is boolean {
  return typeof v === "boolean";
}

// PreferenceMenu (Milestone 4.5/4.6) collects the View preferences — theme,
// navigation layout — into one discoverable popover and offers a single
// "Reset to defaults" action that clears every persisted preference.
function PreferenceMenu({
  layout,
  onLayout,
  collapsed,
  onCollapsed,
}: {
  readonly layout: NavLayout;
  readonly onLayout: (v: NavLayout) => void;
  readonly collapsed: boolean;
  readonly onCollapsed: (v: boolean) => void;
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
              {layout === "side" && (
                <label className="mt-2 flex items-center gap-2 text-xs text-jul-text">
                  <input
                    type="checkbox"
                    checked={collapsed}
                    onChange={(e) => {
                      onCollapsed(e.target.checked);
                    }}
                  />
                  Collapse sidebar to icons
                </label>
              )}
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
  collapsed = false,
}: {
  readonly pathname: string;
  readonly orientation: "row" | "col";
  readonly collapsed?: boolean;
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
          title={collapsed ? n.label : undefined}
          className={`flex items-center gap-2 rounded-md py-1 text-sm transition-colors ${
            collapsed ? "justify-center px-2" : "px-3"
          } ${
            isActive(n.to, pathname, n.exact)
              ? "bg-jul-accent text-jul-bg font-medium"
              : "text-jul-muted hover:text-jul-text"
          }`}
        >
          <span aria-hidden className={collapsed ? "text-base" : "text-sm"}>
            {n.glyph}
          </span>
          {!collapsed && <span>{n.label}</span>}
        </Link>
      ))}
    </nav>
  );
}

export function Layout() {
  const loc = useLocation();
  const [layout, setLayout] = usePersistentState<NavLayout>("nav_layout", "top", isNavLayout);
  const [collapsed, setCollapsed] = usePersistentState<boolean>(
    "nav_collapsed",
    false,
    isBool,
  );

  const controls = (
    <div className="flex items-center gap-2">
      <ConsoleHealthBadge />
      <PreferenceMenu
        layout={layout}
        onLayout={setLayout}
        collapsed={collapsed}
        onCollapsed={setCollapsed}
      />
    </div>
  );

  if (layout === "side") {
    return (
      <div className="flex h-screen bg-jul-bg text-jul-text">
        <aside
          className={`flex shrink-0 flex-col gap-4 border-r border-jul-border bg-jul-surface py-4 ${
            collapsed ? "w-16 px-2" : "w-56 px-4"
          }`}
        >
          <div className="flex items-center justify-between">
            {collapsed ? (
              <span className="mx-auto font-bold tracking-wide text-jul-accent">J</span>
            ) : (
              <div className="flex items-baseline gap-2">
                <span className="font-bold tracking-wide text-jul-accent">Jul.IA</span>
                <span className="text-xs text-jul-muted">v2</span>
              </div>
            )}
            <button
              type="button"
              onClick={() => {
                setCollapsed(!collapsed);
              }}
              title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
              aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
              className="rounded-md border border-jul-border px-1.5 py-0.5 text-xs text-jul-muted hover:text-jul-text"
            >
              {collapsed ? "»" : "«"}
            </button>
          </div>
          <NavLinks pathname={loc.pathname} orientation="col" collapsed={collapsed} />
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
/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Link, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTheme, type ThemePreference } from "@/lib/theme.ts";
import { usePersistentState, resetPreferences } from "@/lib/usePersistentState.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { ConsoleHealthBadge } from "@/features/observability/ConsoleHealthBadge.tsx";
import { CommandPalette, type CommandItem } from "@/app/CommandPalette.tsx";
import { openCommandPalette } from "@/app/commandPaletteBus.ts";
import { fetchOverview, type CertRisk } from "@/api/client.ts";

type NavLayout = "top" | "side";

function isNavLayout(v: unknown): v is NavLayout {
  return v === "top" || v === "side";
}

function isBool(v: unknown): v is boolean {
  return typeof v === "boolean";
}

interface NavItem {
  readonly to: string;
  readonly label: string;
  readonly glyph: string;
  readonly exact?: boolean;
}

// Information architecture is task-driven, not feature-list driven (P1-7): the
// primary destinations are grouped into the three things an operator actually
// does — watch the system, change its configuration, and do so safely. Search
// is deliberately not in a group; it is a global action in the header/sidebar
// controls so it stays reachable from anywhere without competing with the
// task groups.
interface NavGroup {
  readonly label: string;
  readonly items: readonly NavItem[];
}

const NAV_GROUPS: readonly NavGroup[] = [
  {
    label: "Operate",
    items: [
      { to: "/", label: "Overview", glyph: "▣", exact: true },
      // Operations is the consolidated troubleshooting workspace; Events and
      // Timeline live inside it as tabs (C-4) rather than as separate nav nouns.
      { to: "/operations", label: "Operations", glyph: "🛠" },
    ],
  },
  {
    label: "Configure",
    items: [
      { to: "/routes", label: "Routes", glyph: "⇄" },
      { to: "/apps", label: "Apps", glyph: "▦" },
      { to: "/traffic", label: "Traffic", glyph: "📈" },
      { to: "/security", label: "Security", glyph: "🛡" },
      { to: "/plugins", label: "Plugins", glyph: "🧩" },
      { to: "/streams", label: "Streams", glyph: "🔌" },
      { to: "/tls", label: "TLS", glyph: "🔒" },
    ],
  },
  {
    label: "Change safely",
    items: [
      { to: "/wizard", label: "Wizard", glyph: "✨" },
      { to: "/config", label: "Config", glyph: "⚙" },
      { to: "/history", label: "History", glyph: "↩" },
      { to: "/audit", label: "Audit", glyph: "📋" },
    ],
  },
];

// SEARCH_ITEM is the global discovery action, kept out of the task groups.
const SEARCH_ITEM: NavItem = { to: "/search", label: "Search", glyph: "🔍" };

// COMMANDS flattens every destination (Search first, then each group's items)
// into the command-palette list, tagging each with its group for display and
// filtering. Derived once from the same source of truth as the nav so the two
// never drift.
const COMMANDS: readonly CommandItem[] = [
  { ...SEARCH_ITEM, group: "Global" },
  ...NAV_GROUPS.flatMap((g) => g.items.map((n) => ({ ...n, group: g.label }))),
];

function isActive(navTo: string, pathname: string, exact = false): boolean {
	const re = new RegExp(`^${navTo.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`);
	return exact ? pathname === navTo : re.test(pathname);
}

const THEME_LABEL: Record<ThemePreference, string> = {
  system: "🖥 System",
  light: "☀ Light",
  dark: "🌙 Dark",
};

const THEME_ORDER: ThemePreference[] = ["system", "light", "dark"];

// Platform-aware label for the command-palette shortcut so macOS users see ⌘K
// and everyone else sees Ctrl K. Computed once at module load.
const PALETTE_SHORTCUT =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.userAgent)
    ? "⌘K"
    : "Ctrl K";

// CommandPaletteButton makes the otherwise hidden Ctrl/Cmd+K palette
// discoverable: a labelled header affordance that opens it on click and shows
// the keyboard shortcut so operators learn it (P1-7 — search reachable from
// anywhere).
function CommandPaletteButton({ compact = false }: { readonly compact?: boolean }) {
  return (
    <button
      type="button"
      onClick={() => {
        openCommandPalette();
      }}
      title="Jump to any page (command palette)"
      className={`flex items-center gap-2 rounded-md border border-jul-border text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent ${compact ? "w-full justify-center px-2 py-1" : "px-2.5 py-1"}`}
    >
      <span aria-hidden>🔍</span>
      {!compact && (
        <>
          <span className="hidden sm:inline">Jump to…</span>
          <kbd className="rounded border border-jul-border bg-jul-bg px-1 font-mono text-[10px] text-jul-muted">
            {PALETTE_SHORTCUT}
          </kbd>
        </>
      )}
    </button>
  );
}

// PreferenceMenu (Milestone 4.5/4.6) collects the View preferences — theme,
// navigation layout — into one discoverable popover and offers a single
// "Reset to defaults" action that clears every persisted preference.
function PreferenceMenu({
  layout,
  onLayout,
  collapsed,
  onCollapsed,
  compact = false,
  inSidebar = false,
}: {
  readonly layout: NavLayout;
  readonly onLayout: (v: NavLayout) => void;
  readonly collapsed: boolean;
  readonly onCollapsed: (v: boolean) => void;
  readonly compact?: boolean;
  readonly inSidebar?: boolean;
}) {
  const { preference, setPreference } = useTheme();
  const [open, setOpen] = useState(false);
  const [showResetConfirm, setShowResetConfirm] = useState(false);

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
        className={`rounded-md border border-jul-border text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent ${compact ? "w-full px-2 py-1" : "px-2.5 py-1"}`}
      >
        {compact ? "⚙" : "⚙ View"}
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
            className={`absolute z-20 w-60 max-h-[80vh] overflow-y-auto space-y-3 rounded-md border border-jul-border bg-jul-surface p-3 shadow-lg ${
              inSidebar ? "left-full top-0 ml-1" : "right-0 mt-1"
            }`}
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
                  setShowResetConfirm(true);
                }}
                className="w-full rounded-md border border-jul-danger/50 px-2 py-1 text-xs font-medium text-jul-danger hover:bg-jul-danger/10"
              >
                Reset to defaults
              </button>
            </div>
          </div>
        </>
      )}
      {showResetConfirm && (
        <ConfirmDialog
          title="Reset preferences"
          confirmLabel="Reset"
          danger
          onConfirm={() => {
            resetPreferences();
            window.location.reload();
          }}
          onCancel={() => {
            setShowResetConfirm(false);
          }}
        >
          <p className="text-sm text-jul-text">
            Reset all view preferences (theme, layout) to their defaults and
            reload the page?
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}

function NavItemLink({
  n,
  pathname,
  collapsed,
}: {
  readonly n: NavItem;
  readonly pathname: string;
  readonly collapsed: boolean;
}) {
  return (
    <Link
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
  // The sidebar shows the task groups with headers; the top bar keeps a single
  // flat row (group order preserved) so the header stays compact. Search leads
  // both layouts as the global discovery action.
  if (orientation === "col") {
    return (
      <nav className="flex flex-col gap-4" aria-label="Primary">
        <NavItemLink n={SEARCH_ITEM} pathname={pathname} collapsed={collapsed} />
        {NAV_GROUPS.map((g) => (
          <div key={g.label} className="flex flex-col gap-1">
            {!collapsed && (
              <span className="px-3 pt-1 text-[10px] font-semibold uppercase tracking-wider text-jul-muted">
                {g.label}
              </span>
            )}
            {collapsed && <span className="mx-auto h-px w-6 bg-jul-border" aria-hidden />}
            {g.items.map((n) => (
              <NavItemLink key={n.to} n={n} pathname={pathname} collapsed={collapsed} />
            ))}
          </div>
        ))}
      </nav>
    );
  }
  return (
    <nav className="flex flex-wrap gap-1" aria-label="Primary">
      <NavItemLink n={SEARCH_ITEM} pathname={pathname} collapsed={false} />
      {NAV_GROUPS.flatMap((g) => g.items).map((n) => (
        <NavItemLink key={n.to} n={n} pathname={pathname} collapsed={false} />
      ))}
    </nav>
  );
}

// CertAlertBanner shows a persistent alert at the top of every page when
// certificates are expired or expiring soon. It is dismissible per session.
function CertAlertBanner({ certRisk }: { readonly certRisk: CertRisk | undefined }) {
	const navigate = useNavigate();
	const [dismissed, setDismissed] = useState(false);

	if (dismissed || !certRisk) return null;

	let text: string;
	let tone = "";
	if (certRisk.expired > 0) {
		text = `${certRisk.expired} certificate${certRisk.expired > 1 ? "s" : ""} expired`;
		if (certRisk.expiring_soon > 0) {
			text += `, ${certRisk.expiring_soon} expiring ≤ 7d`;
		}
		tone = "bg-jul-danger/15 text-jul-danger border-jul-danger/40";
	} else if (certRisk.expiring_soon > 0) {
		text = `${certRisk.expiring_soon} certificate${certRisk.expiring_soon > 1 ? "s" : ""} expiring within 7d`;
		tone = "bg-jul-warning/15 text-jul-warning border-jul-warning/40";
	} else if (certRisk.errors > 0) {
		text = `${certRisk.errors} certificate${certRisk.errors > 1 ? "s" : ""} with no live data`;
		tone = "bg-jul-warning/15 text-jul-warning border-jul-warning/40";
	} else {
		return null;
	}

	return (
		<div className={`border-b ${tone} px-6 py-2 text-xs font-medium flex items-center justify-between`} role="alert">
			<span>
				{text}. <button className="underline" onClick={() => navigate("/tls")}>Review →</button>
			</span>
			<button type="button" onClick={() => setDismissed(true)} className="text-xs opacity-60 hover:opacity-100" aria-label="Dismiss alert">
				✕
			</button>
		</div>
	);
}

export function Layout() {
  const loc = useLocation();
  const [layout, setLayout] = usePersistentState<NavLayout>("nav_layout", "top", isNavLayout);
  const [collapsed, setCollapsed] = usePersistentState<boolean>("nav_collapsed", false, isBool);

  const { data: overview } = useQuery({
    queryKey: ["console-version"],
    queryFn: fetchOverview,
    refetchInterval: 30000, // version changes rarely, poll every 30s
  });
  const product = overview?.product ?? "Jul.IA Console";
  const version = overview?.version ?? "";
  const certRisk = overview?.cert_risk;

  const controlsTop = (
    <div className="flex items-center gap-2">
      <ConsoleHealthBadge />
      <CommandPaletteButton />
      <PreferenceMenu
        layout={layout}
        onLayout={setLayout}
        collapsed={collapsed}
        onCollapsed={setCollapsed}
      />
    </div>
  );

  const controlsSide = (
    <div className="flex flex-col items-stretch gap-y-2">
      <ConsoleHealthBadge compact={collapsed} />
      <CommandPaletteButton compact={collapsed} />
      <PreferenceMenu
        layout={layout}
        onLayout={setLayout}
        collapsed={collapsed}
        onCollapsed={setCollapsed}
        compact={collapsed}
        inSidebar
      />
    </div>
  );

  // The palette is mounted once at the layout root so its Ctrl/Cmd+K shortcut is
  // available on every page.
  const palette = <CommandPalette commands={COMMANDS} />;

  if (layout === "side") {
    return (
      <div className="flex min-h-screen bg-jul-bg text-jul-text">
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
                <span className="font-bold tracking-wide text-jul-accent">{product}</span>
                {version && version !== "dev" && <span className="text-xs text-jul-muted">v{version}</span>}
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
          {controlsSide}
        </aside>
        <main className="flex-1 p-6">
          <CertAlertBanner certRisk={certRisk} />
          <Outlet />
        </main>
        <footer className="px-6 py-2 text-[10px] text-jul-muted">
          Licensed under AGPL-3.0-or-later
        </footer>
        {palette}
      </div>
    );
  }

  return (
    <div className="flex h-screen flex-col bg-jul-bg text-jul-text">
      <header className="flex items-center gap-4 border-b border-jul-border bg-jul-surface px-6 py-3">
        <span className="font-bold tracking-wide text-jul-accent">{product}</span>
        {version && <span className="text-xs text-jul-muted">v{version}</span>}
        <div className="ml-auto flex items-center gap-4">
          <NavLinks pathname={loc.pathname} orientation="row" />
          {controlsTop}
        </div>
      </header>
      <CertAlertBanner certRisk={certRisk} />
      <main className="flex-1 overflow-auto p-6">
        <Outlet />
      </main>
      <footer className="border-t border-jul-border px-6 py-2 text-[10px] text-jul-muted">
        Licensed under AGPL-3.0-or-later
      </footer>
      {palette}
    </div>
  );
}

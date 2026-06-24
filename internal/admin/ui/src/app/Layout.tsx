import { Link, Outlet, useLocation } from "react-router-dom";
import { useTheme, type ThemePreference } from "@/lib/theme.ts";
import { usePersistentState } from "@/lib/usePersistentState.ts";

const NAV = [
  { to: "/", label: "Overview", exact: true },
  { to: "/routes", label: "Routes" },
  { to: "/apps", label: "Apps" },
  { to: "/tls", label: "TLS" },
  { to: "/security", label: "Security" },
  { to: "/traffic", label: "Traffic" },
  { to: "/search", label: "Search" },
  { to: "/observability", label: "Events" },
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

function ThemeToggle() {
  const { preference, setPreference } = useTheme();
  const next = (): void => {
    const i = THEME_ORDER.indexOf(preference);
    setPreference(THEME_ORDER[(i + 1) % THEME_ORDER.length] ?? "system");
  };
  return (
    <button
      type="button"
      onClick={next}
      title="Toggle theme (system / light / dark)"
      aria-label={`Theme: ${preference}. Click to change.`}
      className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent"
    >
      {THEME_LABEL[preference]}
    </button>
  );
}

type NavLayout = "top" | "side";

function isNavLayout(v: unknown): v is NavLayout {
  return v === "top" || v === "side";
}

function LayoutToggle({
  layout,
  onChange,
}: {
  readonly layout: NavLayout;
  readonly onChange: (v: NavLayout) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => {
        onChange(layout === "top" ? "side" : "top");
      }}
      title="Toggle navigation layout (top bar / sidebar)"
      aria-label={`Navigation layout: ${layout === "top" ? "top bar" : "sidebar"}. Click to change.`}
      className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent"
    >
      {layout === "top" ? "⬒ Top" : "◧ Side"}
    </button>
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
        orientation === "row"
          ? "flex flex-wrap gap-1"
          : "flex flex-col gap-1"
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
      <LayoutToggle layout={layout} onChange={setLayout} />
      <ThemeToggle />
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
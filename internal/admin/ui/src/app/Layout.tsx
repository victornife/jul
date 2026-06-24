import { Link, Outlet, useLocation } from "react-router-dom";
import { useTheme, type ThemePreference } from "@/lib/theme.ts";

const NAV = [
  { to: "/", label: "Overview", exact: true },
  { to: "/routes", label: "Routes" },
  { to: "/apps", label: "Apps" },
  { to: "/tls", label: "TLS" },
  { to: "/security", label: "Security" },
  { to: "/traffic", label: "Traffic" },
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
      className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
    >
      {THEME_LABEL[preference]}
    </button>
  );
}

export function Layout() {
  const loc = useLocation();
  return (
    <div className="flex h-screen flex-col bg-jul-bg text-jul-text">
      <header className="flex items-center gap-4 border-b border-jul-border bg-jul-surface px-6 py-3">
        <span className="font-bold tracking-wide text-jul-accent">Jul.IA</span>
        <span className="text-xs text-jul-muted">Console v2</span>
        <nav className="ml-auto flex flex-wrap gap-1">
          {NAV.map((n) => (
            <Link
              key={n.to}
              to={n.to}
              className={`rounded-md px-3 py-1 text-sm transition-colors ${
                isActive(n.to, loc.pathname, n.exact)
                  ? "bg-jul-accent text-jul-bg font-medium"
                  : "text-jul-muted hover:text-jul-text"
              }`}
            >
              {n.label}
            </Link>
          ))}
        </nav>
        <ThemeToggle />
      </header>
      <main className="flex-1 overflow-auto p-6">
        <Outlet />
      </main>
    </div>
  );
}

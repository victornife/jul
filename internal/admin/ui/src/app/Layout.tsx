import { Link, useLocation } from "react-router-dom";

const nav = [
  { to: "/", label: "Overview" },
];

export function Layout() {
  const loc = useLocation();
  return (
    <div className="flex h-screen flex-col bg-jul-bg text-jul-text">
      <header className="flex items-center gap-4 border-b border-jul-border bg-jul-surface px-6 py-3">
        <span className="font-bold tracking-wide text-jul-accent">Jul.IA</span>
        <span className="text-xs text-jul-muted">Console v2</span>
        <nav className="ml-auto flex gap-2">
          {nav.map((n) => (
            <Link
              key={n.to}
              to={n.to}
              className={`rounded-md px-3 py-1 text-sm transition-colors ${
                loc.pathname === n.to
                  ? "bg-jul-accent text-jul-bg"
                  : "text-jul-muted hover:text-jul-text"
              }`}
            >
              {n.label}
            </Link>
          ))}
        </nav>
      </header>
      <main className="flex-1 overflow-auto p-6">
        <Outlet />
      </main>
    </div>
  );
}

import { Outlet } from "react-router-dom";

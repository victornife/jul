import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { Providers } from "@/app/providers.tsx";
import { App } from "@/app/App.tsx";
import { AuthGate } from "@/app/AuthGate.tsx";
import { authToken } from "@/api/client.ts";
import { initThemeEarly } from "@/lib/theme.ts";
import { installErrorReporter } from "@/lib/errorReporter.ts";

// Apply the persisted theme before first paint to avoid a flash of the wrong
// palette (Milestone 4.1).
initThemeEarly();

// Install the global frontend error/slow-request reporter so uncaught
// exceptions and slow network calls are forwarded to the Console error sink
// (Milestone 5.7).
installErrorReporter();

// Bootstrap: the preferred way to authenticate is the in-app token prompt
// (AuthGate), which appears on the first 401 and keeps the token out of the
// URL. A ?token=<secret> query parameter is still accepted for local-dev
// convenience and shared bootstrap links, but it is discouraged: the token
// leaks into access logs, browser history, and the Referer header. When one is
// present we save it to sessionStorage, immediately prune it from the visible
// URL, and warn in the console.
const params = new URLSearchParams(window.location.search);
const tokenFromQuery = params.get("token");
if (tokenFromQuery) {
  authToken.set(tokenFromQuery);
  params.delete("token");
  const clean = params.toString()
    ? `${window.location.pathname}?${params.toString()}`
    : window.location.pathname;
  window.history.replaceState({}, "", clean);
  console.warn(
    "[jul] Authenticated via the ?token= URL parameter. This is discouraged outside local " +
      "development because the token leaks into access logs, browser history, and the Referer " +
      "header. Prefer the in-app token prompt instead.",
  );
}

const rootEl = document.getElementById("root");

if (!rootEl) throw new Error("#root not found in index.html");
createRoot(rootEl).render(
  <StrictMode>
    <Providers>
      <App />
      <AuthGate />
    </Providers>
  </StrictMode>,
);

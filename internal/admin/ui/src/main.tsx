import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { Providers } from "@/app/providers.tsx";
import { App } from "@/app/App.tsx";
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

// Bootstrap: if the operator visits the console via a shared link that
// includes the auth token in the query string (e.g. /?token=<secret>),
// save it to sessionStorage so subsequent API calls are authenticated,
// then prune the token from the visible URL so it is not left in history.
const params = new URLSearchParams(window.location.search);
const tokenFromQuery = params.get("token");
if (tokenFromQuery) {
  authToken.set(tokenFromQuery);
  params.delete("token");
  const clean = params.toString()
    ? `${window.location.pathname}?${params.toString()}`
    : window.location.pathname;
  window.history.replaceState({}, "", clean);
}

const rootEl = document.getElementById("root");

if (!rootEl) throw new Error("#root not found in index.html");
createRoot(rootEl).render(
  <StrictMode>
    <Providers>
      <App />
    </Providers>
  </StrictMode>
);

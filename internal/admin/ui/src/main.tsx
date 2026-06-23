import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { Providers } from "@/app/providers.tsx";
import { App } from "@/app/App.tsx";
import { authToken } from "@/api/client.ts";

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

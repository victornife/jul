/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { Providers } from "@/app/providers.tsx";
import { App } from "@/app/App.tsx";
import { AuthGate } from "@/app/AuthGate.tsx";
import { PermissionProvider } from "@/auth/PermissionProvider.tsx";
import { initThemeEarly } from "@/lib/theme.ts";
import { installErrorReporter } from "@/lib/errorReporter.ts";

// Apply the persisted theme before first paint to avoid a flash of the wrong
// palette (Milestone 4.1).
initThemeEarly();

// Install the global frontend error/slow-request reporter so uncaught
// exceptions and slow network calls are forwarded to the Console error sink
// (Milestone 5.7).
installErrorReporter();

const rootEl = document.getElementById("root");

if (!rootEl) throw new Error("#root not found in index.html");
createRoot(rootEl).render(
  <StrictMode>
    <Providers>
      <PermissionProvider>
        <App />
        <AuthGate />
      </PermissionProvider>
    </Providers>
  </StrictMode>,
);

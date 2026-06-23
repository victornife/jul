import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@/styles/globals.css";
import { Providers } from "@/app/providers.tsx";
import { App } from "@/app/App.tsx";

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("#root not found in index.html");

createRoot(rootEl).render(
  <StrictMode>
    <Providers>
      <App />
    </Providers>
  </StrictMode>
);

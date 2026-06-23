import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "/console/v2/",
  root: resolve(__dirname),
  build: {
    outDir: resolve(__dirname, "..", "assets", "dist"),
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (
            id.includes("node_modules/react") ||
            id.includes("node_modules/react-dom") ||
            id.includes("node_modules/react-router-dom")
          ) {
            return "vendor";
          }
          if (id.includes("node_modules/@tanstack/react-query")) {
            return "query";
          }
        },
      },
    },
  },
  resolve: {
    alias: {
      "@": resolve(__dirname, "src"),
    },
  },
});

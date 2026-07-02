/// <reference types="vitest/config" />
import { defineConfig, mergeConfig } from "vite";
import viteConfig from "./vite.config";

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      globals: true,
      include: ["src/**/*.{test,spec}.{ts,tsx}"],
      setupFiles: ["src/test/setup.ts"],
      coverage: {
        provider: "v8",
        reporter: ["text", "html"],
        include: ["src/**/*.{ts,tsx}"],
        exclude: ["src/test/**", "src/**/*.d.ts"],
        thresholds: {
          statements: 50,
          branches: 45,
          functions: 40,
          lines: 50,
        },
      },
    },
  })
);

/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

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
      // Node's built-in Web Storage API (stable since Node 24) shadows jsdom's
      // window.localStorage with a stub that throws without --localstorage-file,
      // while window.sessionStorage is unaffected. Disable it so jsdom's own
      // Storage implementation is used in tests.
      execArgv: ["--no-experimental-webstorage"],
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

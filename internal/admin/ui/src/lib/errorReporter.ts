/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { reportClientError } from "@/api/client.ts";

// SLOW_REQUEST_MS is the threshold above which a network request is considered
// slow and reported to the Console error sink (Milestone 5.7).
const SLOW_REQUEST_MS = 2000;

// reportedRecently de-duplicates identical messages within a short window so a
// repeating error (e.g. a render loop) cannot flood the backend sink.
const reportedRecently = new Map<string, number>();
const DEDUPE_WINDOW_MS = 10_000;

function shouldReport(key: string): boolean {
  const now = Date.now();
  const last = reportedRecently.get(key);
  if (last !== undefined && now - last < DEDUPE_WINDOW_MS) return false;
  reportedRecently.set(key, now);
  // Opportunistically prune so the map stays bounded.
  if (reportedRecently.size > 64) {
    for (const [k, t] of reportedRecently) {
      if (now - t > DEDUPE_WINDOW_MS) reportedRecently.delete(k);
    }
  }
  return true;
}

/**
 * installErrorReporter wires the global frontend instrumentation required by
 * Milestone 5.7: it captures uncaught exceptions, unhandled promise rejections,
 * and slow network requests and forwards a redacted summary to the Console's
 * bounded error sink. It returns a cleanup function. It is idempotent across a
 * single page so React StrictMode double-invocation does not double-install.
 */
export function installErrorReporter(): () => void {
  if (typeof window === "undefined") return () => undefined;

  const onError = (ev: ErrorEvent): void => {
    const msg = ev.message || "unknown error";
    if (!shouldReport(`err:${msg}`)) return;
    reportClientError({
      message: msg,
      source: ev.filename,
      line: ev.lineno,
      col: ev.colno,
    });
  };

  const onRejection = (ev: PromiseRejectionEvent): void => {
    const reason = ev.reason as unknown;
    let msg = "unhandled rejection";
    if (reason instanceof Error) msg = reason.message;
    else if (typeof reason === "string") msg = reason;
    if (!shouldReport(`rej:${msg}`)) return;
    reportClientError({ message: msg });
  };

  window.addEventListener("error", onError);
  window.addEventListener("unhandledrejection", onRejection);

  // Wrap fetch to flag slow requests. The original is restored on cleanup.
  const originalFetch = window.fetch.bind(window);
  const wrappedFetch: typeof window.fetch = async (input, init) => {
    const start = performance.now();
    try {
      return await originalFetch(input, init);
    } finally {
      const elapsed = performance.now() - start;
      if (elapsed > SLOW_REQUEST_MS) {
        const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
        // Never report the Console's own error-sink call as slow, to avoid loops.
        if (!url.includes("/api/admin/client-errors") && shouldReport(`slow:${url}`)) {
          reportClientError({
            message: `slow request (${String(Math.round(elapsed))}ms)`,
            source: url,
          });
        }
      }
    }
  };
  window.fetch = wrappedFetch;

  return () => {
    window.removeEventListener("error", onError);
    window.removeEventListener("unhandledrejection", onRejection);
    window.fetch = originalFetch;
  };
}

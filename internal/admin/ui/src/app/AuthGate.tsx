/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, useState } from "react";
import { authToken, UNAUTHORIZED_EVENT, fetchOIDCConfiguration, logoutOIDC } from "@/api/client.ts";
import { useFocusTrap } from "@/lib/useFocusTrap.ts";

/**
 * AuthGate surfaces a first-class token prompt when the admin API rejects the
 * current credential (P1-8). It listens for the UNAUTHORIZED_EVENT the API
 * client dispatches on any 401 and overlays a modal where the operator pastes
 * the admin bearer token, which is stored in sessionStorage for subsequent
 * Authorization headers. This replaces the ?token= query-string pattern, which
 * leaks the secret into access logs, browser history, and the Referer header.
 *
 * It renders nothing until a 401 occurs, so it adds no chrome in the common
 * authenticated case.
 */
export function AuthGate() {
  const [oidcAvailable, setOidcAvailable] = useState(false);
  const [provider, setProvider] = useState<string | null>(null);
  const [oidcError, setOidcError] = useState(false);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    let mounted = true;
    void fetchOIDCConfiguration()
      .then((data: { enabled?: boolean; provider_name?: string | undefined } | null) => {
        if (mounted && data !== null) setOidcAvailable(true);
        if (mounted && data?.enabled && typeof data.provider_name === "string")
          setProvider(data.provider_name);
      })
      .catch(() => {
        /* Local-token recovery remains available during outages. */
      });
    const url = new URL(window.location.href);
    if (url.searchParams.has("oidc_error")) {
      setOidcError(true);
      setOpen(true);
      url.searchParams.delete("oidc_error");
      window.history.replaceState(null, "", url.pathname + url.search + url.hash);
    }
    return () => {
      mounted = false;
    };
  }, []);
  const openRef = useRef(open);
  useEffect(() => {
    openRef.current = open;
  }, [open]);

  const [value, setValue] = useState("");
  const [saved, setSaved] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  useFocusTrap(dialogRef, open);

  useEffect(() => {
    function onUnauthorized(): void {
      if (openRef.current) return; // already open — don't reset what the user is typing
      setValue(authToken.get());
      setSaved(false);
      setOpen(true);
    }
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    };
  }, []);

  if (!open) return null;

  async function save(): Promise<void> {
    const t = value.trim();
    if (t === "") return;
    if (oidcAvailable) {
      try {
        const accepted = await logoutOIDC();
        if (!accepted) {
          setOidcError(true);
          return;
        }
      } catch {
        setOidcError(true);
        return;
      }
    }
    authToken.set(t);
    setSaved(true);
    // Reload so every panel re-runs its queries with the new token. This is the
    // simplest correct way to recover the whole app's data after re-auth.
    window.location.reload();
  }

  return (
    <div className="fixed inset-0 z-[60] flex items-start justify-center pt-[18vh]">
      <div className="absolute inset-0 bg-black/50" />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={provider ? "Console sign-in required" : "Admin token required"}
        className="relative z-10 w-full max-w-md space-y-4 rounded-lg border border-jul-border bg-jul-surface p-5 shadow-2xl outline-none"
      >
        <div className="space-y-1">
          <h2 className="text-lg font-semibold text-jul-text">
            {provider ? "Sign in to Console" : "Admin token required"}
          </h2>
          {provider && (
            <form
              method="post"
              action="/auth/oidc/login"
              onSubmit={() => {
                authToken.clear();
              }}
            >
              <p className="text-sm text-jul-muted">
                Your session is unavailable or expired. Sign in to continue.
              </p>
              <button type="submit" className="rounded-md bg-jul-accent px-4 py-2 text-jul-bg">
                Sign in with {provider}
              </button>
            </form>
          )}
          {oidcError && (
            <p role="alert">
              Sign-in could not be completed. The provider may be unavailable or your account may
              not have a Jul role. Try again or use a local recovery token.
            </p>
          )}
          <p className="text-sm text-jul-muted">
            The admin API rejected the request. Paste your admin bearer token to continue. It is
            stored only in this tab&apos;s session storage and sent in the Authorization header —
            never in the URL.
          </p>
        </div>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Token</span>
          <input
            type="password"
            value={value}
            autoFocus
            placeholder="paste the admin token"
            onChange={(e) => {
              setValue(e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") void save();
            }}
            className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
        </label>

        <p className="rounded-md border border-jul-border bg-jul-bg p-2 text-xs text-jul-muted">
          Use a named principal token with the required Jul role, or the configured bootstrap
          recovery token. A credential in a ?token= URL leaks into logs, history, and referrers.
        </p>

        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={() => {
              setOpen(false);
            }}
            className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text hover:bg-jul-bg"
          >
            Dismiss
          </button>
          <button
            type="button"
            onClick={() => {
              void save();
            }}
            disabled={value.trim() === "" || saved}
            className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {saved ? "Saving…" : "Save & retry"}
          </button>
        </div>
      </div>
    </div>
  );
}

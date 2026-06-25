import { useEffect, useState } from "react";
import { authToken, UNAUTHORIZED_EVENT } from "@/api/client.ts";

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
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    function onUnauthorized(): void {
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

  function save(): void {
    const t = value.trim();
    if (t === "") return;
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
        role="dialog"
        aria-modal="true"
        aria-label="Admin token required"
        className="relative z-10 w-full max-w-md space-y-4 rounded-lg border border-jul-border bg-jul-surface p-5 shadow-2xl"
      >
        <div className="space-y-1">
          <h2 className="text-lg font-semibold text-jul-text">Admin token required</h2>
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
              if (e.key === "Enter") save();
            }}
            className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
        </label>

        <p className="rounded-md border border-jul-border bg-jul-bg p-2 text-xs text-jul-muted">
          The token is the value of <span className="font-mono">admin.token</span> in your Jul
          configuration. Avoid the <span className="font-mono">?token=</span> URL parameter outside
          local development — it leaks into logs, history, and referrers.
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
            onClick={save}
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

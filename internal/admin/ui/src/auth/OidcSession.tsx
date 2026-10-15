/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { useState } from "react";
import { usePermission } from "@/auth/usePermission.ts";
import { authToken, logoutOIDC } from "@/api/client.ts";

/** Session metadata is server-owned; cookies and provider tokens are never read. */
export function OidcSession() {
  const { identity } = usePermission();
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  if (identity?.authentication !== "oidc") return null;
  async function logout(): Promise<void> {
    setBusy(true); setFailed(false);
    try {
      const accepted = await logoutOIDC();
      if (!accepted) throw new Error("logout failed");
      authToken.clear();
      window.location.reload();
    } catch { setFailed(true); setBusy(false); }
  }
  return <div className="flex items-center gap-2 text-xs">
    {identity.session_expires_at && <span title="Activity can extend idle expiry, never the absolute lifetime">Session expires {new Date(identity.session_expires_at).toLocaleTimeString()}</span>}
    <button type="button" disabled={busy} onClick={() => { void logout(); }}>{busy ? "Signing out…" : "Sign out"}</button>
    {failed && <span role="alert">Could not sign out. Try again.</span>}
  </div>;
}

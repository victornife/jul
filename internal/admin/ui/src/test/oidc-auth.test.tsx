/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl */
import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { AuthGate } from "@/app/AuthGate.tsx";
import { OidcSession } from "@/auth/OidcSession.tsx";
import { PermissionContext, type PermissionState } from "@/auth/usePermission.ts";
import { UNAUTHORIZED_EVENT } from "@/api/client.ts";
const identity = { principal: "alice", role: "viewer", permissions: ["status:read"], token_id: "oidc:public-fingerprint", legacy: false, authentication: "oidc", session_expires_at: "2026-10-15T12:00:00Z" };
const permission: PermissionState = { identity, ready: true, isLoading: false, has: () => false };
function unauthorized() { act(() => { window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT)); }); }
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); sessionStorage.clear(); window.history.replaceState(null, "", "/"); });
function enabled() { return new Response(JSON.stringify({ enabled: true, provider_name: "Test IdP" })); }
it("offers configured sign-in and clears local credentials before form submission", async () => {
 vi.stubGlobal("fetch", vi.fn().mockResolvedValue(enabled())); render(<AuthGate />); unauthorized();
 const button = await screen.findByRole("button", { name: "Sign in with Test IdP" });
 expect(screen.getByRole("dialog")).toHaveAccessibleName("Console sign-in required");
 sessionStorage.setItem("jul_admin_token", "local-token"); const form = button.closest("form"); if (!form) throw new Error("sign-in form missing");
 expect(form).toHaveAttribute("method", "post"); expect(form).toHaveAttribute("action", "/auth/oidc/login");
 fireEvent.submit(form); expect(sessionStorage.getItem("jul_admin_token")).toBeNull(); unauthorized();
});
it("consumes a generic callback error and preserves recovery", async () => {
 window.history.replaceState(null, "", "/?oidc_error=login_failed#config");
 vi.stubGlobal("fetch", vi.fn().mockResolvedValue(enabled())); render(<AuthGate />);
 expect(await screen.findByRole("alert")).toHaveTextContent(/provider may be unavailable/);
 expect(window.location.search).toBe(""); expect(window.location.hash).toBe("#config");
 expect(await screen.findByRole("button", { name: "Sign in with Test IdP" })).toBeVisible();
 expect(screen.getByLabelText("Token")).toHaveAttribute("type", "password");
});
it("retains recovery when status is unavailable", async () => {
 vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline"))); render(<AuthGate />); unauthorized();
 await waitFor(() => { expect(screen.getByRole("dialog")).toHaveAccessibleName("Admin token required"); });
 fireEvent.click(screen.getByRole("button", { name: "Dismiss" })); expect(screen.queryByRole("dialog")).toBeNull();
});
it.each(["refusal", "network", "success"])("handles %s during local-token switching", async (mode) => {
 const fetcher = vi.fn().mockResolvedValueOnce(enabled());
 if (mode === "network") fetcher.mockRejectedValue(new Error("offline"));
 else fetcher.mockResolvedValue(new Response(null, { status: mode === "success" ? 204 : 503 }));
 vi.stubGlobal("fetch", fetcher); render(<AuthGate />); unauthorized();
 await screen.findByRole("button", { name: "Sign in with Test IdP" });
 fireEvent.change(screen.getByLabelText("Token"), { target: { value: "recovery-token" } });
 fireEvent.keyDown(screen.getByLabelText("Token"), { key: "Enter" });
 if (mode === "success") await waitFor(() => { expect(sessionStorage.getItem("jul_admin_token")).toBe("recovery-token"); });
 else { expect(await screen.findByRole("alert")).toBeVisible(); expect(sessionStorage.getItem("jul_admin_token")).toBeNull(); }
 expect(fetcher).toHaveBeenLastCalledWith("/auth/oidc/logout", expect.objectContaining({ method: "POST", headers: { "X-Jul-Console": "1" } }));
});
it("hides session controls for local-token identities", () => {
 render(<PermissionContext.Provider value={{ ...permission, identity: { ...identity, authentication: undefined } }}><OidcSession /></PermissionContext.Provider>); expect(screen.queryByRole("button")).toBeNull();
});
it.each([204, 403])("handles logout HTTP %i without reading session cookies", async (status) => {
 const fetcher = vi.fn().mockResolvedValue(new Response(null, { status })); vi.stubGlobal("fetch", fetcher);
 render(<PermissionContext.Provider value={permission}><OidcSession /></PermissionContext.Provider>);
 expect(screen.getByText(/Session expires/)).toBeVisible(); fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
 await waitFor(() => { expect(fetcher).toHaveBeenCalledWith("/auth/oidc/logout", expect.objectContaining({ credentials: "same-origin", headers: { "X-Jul-Console": "1" } })); });
 if (status === 403) { expect(await screen.findByRole("alert")).toHaveTextContent(/Could not sign out/); expect(screen.getByRole("button", { name: "Sign out" })).toBeEnabled(); }
 else expect(screen.getByRole("button")).toBeDisabled();
});
it("clears stale cookies before token recovery after OIDC is disabled", async () => {
 const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ enabled: false }))).mockResolvedValue(new Response(null, { status: 204 }));
 vi.stubGlobal("fetch", fetcher); render(<AuthGate />); unauthorized();
 await waitFor(() => { expect(fetcher).toHaveBeenCalled(); });
 fireEvent.change(screen.getByLabelText("Token"), { target: { value: "recovery-token" } });
 fireEvent.click(screen.getByRole("button", { name: "Save & retry" }));
 await waitFor(() => { expect(sessionStorage.getItem("jul_admin_token")).toBe("recovery-token"); });
 expect(fetcher).toHaveBeenLastCalledWith("/auth/oidc/logout", expect.objectContaining({ method: "POST" }));
});

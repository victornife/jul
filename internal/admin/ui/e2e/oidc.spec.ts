/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl */
import { test, expect } from "@playwright/test";
test.beforeEach(async ({ request }) => {
  await request.post("http://127.0.0.1:9297/recover");
});
test("sign-in, RBAC, CSRF, secret-free storage, expiry and logout", async ({ page, context }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Sign in with OpenID Connect" }).click();
  await expect(page.getByText("alice", { exact: true })).toBeVisible();
  await expect(page.getByTitle("Signed in as alice (role: viewer)")).toBeVisible();
  await page.goto("/audit");
  await expect(page.getByRole("button", { name: "Export CSV", exact: true })).toBeDisabled();
  expect(page.url()).not.toMatch(/code=|state=|id_token|access_token/);
  const cookies = await context.cookies();
  const session = cookies.find((cookie) => cookie.name === "jul_oidc_dev");
  expect(session?.httpOnly).toBe(true);
  expect(session?.sameSite).toBe("Lax");
  expect(session?.path).toBe("/");
  if (!session) throw new Error("session cookie missing");
  const storage = await page.evaluate(() => ({
    local: { ...localStorage },
    session: { ...sessionStorage },
    visible: document.body.textContent,
    cookie: document.cookie,
  }));
  expect(JSON.stringify(storage)).not.toContain(session.value);
  expect(JSON.stringify(storage)).not.toMatch(/synthetic-not-for-Jul|id_token|code_verifier/);
  const csrf = await page.evaluate(
    async () => (await fetch("/cache/purge", { method: "POST" })).status,
  );
  expect(csrf).toBe(401);
  const bypass = await page.evaluate(
    async () =>
      (await fetch("/cache/purge", { method: "POST", headers: { "X-Jul-Console": "1" } })).status,
  );
  expect(bypass).toBe(403);
  // Re-authentication is explicit, never a provider redirect for API callers.
  // Absolute expiry also applies while background Console polling is active.
  await page.waitForTimeout(15_500);
  const expired = await page.evaluate(async () => {
    const response = await fetch("/api/admin/me");
    return { status: response.status, location: response.headers.get("Location") };
  });
  expect(expired).toEqual({ status: 401, location: null });
  await page.reload();
  await page.getByRole("button", { name: "Sign in with OpenID Connect" }).click();
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("button", { name: "Sign in with OpenID Connect" })).toBeVisible();
  expect((await context.cookies()).some((cookie) => cookie.name === "jul_oidc_dev")).toBe(false);
});
test("provider outage and rejected nonce recover without credential leakage", async ({
  page,
  request,
}) => {
  await request.post("http://127.0.0.1:9297/outage");
  await page.goto("/");
  await page.getByRole("button", { name: "Sign in with OpenID Connect" }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "Sign-in could not be completed" }),
  ).toBeVisible();
  await request.post("http://127.0.0.1:9297/nonce");
  await page.waitForTimeout(5200);
  await page.getByRole("button", { name: "Sign in with OpenID Connect" }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "Sign-in could not be completed" }),
  ).toBeVisible();
  expect(page.url()).not.toMatch(/code=|state=/);
});

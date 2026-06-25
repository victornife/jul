import { describe, it, expect } from "vitest";
import {
  authWarnings,
  emptyAuthDraft,
  generateRouteToml,
  type AuthDraft,
  type RouteDraft,
} from "@/lib/routeToml.ts";

function baseDraft(auth: AuthDraft): RouteDraft {
  return {
    listen: ":8080",
    serverNames: "",
    path: "/api",
    matchType: "prefix",
    action: "proxy",
    target: "http://127.0.0.1:3000",
    auth,
    cache: false,
    compression: false,
    rateLimit: false,
  };
}

describe("generateRouteToml auth", () => {
  it("emits no auth block for method none", () => {
    const toml = generateRouteToml(baseDraft(emptyAuthDraft()));
    expect(toml).not.toContain("auth");
  });

  it("never emits a bare inert auth = {}", () => {
    for (const method of ["none", "cidr", "basic", "jwt", "forward"] as const) {
      const toml = generateRouteToml(baseDraft({ ...emptyAuthDraft(), method }));
      expect(toml).not.toMatch(/auth\s*=\s*\{\s*\}/);
    }
  });

  it("emits CIDR allow/deny lists", () => {
    const toml = generateRouteToml(
      baseDraft({
        ...emptyAuthDraft(),
        method: "cidr",
        allow: "10.0.0.0/8, 192.168.1.0/24",
        deny: "203.0.113.0/24",
      }),
    );
    expect(toml).toContain('auth.allow = ["10.0.0.0/8", "192.168.1.0/24"]');
    expect(toml).toContain('auth.deny = ["203.0.113.0/24"]');
  });

  it("emits basic auth with file and optional realm", () => {
    const withRealm = generateRouteToml(
      baseDraft({
        ...emptyAuthDraft(),
        method: "basic",
        basicFile: "/etc/jul/htpasswd",
        basicRealm: "Ops",
      }),
    );
    expect(withRealm).toContain('auth.basic = { file = "/etc/jul/htpasswd", realm = "Ops" }');
    const noRealm = generateRouteToml(
      baseDraft({ ...emptyAuthDraft(), method: "basic", basicFile: "/etc/jul/htpasswd" }),
    );
    expect(noRealm).toContain('auth.basic = { file = "/etc/jul/htpasswd" }');
    expect(noRealm).not.toContain("realm");
  });

  it("emits jwt auth with jwks_url and optional issuer/audience", () => {
    const toml = generateRouteToml(
      baseDraft({
        ...emptyAuthDraft(),
        method: "jwt",
        jwtJwksUrl: "https://issuer.example/jwks",
        jwtIssuer: "https://issuer.example/",
        jwtAudience: "api://jul",
      }),
    );
    expect(toml).toContain(
      'auth.jwt = { jwks_url = "https://issuer.example/jwks", issuer = "https://issuer.example/", audience = "api://jul" }',
    );
  });

  it("emits forward-auth with url", () => {
    const toml = generateRouteToml(
      baseDraft({
        ...emptyAuthDraft(),
        method: "forward",
        forwardUrl: "http://127.0.0.1:4181/auth",
      }),
    );
    expect(toml).toContain('auth.forward_auth = { url = "http://127.0.0.1:4181/auth" }');
  });
});

describe("authWarnings", () => {
  it("has no warning for method none", () => {
    expect(authWarnings(emptyAuthDraft())).toHaveLength(0);
  });

  it("warns on a CIDR policy with neither allow nor deny", () => {
    expect(authWarnings({ ...emptyAuthDraft(), method: "cidr" }).length).toBeGreaterThan(0);
  });

  it("accepts a CIDR policy with an allow list", () => {
    expect(authWarnings({ ...emptyAuthDraft(), method: "cidr", allow: "10.0.0.0/8" })).toHaveLength(
      0,
    );
  });

  it("warns when basic has no file", () => {
    expect(authWarnings({ ...emptyAuthDraft(), method: "basic" }).length).toBeGreaterThan(0);
  });

  it("warns when jwt jwks_url is not https", () => {
    expect(
      authWarnings({ ...emptyAuthDraft(), method: "jwt", jwtJwksUrl: "http://issuer/jwks" }).length,
    ).toBeGreaterThan(0);
  });

  it("accepts an https jwks_url", () => {
    expect(
      authWarnings({ ...emptyAuthDraft(), method: "jwt", jwtJwksUrl: "https://issuer/jwks" }),
    ).toHaveLength(0);
  });

  it("warns when forward-auth url is missing or non-http", () => {
    expect(authWarnings({ ...emptyAuthDraft(), method: "forward" }).length).toBeGreaterThan(0);
    expect(
      authWarnings({ ...emptyAuthDraft(), method: "forward", forwardUrl: "ftp://x" }).length,
    ).toBeGreaterThan(0);
  });
});

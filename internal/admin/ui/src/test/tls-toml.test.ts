import { describe, it, expect } from "vitest";
import {
  caForEnvironment,
  emptyTLSDraft,
  generateTLSToml,
  tlsWarnings,
  type TLSDraft,
} from "@/lib/tlsToml.ts";

function draft(over: Partial<TLSDraft>): TLSDraft {
  return { ...emptyTLSDraft(), ...over };
}

describe("caForEnvironment", () => {
  it("maps staging to the staging directory (safe default)", () => {
    expect(caForEnvironment("staging")).toBe("letsencrypt-staging");
  });
  it("maps production to the production directory", () => {
    expect(caForEnvironment("production")).toBe("letsencrypt");
  });
});

describe("generateTLSToml ACME", () => {
  it("emits an ACME block with staging CA by default", () => {
    const toml = generateTLSToml(
      draft({
        serverNames: "example.com, www.example.com",
        mode: "acme",
        acmeEmail: "ops@example.com",
        target: "http://app",
      }),
    );
    expect(toml).toContain("[servers.tls]");
    expect(toml).toContain("enabled = true");
    expect(toml).toContain("[servers.tls.acme]");
    expect(toml).toContain('email = "ops@example.com"');
    expect(toml).toContain('ca = "letsencrypt-staging"');
    expect(toml).toContain('challenge = "http-01"');
    expect(toml).toContain('server_names = ["example.com", "www.example.com"]');
    expect(toml).toContain('proxy_pass = "http://app"');
    // ACME and static cert/key are mutually exclusive: never emit cert/key here.
    expect(toml).not.toContain("cert =");
    expect(toml).not.toContain("key =");
  });

  it("uses the production CA when the environment is production", () => {
    const toml = generateTLSToml(
      draft({
        serverNames: "example.com",
        mode: "acme",
        acmeEmail: "ops@example.com",
        acmeEnvironment: "production",
        target: "http://app",
      }),
    );
    expect(toml).toContain('ca = "letsencrypt"');
  });

  it("emits explicit ACME domains when provided", () => {
    const toml = generateTLSToml(
      draft({
        serverNames: "example.com",
        mode: "acme",
        acmeEmail: "ops@example.com",
        acmeDomains: "a.example.com, b.example.com",
        target: "http://app",
      }),
    );
    expect(toml).toContain('domains = ["a.example.com", "b.example.com"]');
  });
});

describe("generateTLSToml static", () => {
  it("emits cert/key and no ACME block in static mode", () => {
    const toml = generateTLSToml(
      draft({
        serverNames: "example.com",
        mode: "static",
        certFile: "/etc/tls/cert.pem",
        keyFile: "/etc/tls/key.pem",
        minVersion: "1.3",
        action: "static",
        target: "/var/www",
      }),
    );
    expect(toml).toContain('cert = "/etc/tls/cert.pem"');
    expect(toml).toContain('key = "/etc/tls/key.pem"');
    expect(toml).toContain('min_version = "1.3"');
    expect(toml).toContain('root = "/var/www"');
    expect(toml).not.toContain("[servers.tls.acme]");
  });
});

describe("tlsWarnings", () => {
  it("warns when ACME lacks an email", () => {
    expect(
      tlsWarnings(draft({ mode: "acme", serverNames: "x.test", target: "http://a" })).some((w) =>
        w.includes("email"),
      ),
    ).toBe(true);
  });

  it("warns when ACME has no domain at all", () => {
    expect(
      tlsWarnings(
        draft({ mode: "acme", acmeEmail: "ops@x.test", serverNames: "", target: "http://a" }),
      ).some((w) => w.includes("domain")),
    ).toBe(true);
  });

  it("warns about production rate limits", () => {
    expect(
      tlsWarnings(
        draft({
          mode: "acme",
          acmeEmail: "ops@x.test",
          serverNames: "x.test",
          acmeEnvironment: "production",
          target: "http://a",
        }),
      ).some((w) => w.toLowerCase().includes("rate limit")),
    ).toBe(true);
  });

  it("warns when static mode is missing cert or key", () => {
    const w = tlsWarnings(draft({ mode: "static", serverNames: "x.test", target: "http://a" }));
    expect(w.some((x) => x.includes("certificate"))).toBe(true);
    expect(w.some((x) => x.includes("private-key"))).toBe(true);
  });

  it("has no warnings for a complete static draft", () => {
    expect(
      tlsWarnings(
        draft({
          mode: "static",
          certFile: "/c.pem",
          keyFile: "/k.pem",
          serverNames: "x.test",
          action: "proxy",
          target: "http://app",
        }),
      ),
    ).toHaveLength(0);
  });
});

/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

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

describe("generateTLSToml mutual TLS", () => {
  it("omits client_auth when mode is none", () => {
    const toml = generateTLSToml(
      draft({ serverNames: "x.test", mode: "acme", acmeEmail: "o@x.test", target: "http://app" }),
    );
    expect(toml).not.toContain("[servers.tls.client_auth]");
    expect(toml).not.toContain("require_client_cert");
  });

  it("emits a client_auth block with ca_file, crl, and verify_san", () => {
    const toml = generateTLSToml(
      draft({
        serverNames: "x.test",
        mode: "static",
        certFile: "/c.pem",
        keyFile: "/k.pem",
        clientAuthMode: "require",
        clientCAFile: "/etc/jul/clients-ca.pem",
        clientCRLFile: "/etc/jul/clients.crl",
        clientVerifySAN: "svc-a.internal, svc-b.internal",
        requireClientCert: true,
        target: "http://app",
      }),
    );
    expect(toml).toContain("[servers.tls.client_auth]");
    expect(toml).toContain('mode = "require"');
    expect(toml).toContain('ca_file = "/etc/jul/clients-ca.pem"');
    expect(toml).toContain('crl_file = "/etc/jul/clients.crl"');
    expect(toml).toContain('verify_san = ["svc-a.internal", "svc-b.internal"]');
    expect(toml).toContain("require_client_cert = true");
  });

  it("keeps [servers.tls] bare keys (cert/key) before the sub-tables", () => {
    // TOML binds bare keys to the most recently opened table, so cert/key must
    // be emitted before [servers.tls.client_auth] / [servers.tls.acme].
    const toml = generateTLSToml(
      draft({
        serverNames: "x.test",
        mode: "static",
        certFile: "/c.pem",
        keyFile: "/k.pem",
        clientAuthMode: "request",
        clientCAFile: "/ca.pem",
        target: "http://app",
      }),
    );
    const certIdx = toml.indexOf("cert =");
    const keyIdx = toml.indexOf("key =");
    const clientAuthIdx = toml.indexOf("[servers.tls.client_auth]");
    expect(certIdx).toBeGreaterThan(-1);
    expect(keyIdx).toBeGreaterThan(-1);
    expect(clientAuthIdx).toBeGreaterThan(-1);
    expect(certIdx).toBeLessThan(clientAuthIdx);
    expect(keyIdx).toBeLessThan(clientAuthIdx);
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

  it("warns when mutual TLS lacks a client-CA bundle", () => {
    expect(
      tlsWarnings(
        draft({
          mode: "static",
          certFile: "/c.pem",
          keyFile: "/k.pem",
          serverNames: "x.test",
          target: "http://app",
          clientAuthMode: "require",
        }),
      ).some((w) => w.includes("client-CA")),
    ).toBe(true);
  });

  it("warns when require-client-cert is set without mutual TLS", () => {
    expect(
      tlsWarnings(
        draft({
          mode: "static",
          certFile: "/c.pem",
          keyFile: "/k.pem",
          serverNames: "x.test",
          target: "http://app",
          requireClientCert: true,
        }),
      ).some((w) => w.includes("Require client certificate")),
    ).toBe(true);
  });
});

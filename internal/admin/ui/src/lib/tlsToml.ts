/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

// Client-side generator for the guided TLS/ACME editor (Wave A, Milestone 2.x).
// Like the route and app editors it emits a complete [[servers]] block that is
// appended to the running configuration and routed through the authoritative
// Validate → Diff → Apply → Rollback pipeline; it never writes directly.
//
// TLS is the most dangerous surface to hand-edit (a wrong ACME directory burns
// Let's Encrypt rate limits; a missing cert/key fails the bind), so the editor
// makes the high-risk choices explicit: static cert/key vs automatic ACME, and
// for ACME the staging-vs-production environment is a first-class toggle that
// maps to the safe `letsencrypt-staging` default.

export type TLSMode = "static" | "acme";
export type ACMEEnvironment = "staging" | "production";
export type ACMEChallenge = "http-01" | "tls-alpn-01" | "dns-01";
export type TLSMinVersion = "" | "1.2" | "1.3";
export type TLSRouteAction = "static" | "proxy";
// Mutual-TLS client-auth mode. "none" omits the client_auth block; "request"
// verifies a presented client cert but still allows anonymous clients; "require"
// rejects any connection without a CA-verified client certificate.
export type ClientAuthMode = "none" | "request" | "require";

export interface TLSDraft {
  listen: string;
  serverNames: string; // comma-separated; required for ACME
  mode: TLSMode;
  minVersion: TLSMinVersion;

  // static mode
  certFile: string;
  keyFile: string;

  // acme mode
  acmeEmail: string;
  acmeEnvironment: ACMEEnvironment;
  acmeChallenge: ACMEChallenge;
  acmeDomains: string; // optional; defaults to server_names
  dnsServer: string;
  dnsObservers: string;
  dnsZone: string;
  dnsTSIGKeyName: string;
  dnsTSIGSecretRef: string;

  // mutual TLS (client certificates). clientAuthMode "none" disables it.
  clientAuthMode: ClientAuthMode;
  clientCAFile: string; // CA bundle that signs accepted client certs
  clientCRLFile: string; // optional revocation list
  clientVerifySAN: string; // optional comma-separated allowed client SANs

  // a TLS server block needs something to serve; generate one location so the
  // result is a valid, useful block rather than a bare listener.
  action: TLSRouteAction;
  path: string;
  target: string;
  // requireClientCert sets require_client_cert on the location, rejecting
  // requests without a verified client certificate (independent of the
  // listener mode "request" which only verifies when one is presented).
  requireClientCert: boolean;
}

export function emptyTLSDraft(): TLSDraft {
  return {
    listen: ":443",
    serverNames: "",
    mode: "acme",
    minVersion: "",
    certFile: "",
    keyFile: "",
    acmeEmail: "",
    acmeEnvironment: "staging",
    acmeChallenge: "http-01",
    acmeDomains: "",
    dnsServer: "",
    dnsObservers: "",
    dnsZone: "",
    dnsTSIGKeyName: "",
    dnsTSIGSecretRef: "",
    clientAuthMode: "none",
    clientCAFile: "",
    clientCRLFile: "",
    clientVerifySAN: "",
    action: "proxy",
    path: "/",
    target: "",
    requireClientCert: false,
  };
}

function tomlString(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlStringArray(items: string[]): string {
  return `[${items.map((i) => tomlString(i)).join(", ")}]`;
}

function splitCsv(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

function validSecretReference(value: string): boolean {
  const match = /^\$\{(?:env|file|secret):([^}]*)\}$/.exec(value.trim());
  return (match?.[1] ?? "").trim().length > 0;
}

// caForEnvironment maps the staging/production toggle to the ACME directory
// name the server understands. Staging is the default everywhere so a misconfig
// never consumes production issuance quota.
export function caForEnvironment(env: ACMEEnvironment): string {
  return env === "production" ? "letsencrypt" : "letsencrypt-staging";
}

// tlsWarnings reports human-readable problems before the operator opens the
// diff. The server validates authoritatively; these are near-side hints.
export function tlsWarnings(d: TLSDraft): string[] {
  const warn: string[] = [];
  const names = splitCsv(d.serverNames);
  if (d.mode === "acme") {
    if (!d.acmeEmail.trim()) warn.push("ACME needs an account email address.");
    if (names.length === 0 && splitCsv(d.acmeDomains).length === 0) {
      warn.push("ACME needs at least one domain: set host names or ACME domains.");
    }
    if (d.acmeEnvironment === "production") {
      warn.push(
        "Production Let's Encrypt has strict rate limits. Verify issuance against staging first.",
      );
    }
    if (d.acmeChallenge === "dns-01") {
      if (!d.dnsServer.trim() || !d.dnsZone.trim() || !d.dnsTSIGKeyName.trim()) {
        warn.push("DNS-01 needs an authoritative server, zone and TSIG key name.");
      }
      if (splitCsv(d.dnsObservers).length === 0 || splitCsv(d.dnsObservers).length > 8) {
        warn.push("DNS-01 needs 1–8 CA-facing authoritative DNS observers.");
      }
      if (!validSecretReference(d.dnsTSIGSecretRef)) {
        warn.push("DNS-01 needs a single secret reference; never paste the TSIG secret itself.");
      }
    }
  } else {
    if (!d.certFile.trim()) warn.push("Static TLS needs a certificate file path.");
    if (!d.keyFile.trim()) warn.push("Static TLS needs a private-key file path.");
  }
  if (d.clientAuthMode !== "none" && !d.clientCAFile.trim()) {
    warn.push("Mutual TLS needs a client-CA bundle file to verify client certificates.");
  }
  if (d.requireClientCert && d.clientAuthMode === "none") {
    warn.push("“Require client certificate” needs mutual TLS enabled (request or require).");
  }
  if (d.action === "static" && !d.target.trim()) {
    warn.push("Static file serving needs a root directory.");
  }
  if (d.action === "proxy" && !d.target.trim()) {
    warn.push("Reverse proxy needs an upstream target.");
  }
  return warn;
}

/** Generates a complete TLS-enabled [[servers]] block for a new secure site. */
export function generateTLSToml(d: TLSDraft): string {
  const lines: string[] = [];
  lines.push("[[servers]]");
  lines.push(`listen = ${tomlString(d.listen.trim() || ":443")}`);
  const names = splitCsv(d.serverNames);
  if (names.length > 0) {
    lines.push(`server_names = ${tomlStringArray(names)}`);
  }
  lines.push("");
  lines.push("  [servers.tls]");
  lines.push("  enabled = true");
  if (d.minVersion) {
    lines.push(`  min_version = ${tomlString(d.minVersion)}`);
  }
  // Emit ALL bare keys of [servers.tls] (enabled, min_version, cert, key)
  // BEFORE any sub-table header. Once a sub-table like [servers.tls.client_auth]
  // or [servers.tls.acme] is opened, subsequent bare keys would bind to that
  // sub-table instead of [servers.tls], producing invalid configuration.
  if (d.mode === "static") {
    lines.push(`  cert = ${tomlString(d.certFile.trim())}`);
    lines.push(`  key = ${tomlString(d.keyFile.trim())}`);
  }
  if (d.clientAuthMode !== "none") {
    lines.push("");
    lines.push("    [servers.tls.client_auth]");
    lines.push(`    mode = ${tomlString(d.clientAuthMode)}`);
    lines.push(`    ca_file = ${tomlString(d.clientCAFile.trim())}`);
    if (d.clientCRLFile.trim()) {
      lines.push(`    crl_file = ${tomlString(d.clientCRLFile.trim())}`);
    }
    const sans = splitCsv(d.clientVerifySAN);
    if (sans.length > 0) {
      lines.push(`    verify_san = ${tomlStringArray(sans)}`);
    }
  }
  if (d.mode === "acme") {
    lines.push("");
    lines.push("    [servers.tls.acme]");
    lines.push("    enabled = true");
    lines.push(`    email = ${tomlString(d.acmeEmail.trim())}`);
    lines.push(`    ca = ${tomlString(caForEnvironment(d.acmeEnvironment))}`);
    lines.push(`    challenge = ${tomlString(d.acmeChallenge)}`);
    const domains = splitCsv(d.acmeDomains);
    if (domains.length > 0) {
      lines.push(`    domains = ${tomlStringArray(domains)}`);
    }
    if (d.acmeChallenge === "dns-01") {
      lines.push('    dns_provider = "rfc2136"');
      lines.push("");
      lines.push("      [servers.tls.acme.rfc2136]");
      lines.push(`      server = ${tomlString(d.dnsServer.trim())}`);
      lines.push(`      observers = ${tomlStringArray(splitCsv(d.dnsObservers))}`);
      lines.push(`      zone = ${tomlString(d.dnsZone.trim())}`);
      lines.push(`      tsig_key_name = ${tomlString(d.dnsTSIGKeyName.trim())}`);
      lines.push(
        `      tsig_secret = ${tomlString(validSecretReference(d.dnsTSIGSecretRef) ? d.dnsTSIGSecretRef.trim() : "")}`,
      );
    }
  }
  lines.push("");
  lines.push("  [[servers.locations]]");
  lines.push(`  match = { type = "prefix", path = ${tomlString(d.path.trim() || "/")} }`);
  if (d.action === "static") {
    lines.push(`  root = ${tomlString(d.target.trim())}`);
  } else {
    lines.push(`  proxy_pass = ${tomlString(d.target.trim())}`);
  }
  if (d.requireClientCert) {
    lines.push("  require_client_cert = true");
  }
  return lines.join("\n");
}

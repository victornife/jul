import { describe, it, expect } from "vitest";
import {
  looksLikeSecretLiteral,
  secretRefWarnings,
  secretReference,
  suggestEnvName,
} from "@/lib/secretsRef.ts";

describe("suggestEnvName", () => {
  it("converts a label to a namespaced screaming-snake name", () => {
    expect(suggestEnvName("admin token")).toBe("JUL_ADMIN_TOKEN");
    expect(suggestEnvName("Consul ACL Token")).toBe("JUL_CONSUL_ACL_TOKEN");
  });
  it("does not double the JUL_ prefix", () => {
    expect(suggestEnvName("JUL_ADMIN_TOKEN")).toBe("JUL_ADMIN_TOKEN");
  });
  it("falls back for empty input", () => {
    expect(suggestEnvName("   ")).toBe("JUL_SECRET");
  });
});

describe("secretReference", () => {
  it("builds an env reference", () => {
    expect(secretReference("env", "JUL_ADMIN_TOKEN")).toBe("${env:JUL_ADMIN_TOKEN}");
  });
  it("builds a file reference", () => {
    expect(secretReference("file", "/run/secrets/admin-token")).toBe(
      "${file:/run/secrets/admin-token}",
    );
  });
  it("trims whitespace around the target", () => {
    expect(secretReference("env", "  X  ")).toBe("${env:X}");
  });
});

describe("secretRefWarnings", () => {
  it("requires a target", () => {
    expect(secretRefWarnings("env", "").length).toBeGreaterThan(0);
    expect(secretRefWarnings("file", "  ").length).toBeGreaterThan(0);
  });
  it("rejects malformed env names", () => {
    expect(secretRefWarnings("env", "1BAD").length).toBeGreaterThan(0);
    expect(secretRefWarnings("env", "has space").length).toBeGreaterThan(0);
  });
  it("accepts a valid env name", () => {
    expect(secretRefWarnings("env", "JUL_TOKEN")).toHaveLength(0);
  });
  it("warns about relative file paths", () => {
    expect(secretRefWarnings("file", "secrets/token").length).toBeGreaterThan(0);
  });
  it("accepts an absolute file path", () => {
    expect(secretRefWarnings("file", "/run/secrets/token")).toHaveLength(0);
  });
});

describe("looksLikeSecretLiteral", () => {
  it("flags long or mixed-class values", () => {
    expect(looksLikeSecretLiteral("S3cretValue123")).toBe(true);
    expect(looksLikeSecretLiteral("abcdefghijklmnopqrst")).toBe(true);
  });
  it("does not flag short placeholders", () => {
    expect(looksLikeSecretLiteral("x")).toBe(false);
    expect(looksLikeSecretLiteral("TODO")).toBe(false);
  });
});

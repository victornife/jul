/**
 * Tests for the load-failure taxonomy (describeApiError). Each HTTP status and
 * the network/unknown fallbacks must map to a distinct, actionable description
 * so panels stop rendering an identical "Failed to load X" for every cause.
 */
import { describe, it, expect } from "vitest";
import { ApiError, describeApiError } from "@/api/client.ts";

describe("describeApiError", () => {
  it("maps 401 to a non-retryable re-auth prompt", () => {
    const d = describeApiError(new ApiError("/api/routes", 401, "unauthorized"), "routes");
    expect(d.kind).toBe("unauthorized");
    expect(d.status).toBe(401);
    expect(d.retryable).toBe(false);
    expect(d.title).toBe("Session expired");
    expect(d.message).toContain("Re-enter");
    expect(d.message).toContain("routes");
  });

  it("maps 403 to a non-retryable permission error", () => {
    const d = describeApiError(new ApiError("/api/security", 403, "forbidden"), "security info");
    expect(d.kind).toBe("forbidden");
    expect(d.retryable).toBe(false);
    expect(d.message).toContain("not permitted");
    expect(d.message).toContain("security info");
  });

  it("maps 404 to an availability/feature-disabled message", () => {
    const d = describeApiError(new ApiError("/api/streams", 404, "not found"), "streams");
    expect(d.kind).toBe("notFound");
    expect(d.retryable).toBe(false);
    expect(d.message).toContain("disabled in this build");
  });

  it("maps 409 to a retryable conflict and prefers the server message", () => {
    const d = describeApiError(
      new ApiError("/api/config", 409, "version feedface is stale"),
      "the configuration",
    );
    expect(d.kind).toBe("conflict");
    expect(d.retryable).toBe(true);
    expect(d.message).toBe("version feedface is stale");
  });

  it("falls back to a generic conflict message when the server gave none", () => {
    const d = describeApiError(new ApiError("/api/config", 409, ""), "the configuration");
    expect(d.kind).toBe("conflict");
    expect(d.message).toContain("changed on the server");
  });

  it("maps 429 to a retryable rate-limit message", () => {
    const d = describeApiError(new ApiError("/api/overview", 429, "slow down"), "the overview");
    expect(d.kind).toBe("rateLimited");
    expect(d.retryable).toBe(true);
    expect(d.message).toContain("rate-limited");
  });

  it("maps 5xx to a retryable server error and includes the detail", () => {
    const d = describeApiError(new ApiError("/api/tls", 503, "backend down"), "TLS info");
    expect(d.kind).toBe("server");
    expect(d.status).toBe(503);
    expect(d.retryable).toBe(true);
    expect(d.message).toContain("TLS info");
    expect(d.message).toContain("backend down");
  });

  it("omits the detail suffix when a 5xx carried no message", () => {
    const d = describeApiError(new ApiError("/api/tls", 500, ""), "TLS info");
    expect(d.kind).toBe("server");
    expect(d.message).toBe("The server failed while loading TLS info.");
  });

  it("maps an unexpected 4xx to the unknown bucket but keeps the status", () => {
    const d = describeApiError(new ApiError("/api/x", 418, "teapot"), "routes");
    expect(d.kind).toBe("unknown");
    expect(d.status).toBe(418);
    expect(d.message).toBe("teapot");
  });

  it("maps a fetch TypeError to a network error with no status", () => {
    const d = describeApiError(new TypeError("Failed to fetch"), "routes");
    expect(d.kind).toBe("network");
    expect(d.status).toBeUndefined();
    expect(d.retryable).toBe(true);
    expect(d.message).toContain("couldn't reach Jul");
  });

  it("maps a generic Error to the unknown bucket using its message", () => {
    const d = describeApiError(new Error("boom"), "routes");
    expect(d.kind).toBe("unknown");
    expect(d.message).toBe("boom");
  });

  it("maps a non-Error throw to the unknown bucket with a resource fallback", () => {
    const d = describeApiError("weird", "routes");
    expect(d.kind).toBe("unknown");
    expect(d.message).toBe("Could not load routes.");
  });
});

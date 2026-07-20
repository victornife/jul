/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Tests for the config-apply outcome taxonomy (AUX-02): the pure
 * deriveApplyOutcome projection and the ApplyOutcomeBanner that renders it.
 * Every acceptance-criteria branch — full-live, restart-required, partial
 * subsystem failure, and the specific stream partial-reload-failed case — has a
 * dedicated assertion so a regression in the outcome wording or severity is
 * caught.
 */
import { describe, it, expect, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import {
  deriveApplyOutcome,
  streamReloadFailure,
  reloadSubsystemFailures,
  type ApplyOutcome,
} from "@/lib/applyOutcome.ts";
import { ApplyOutcomeBanner } from "@/features/config/ApplyOutcomeBanner.tsx";
import type { ReloadResult } from "@/api/client.ts";

function reload(over: Partial<ReloadResult> = {}): ReloadResult {
  return {
    id: "rl_1",
    source: "admin",
    outcome: "applied_live",
    desired_version: "v1",
    serving_version: "v1",
    started_at: new Date().toISOString(),
    completed_at: new Date().toISOString(),
    duration_ms: 12,
    persisted: true,
    published: true,
    timed_out: false,
    http: { status: "ok" },
    stream: { status: "ok" },
    admin: { status: "ok" },
    ...over,
  };
}

afterEach(() => {
  cleanup();
});

describe("streamReloadFailure", () => {
  it("returns null for an absent, empty, or ok stream status", () => {
    expect(streamReloadFailure(undefined)).toBeNull();
    expect(streamReloadFailure("")).toBeNull();
    expect(streamReloadFailure("ok")).toBeNull();
  });

  it("parses a failed status into a subsystem failure with its reason", () => {
    const f = streamReloadFailure("failed: bind tcp/5432: address in use");
    expect(f).not.toBeNull();
    expect(f?.name).toBe("L4 stream proxy");
    expect(f?.detail).toBe("bind tcp/5432: address in use");
  });

  it("omits the detail when the reason is empty", () => {
    const f = streamReloadFailure("failed:");
    expect(f?.name).toBe("L4 stream proxy");
    expect(f?.detail).toBeUndefined();
  });
});

describe("deriveApplyOutcome", () => {
  it("full-live: accepted, runtime observed, stream ok", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: true,
      streamStatus: "ok",
    });
    expect(o.kind).toBe("full-live");
    expect(o.severity).toBe("success");
    expect(o.blocking).toBe(false);
    expect(o.failures).toHaveLength(0);
  });

  it("full-live: no streams configured (empty status)", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: true,
      streamStatus: "",
    });
    expect(o.kind).toBe("full-live");
  });

  it("reload-pending: accepted but runtime not observed yet", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: false,
    });
    expect(o.kind).toBe("reload-pending");
    expect(o.severity).toBe("info");
    expect(o.blocking).toBe(false);
  });

  it("partial-reload: accepted but the stream subsystem failed to activate", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: true,
      streamStatus: "failed: bind tcp/5432: address in use",
    });
    expect(o.kind).toBe("partial-reload");
    expect(o.severity).toBe("warning");
    expect(o.blocking).toBe(false);
    expect(o.failures).toHaveLength(1);
    expect(o.failures[0]?.name).toBe("L4 stream proxy");
    expect(o.failures[0]?.detail).toContain("address in use");
  });

  it("partial-reload outranks a still-pending reload", () => {
    // A subsystem failure must not be masked by the in-flight reload state.
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: true,
      streamStatus: "failed: bad config",
    });
    expect(o.kind).toBe("partial-reload");
  });

  it("restart-required: not accepted, uses the server message", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: false,
      restartMessage: "Changing the ACME domain set requires a restart.",
    });
    expect(o.kind).toBe("restart-required");
    expect(o.severity).toBe("blocked");
    expect(o.blocking).toBe(true);
    expect(o.message).toBe("Changing the ACME domain set requires a restart.");
  });

  it("restart-required: falls back to a default message when none is given", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: false,
    });
    expect(o.kind).toBe("restart-required");
    expect(o.message).toMatch(/restart the server/i);
  });

  it("reload-timed-out: accepted but reload exceeded timeout", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: true,
      streamStatus: "ok",
      reload: reload({ outcome: "applied_degraded", timed_out: true }),
    });
    expect(o.kind).toBe("reload-timed-out");
    expect(o.severity).toBe("warning");
    expect(o.blocking).toBe(false);
    expect(o.failures).toHaveLength(0);
    expect(o.message).toMatch(/reload_timeout/i);
  });

  it("reload-timed-out outranks partial-reload and reload-pending", () => {
    // A timed-out reload takes precedence over stream failure and pending state.
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      streamStatus: "failed: bind error",
      reload: reload({ outcome: "applied_degraded", timed_out: true }),
    });
    expect(o.kind).toBe("reload-timed-out");
  });

  it("reports partial-reload from applied_degraded reload outcome", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: true,
      reload: reload({
        outcome: "applied_degraded",
        http: { status: "ok" },
        admin: { status: "failed", error: "admin listener reload failed" },
      }),
    });
    expect(o.kind).toBe("partial-reload");
    expect(o.failures).toHaveLength(1);
    expect(o.failures[0]?.name).toBe("Admin subsystem");
  });

  it("reports restored when the candidate was rolled back", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      restored: true,
    });
    expect(o.kind).toBe("restored");
    expect(o.severity).toBe("warning");
  });

  it("reports restoration-failed when rollback failed", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      restoreError: "permission denied",
    });
    expect(o.kind).toBe("restoration-failed");
    expect(o.blocking).toBe(true);
  });

  it("reports reload-pending from saved_not_live outcome", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      reload: reload({ outcome: "saved_not_live", persisted: true }),
    });
    expect(o.kind).toBe("reload-pending");
  });
});

describe("reloadSubsystemFailures", () => {
  it("collects failed and timed-out subsystems", () => {
    const failures = reloadSubsystemFailures(
      reload({
        http: { status: "ok" },
        stream: { status: "failed", error: "bind error" },
        admin: { status: "timed_out", error: "deadline exceeded" },
      }),
    );
    expect(failures).toHaveLength(2);
    expect(failures.map((f) => f.name)).toContain("L4 stream proxy");
    expect(failures.map((f) => f.name)).toContain("Admin subsystem");
  });
});

describe("ApplyOutcomeBanner", () => {
  function bannerFor(input: Parameters<typeof deriveApplyOutcome>[0]): ApplyOutcome {
    return deriveApplyOutcome(input);
  }

  it("renders a full-live apply as polite status", () => {
    render(
      <ApplyOutcomeBanner
        outcome={bannerFor({ accepted: true, pendingReload: true, runtimeObserved: true, streamStatus: "ok" })}
      />,
    );
    const el = screen.getByRole("status");
    expect(el).toHaveAttribute("data-outcome", "full-live");
    expect(el).toHaveTextContent("Applied and live");
  });

  it("renders a partial-reload as an assertive alert listing the failed subsystem", () => {
    render(
      <ApplyOutcomeBanner
        outcome={bannerFor({
          accepted: true,
          pendingReload: true,
          runtimeObserved: true,
          streamStatus: "failed: bind tcp/5432: address in use",
        })}
      />,
    );
    const el = screen.getByRole("alert");
    expect(el).toHaveAttribute("data-outcome", "partial-reload");
    expect(el).toHaveTextContent("L4 stream proxy");
    expect(el).toHaveTextContent("address in use");
  });

  it("renders a restart-required apply as a blocking alert", () => {
    render(
      <ApplyOutcomeBanner
        outcome={bannerFor({
          accepted: false,
          pendingReload: false,
          runtimeObserved: false,
          restartMessage: "Restart to apply the ACME change.",
        })}
      />,
    );
    const el = screen.getByRole("alert");
    expect(el).toHaveAttribute("data-outcome", "restart-required");
    expect(el).toHaveTextContent("Restart required");
  });

  it("renders a reload-timed-out apply as a warning alert", () => {
    render(
      <ApplyOutcomeBanner
        outcome={bannerFor({
          accepted: true,
          pendingReload: false,
          runtimeObserved: true,
          streamStatus: "ok",
          reload: reload({ outcome: "applied_degraded", timed_out: true }),
        })}
      />,
    );
    const el = screen.getByRole("alert");
    expect(el).toHaveAttribute("data-outcome", "reload-timed-out");
  });
});

// ── Phase 2 outcome kinds (P2-05 §22.7) ─────────────────────────────────────

describe("deriveApplyOutcome — stage_restart and discard outcomes (P2-05)", () => {
  it("staged-for-restart: mode=stage_restart, accepted, first stage", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      mode: "stage_restart",
      isStagedUpdate: false,
    });
    expect(o.kind).toBe("staged-for-restart");
    expect(o.severity).toBe("info");
    expect(o.blocking).toBe(false);
  });

  it("staged-update: mode=stage_restart, accepted, isStagedUpdate", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      mode: "stage_restart",
      isStagedUpdate: true,
    });
    expect(o.kind).toBe("staged-update");
    expect(o.severity).toBe("info");
    expect(o.blocking).toBe(false);
  });

  it("pending-restart-blocks-hot: pendingRestartBlocksHot=true", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: false,
      pendingRestartBlocksHot: true,
    });
    expect(o.kind).toBe("pending-restart-blocks-hot");
    expect(o.severity).toBe("blocked");
    expect(o.blocking).toBe(true);
  });

  it("discard-success: isDiscard=true", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      isDiscard: true,
    });
    expect(o.kind).toBe("discard-success");
    expect(o.severity).toBe("success");
    expect(o.blocking).toBe(false);
  });

  it("staged-for-restart takes precedence over restart-required check", () => {
    // Even when accepted=true and mode=stage_restart, no restart-required path.
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: false,
      mode: "stage_restart",
    });
    expect(o.kind).toBe("staged-for-restart");
  });

  it("restart-required still fires when mode=hot and accepted=false", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: false,
      mode: "hot",
      restartMessage: "cache settings changed",
    });
    expect(o.kind).toBe("restart-required");
    expect(o.severity).toBe("blocked");
    expect(o.blocking).toBe(true);
    expect(o.message).toContain("cache settings changed");
  });
});

describe("ApplyOutcomeBanner — reload-timed-out and capabilities (continued)", () => {
  function bannerFor(input: Parameters<typeof deriveApplyOutcome>[0]): ApplyOutcome {
    return deriveApplyOutcome(input);
  }

  it("the timed-out copy mentions the timeout", () => {
    render(
      <ApplyOutcomeBanner
        outcome={bannerFor({
          accepted: true,
          pendingReload: false,
          runtimeObserved: true,
          streamStatus: "ok",
          reload: reload({ outcome: "applied_degraded", timed_out: true }),
        })}
      />,
    );
    const el = screen.getByRole("alert");
    expect(el).toHaveTextContent("reload exceeded");
  });

  it("shows the capability tally only for a non-blocking outcome", () => {
    const { rerender } = render(
      <ApplyOutcomeBanner
        outcome={bannerFor({ accepted: true, pendingReload: true, runtimeObserved: true })}
        capabilities={{ active: 3, total: 5 }}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("3 of 5 capabilities");

    rerender(
      <ApplyOutcomeBanner
        outcome={bannerFor({ accepted: false, pendingReload: false, runtimeObserved: false })}
        capabilities={{ active: 3, total: 5 }}
      />,
    );
    expect(screen.getByRole("alert")).not.toHaveTextContent("capabilities");
  });
});

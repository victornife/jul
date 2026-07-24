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
import { deriveApplyOutcome, streamReloadFailure, type ApplyOutcome } from "@/lib/applyOutcome.ts";
import { ApplyOutcomeBanner } from "@/features/config/ApplyOutcomeBanner.tsx";

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
      runtimeObserved: false,
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

  it("reload-timed-out: accepted but previous reload exceeded timeout", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: true,
      streamStatus: "ok",
      reloadTimedOut: true,
      published: true,
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
      pendingReload: true,
      runtimeObserved: false,
      streamStatus: "failed: bind error",
      reloadTimedOut: true,
      published: true,
    });
    expect(o.kind).toBe("reload-timed-out");
  });

  // Finding 5: saved_not_live must never be rendered as "serving".
  it("saved-not-live: accepted, backend outcome saved_not_live, final outcome unknown", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: false,
      savedNotLive: true,
    });
    expect(o.kind).toBe("saved-not-live");
    expect(o.severity).toBe("warning");
    expect(o.blocking).toBe(false);
    // The copy must NOT claim the config is serving, and must point the operator
    // at the runtime overview for the terminal outcome.
    expect(o.message.toLowerCase()).not.toContain("is now serving");
    expect(o.message.toLowerCase()).not.toContain("and is now live");
    expect(o.message).toMatch(/final outcome|runtime overview/i);
  });

  it("saved-not-live outranks reloadTimedOut so timed-out copy never claims serving", () => {
    // The backend marks a saved_not_live response as timed_out; without the
    // precedence guard the operator would see the "is now serving" timeout copy.
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: true,
      runtimeObserved: false,
      savedNotLive: true,
      reloadTimedOut: true,
    });
    expect(o.kind).toBe("saved-not-live");
    expect(o.message.toLowerCase()).not.toContain("is now serving");
  });
});

describe("ApplyOutcomeBanner", () => {
  function bannerFor(input: Parameters<typeof deriveApplyOutcome>[0]): ApplyOutcome {
    return deriveApplyOutcome(input);
  }

  it("renders a full-live apply as polite status", () => {
    render(
      <ApplyOutcomeBanner
        outcome={bannerFor({
          accepted: true,
          pendingReload: true,
          runtimeObserved: true,
          streamStatus: "ok",
        })}
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
          pendingReload: true,
          runtimeObserved: true,
          streamStatus: "ok",
          reloadTimedOut: true,
          published: true,
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
          pendingReload: true,
          runtimeObserved: true,
          streamStatus: "ok",
          reloadTimedOut: true,
          published: true,
        })}
      />,
    );
    const el = screen.getByRole("alert");
    expect(el).toHaveTextContent("reload exceeded");
  });

  it("treats a timed-out subsystem as degradation", () => {
    const o = deriveApplyOutcome({
      accepted: true,
      pendingReload: false,
      runtimeObserved: true,
      reloadOutcome: "applied_degraded",
      published: true,
      admin: { status: "timed_out", error: "policy install exceeded deadline" },
    });
    expect(o.kind).toBe("partial-reload");
    expect(o.failures[0]?.name).toBe("admin subsystem");
  });

  it("renders not-applied plus restored without claiming live", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: true,
      reloadOutcome: "not_applied",
      persisted: true,
      restored: true,
      reloadError: "prepare failed",
    });
    expect(o.kind).toBe("rejected-restored");
    expect(o.message.toLowerCase()).not.toContain("now live");
  });

  it("distinguishes restoration failure", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: true,
      reloadOutcome: "not_applied",
      persisted: true,
      restoreError: "disk digest mismatch",
    });
    expect(o.kind).toBe("restoration-failed");
    expect(o.blocking).toBe(true);
  });

  it("classifies an enqueue rejection before generic restored handling", () => {
    const o = deriveApplyOutcome({
      accepted: false,
      pendingReload: false,
      runtimeObserved: true,
      reloadOutcome: "not_applied",
      enqueueFailed: true,
      restored: true,
    });
    expect(o.kind).toBe("enqueue-failed");
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

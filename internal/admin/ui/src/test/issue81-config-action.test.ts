/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import { configActionLabel } from "@/lib/configActionPresentation.ts";
import { recommendPatchAction } from "@/lib/configDraftHandoff.ts";
import { evaluateConfigHandoffGuard } from "@/lib/configHandoffGuard.ts";

describe("issue #81 action and race contract", () => {
  it("uses the exact primary labels", () => {
    expect(configActionLabel("hot")).toBe("Apply live");
    expect(configActionLabel("stage_restart")).toBe("Save for next restart");
    expect(configActionLabel("update_staged")).toBe("Update staged configuration");
    expect(configActionLabel("none")).toBe("No safe apply action");
  });

  it("keeps the primary action blocked until pending-restart status is known", () => {
    expect(
      evaluateConfigHandoffGuard({
        pendingKnown: false,
        pendingChanged: false,
        baseChanged: false,
        refreshing: false,
        refreshFailed: false,
      }),
    ).toMatchObject({ blocked: true, requiresRefresh: false });
  });

  it("requires a fresh server preview after pending state changes", () => {
    expect(
      evaluateConfigHandoffGuard({
        pendingKnown: true,
        pendingChanged: true,
        baseChanged: false,
        refreshing: false,
        refreshFailed: false,
      }),
    ).toMatchObject({ blocked: true, requiresRefresh: true });
  });

  it("blocks a stale raw base instead of substituting a newer token", () => {
    expect(
      evaluateConfigHandoffGuard({
        pendingKnown: true,
        pendingChanged: false,
        baseChanged: true,
        refreshing: false,
        refreshFailed: false,
      }),
    ).toMatchObject({ blocked: true, requiresRefresh: false });
  });

  it("follows the server result for retained versus all-new listener max_conns previews", () => {
    const retained = {
      can_apply_hot: false,
      can_stage_restart: true,
      changes: [],
      hot_paths: [],
      restart_required_paths: ["rate_limit.max_conns"],
      new_listener_only_paths: [],
      ignored_deprecated_paths: [],
      validation_rejected_paths: [],
      pending_subsystems: ["listeners"],
    };
    const allNew = {
      ...retained,
      can_apply_hot: true,
      restart_required_paths: [],
      new_listener_only_paths: ["rate_limit.max_conns"],
    };
    expect(recommendPatchAction(retained, { state: "none", subsystems: [] })).toBe(
      "stage_restart",
    );
    expect(recommendPatchAction(allNew, { state: "none", subsystems: [] })).toBe("hot");
  });

  it("stages a server-classified global.config_authority change", () => {
    expect(
      recommendPatchAction(
        {
          can_apply_hot: false,
          can_stage_restart: true,
          changes: [],
          hot_paths: [],
          restart_required_paths: ["global.config_authority"],
          new_listener_only_paths: [],
          ignored_deprecated_paths: [],
          validation_rejected_paths: [],
          pending_subsystems: ["config_authority"],
        },
        { state: "none", subsystems: [] },
      ),
    ).toBe("stage_restart");
  });

  it("updates an existing managed staged candidate instead of hot-applying", () => {
    const lifecycle = {
      can_apply_hot: true,
      can_stage_restart: true,
      changes: [],
      hot_paths: ["compression.level"],
      restart_required_paths: [],
      new_listener_only_paths: [],
      ignored_deprecated_paths: [],
      validation_rejected_paths: [],
      pending_subsystems: [],
    };
    expect(
      recommendPatchAction(lifecycle, {
        state: "managed_staged",
        stagedVersion: "stage-a",
        subsystems: ["cache"],
      }),
    ).toBe("update_staged");
  });

});

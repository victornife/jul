/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { RecommendedConfigAction } from "@/lib/configDraftHandoff.ts";

export const configActionLabels: Record<RecommendedConfigAction, string> = {
  hot: "Apply live",
  stage_restart: "Save for next restart",
  update_staged: "Update staged configuration",
  none: "No safe apply action",
};

export function configActionLabel(action: RecommendedConfigAction): string {
  return configActionLabels[action];
}

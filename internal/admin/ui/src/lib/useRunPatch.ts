/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { patchConfig, ConfigRejectedError, type ConfigPatch } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";

// useRunPatch previews a structured patch and hands the resulting diff to the
// Config editor for Validate → Diff → Apply — it never writes directly. It is the
// single shared handoff hook used by every structured editor (Plugins, Streams,
// Apps, mutual-TLS, and the TLS panel's per-location require_client_cert toggle)
// so they all route through the same flow.
export function useRunPatch(): {
  readonly error: string | null;
  readonly busy: boolean;
  readonly run: (patch: ConfigPatch) => void;
} {
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  function run(patch: ConfigPatch): void {
    setError(null);
    setBusy(true);
    void (async () => {
      try {
        const res = await patchConfig(patch);
        setPendingDraft({
          kind: "patch",
          ops: [patch],
          baseVersion: res.base_version,
          previewDiff: res.diff,
          candidate: res.candidate,
        });
        void navigate("/config");
      } catch (err) {
        setError(
          err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.",
        );
        setBusy(false);
      }
    })();
  }
  return { error, busy, run };
}

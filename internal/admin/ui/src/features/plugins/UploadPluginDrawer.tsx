/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { uploadPluginWasm } from "@/api/client.ts";

/**
 * Lets an operator upload a compiled .wasm module directly to the server. The
 * uploaded file is stored server-side and its path can then be referenced when
 * declaring a plugin.
 */
export function UploadPluginDrawer({
  onClose,
  onUploaded,
  uploadMaxSizeMB,
}: {
  readonly onClose: () => void;
  readonly onUploaded: (path: string) => void;
  readonly uploadMaxSizeMB: number;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function validateWasm(f: File): string | null {
    if (f.size <= 0) return "File is empty.";
    if (!f.name.endsWith(".wasm")) return "Expected a .wasm file.";
    const limit = uploadMaxSizeMB * 1024 * 1024;
    if (limit > 0 && f.size > limit) {
      return `File exceeds the server upload limit of ${String(uploadMaxSizeMB)} MB.`;
    }
    return null;
  }

  async function submit(): Promise<void> {
    if (!file) return;
    const validation = validateWasm(file);
    if (validation) { setErr(validation); return; }
    setBusy(true);
    setErr(null);
    try {
      const resp = await uploadPluginWasm(file);
      onUploaded(resp.path);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Upload failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer
      title="Upload .wasm"
      subtitle="Choose a compiled WebAssembly module"
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {err && <span className="text-xs text-jul-danger">{err}</span>}
          <button
            type="button"
            disabled={busy || !file}
            onClick={() => { void submit(); }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {busy ? "Uploading…" : "Upload"}
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          The module is uploaded to the server and referenced by path in the plugin declaration.
          After upload, you can create a new plugin that points to the uploaded file.
        </p>
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Module file</span>
          <input
            type="file"
            accept=".wasm"
            onChange={(e) => { setFile(e.target.files?.[0] ?? null); setErr(null); }}
            className="block w-full text-sm text-jul-text file:mr-4 file:rounded-md file:border-0 file:bg-jul-accent file:px-3 file:py-1.5 file:text-sm file:font-medium file:text-jul-bg hover:file:brightness-110"
          />
          <span className="text-xs text-jul-muted">
            {uploadMaxSizeMB > 0
              ? `Accepted: .wasm, up to ${String(uploadMaxSizeMB)} MB.`
              : "Uploads are disabled by admin config."}
          </span>
        </label>
        {file && (
          <div className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            <span className="font-medium text-jul-text">{file.name}</span>
            <span className="ml-2">{`${(file.size / 1024).toFixed(1)} KB`}</span>
          </div>
        )}
      </div>
    </Drawer>
  );
}

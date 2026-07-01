import { useState } from "react";
import {
  patchConfig,
  ConfigRejectedError,
  type ConfigPatch,
  type LocationProjection,
  type RouteProjection,
  type TranscodePatch,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { useNavigate } from "react-router-dom";

function Toggle({
  label,
  on,
  onChange,
}: {
  readonly label: string;
  readonly on: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={on}
        onChange={(e) => { onChange(e.target.checked); }}
        className="h-4 w-4 rounded border-jul-border text-jul-accent accent-jul-accent"
      />
      {label}
    </label>
  );
}

function useTranscodeQuickEdit(route: RouteProjection, loc: LocationProjection) {
  const navigate = useNavigate();
  const t = loc.transcode;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [tcTarget, setTcTarget]               = useState(loc.target ?? "");
  const [tcDescPath, setTcDescPath]           = useState(t?.descriptor_set ?? "");

  const tcReflectDefault: boolean | undefined = t?.use_reflection;
  const tcPreserveDefault: boolean | undefined = t?.preserve_proto_field_names;
  const tcStreamDefault: boolean | undefined = t?.streaming;
  const tcModeDefault: "ndjson" | "sse" | undefined = t?.stream_mode as "ndjson" | "sse" | undefined;
  const tcMaxSizeDefault: string | undefined = t?.max_message_size;
  const tcDescDefault: string | undefined = t?.descriptor_set;

  const [tcPreserve, setTcPreserve]           = useState(tcPreserveDefault ?? false);
  const [tcReflect, setTcReflect]             = useState(tcReflectDefault ?? false);
  const [tcTLS, setTcTLS]                     = useState(t?.tls ?? false);
  const [tcStream, setTcStream]               = useState(tcStreamDefault ?? false);
  const [tcMode, setTcMode]                   = useState<"ndjson" | "sse">(tcModeDefault ?? "ndjson");
  const [tcMaxSize, setTcMaxSize]             = useState(tcMaxSizeDefault ?? "");

  const dirty =
    tcTarget.trim()    !== (loc.target ?? "").trim() ||
    tcDescPath.trim()  !== (tcDescDefault ?? "").trim() ||
    tcReflect          !== (tcReflectDefault ?? false) ||
    tcTLS              !== (t?.tls ?? false) ||
    tcPreserve         !== (tcPreserveDefault ?? false) ||
    tcStream           !== (tcStreamDefault ?? false) ||
    tcMode             !== (tcModeDefault ?? "ndjson") ||
    tcMaxSize.trim()   !== (tcMaxSizeDefault ?? "").trim();

  function buildPatch(): ConfigPatch {
    const payload: TranscodePatch = {
      target: tcTarget.trim(),
      ...(tcReflect
        ? { use_reflection: true }
        : { descriptor_path: tcDescPath.trim() }),
      tls: tcTLS,
      preserve_names: tcPreserve,
      streaming: tcStream,
      ...(tcStream ? { stream_mode: tcMode } : {}),
      ...(tcMaxSize.trim() !== "" ? { max_message_size: tcMaxSize.trim() } : {}),
    };
    return {
      op: "location_set_transcode",
      listen: route.listen,
      server_names: route.server_names ?? [],
      match_type: loc.type,
      path: loc.match,
      transcode: payload,
    };
  }

  async function save() {
    setError(null);
    setBusy(true);
    try {
      const patch = buildPatch();
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
      setError(err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.");
      setBusy(false);
    }
  }

  return {
    busy, error,
    tcTarget, setTcTarget,
    tcDescPath, setTcDescPath,
    tcPreserve, setTcPreserve,
    tcReflect, setTcReflect,
    tcTLS, setTcTLS,
    tcStream, setTcStream,
    tcMode, setTcMode,
    tcMaxSize, setTcMaxSize,
    dirty,
    save,
  };
}

/** TranscodeQuickEdit is the quick-edit card for grpc_transcode routes. */
export function TranscodeQuickEdit({
  route,
  loc,
}: {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
}) {
  const edit = useTranscodeQuickEdit(route, loc);

  return (
    <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
      <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
        Transcode quick edit
      </span>
      <div className="flex flex-col gap-2">
        <label className="text-xs text-jul-muted">gRPC target</label>
        <input
          type="text"
          value={edit.tcTarget}
          placeholder="grpc-backend:50051"
          onChange={(e) => { edit.setTcTarget(e.target.value); }}
          className="flex-1 rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
        />

        <label className="text-xs text-jul-muted">Descriptor/Reflection</label>
        {!edit.tcReflect && (
          <input
            type="text"
            value={edit.tcDescPath}
            placeholder="/path/to/descriptor_set.pb"
            onChange={(e) => { edit.setTcDescPath(e.target.value); }}
            className="flex-1 rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
        )}
        <Toggle
          label="Use server reflection instead of descriptor file"
          on={edit.tcReflect}
          onChange={(v) => {
            edit.setTcReflect(v);
            if (v) edit.setTcDescPath("");
          }}
        />

        <div className="grid grid-cols-2 gap-2">
          <Toggle label="Backend TLS" on={edit.tcTLS} onChange={edit.setTcTLS} />
          <Toggle label="Preserve proto field names" on={edit.tcPreserve} onChange={edit.setTcPreserve} />
          <Toggle label="Streaming" on={edit.tcStream} onChange={edit.setTcStream} />
          <div className="flex items-center gap-2">
            <span className="text-sm text-jul-text">Mode</span>
            <select
              value={edit.tcMode}
              onChange={(e) => { edit.setTcMode(e.target.value as "ndjson" | "sse"); }}
              disabled={!edit.tcStream}
              className="rounded-md border border-jul-border bg-jul-bg px-2 py-1 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent disabled:opacity-40"
            >
              <option value="ndjson">ndjson</option>
              <option value="sse">sse</option>
            </select>
          </div>
        </div>

        <label className="text-xs text-jul-muted">Max message size (optional)</label>
        <input
          type="text"
          value={edit.tcMaxSize}
          placeholder="e.g. 4m"
          onChange={(e) => { edit.setTcMaxSize(e.target.value); }}
          className="flex-1 rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
        />
      </div>
      <div className="flex gap-2">
        <button
          type="button"
          disabled={
            edit.busy ||
            !edit.dirty ||
            edit.tcTarget.trim() === "" ||
            (!edit.tcReflect && edit.tcDescPath.trim() === "")
          }
          onClick={() => void edit.save()}
          className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
        >
          Save transcode settings →
        </button>
      </div>
      {edit.error && <p className="text-xs text-jul-danger">{edit.error}</p>}
    </div>
  );
}

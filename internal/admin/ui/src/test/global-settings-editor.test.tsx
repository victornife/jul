/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { PermissionContext } from "@/auth/usePermission.ts";
import { GlobalSettingsEditor } from "@/features/config/GlobalSettingsEditor.tsx";
import { GlobalSettingsProjectionSchema, type GlobalSettingsProjection } from "@/api/client.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

vi.unmock("@/api/client.ts");
const current = GlobalSettingsProjectionSchema.parse({
  lifecycle: {
    log_level: { class: "hot_reload", subsystem: "logging", reason: "rebuild logger" },
    worker_threads: { class: "restart_required", subsystem: "runtime", reason: "process setting" },
  },
});
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const preview = {
  ok: true,
  summary: "global changes",
  diff: { summary: "global changes" },
  base_version: "v1",
  lifecycle: { changes: [], can_apply_hot: true, can_stage_restart: true },
};
const inspectionCurrent = GlobalSettingsProjectionSchema.parse({
  inspection: {
    mode: "enforce",
    max_buffered_bytes: "256m",
    max_guest_capacity_bytes: "1g",
    max_response_header_bytes: "64k",
    max_concurrent_evaluations: 4,
    evaluation_queue_timeout: "1s",
    body_timeout: "1m0s",
  },
});
function mount(
  canWrite = true,
  projection: GlobalSettingsProjection = current,
  configAuthority?: "managed" | "file_owned",
  canReadRaw = canWrite,
  baseVersion: string | undefined = "v1",
) {
  const onClose = vi.fn();
  const permissions = [...(canWrite ? ["config:write"] : []), ...(canReadRaw ? ["config:raw"] : [])];
  render(
    <MemoryRouter>
      <PermissionContext.Provider
        value={{
          ready: true,
          isLoading: false,
          identity: { principal: "test", role: "custom", permissions, token_id: "", legacy: false },
          has: (p) => permissions.includes(p),
        }}
      >
        <GlobalSettingsEditor current={projection} configAuthority={configAuthority} baseVersion={baseVersion} onClose={onClose} />
      </PermissionContext.Provider>
    </MemoryRouter>,
  );
  return onClose;
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  takePendingDraft();
  sessionStorage.clear();
});

describe("global configuration UI uses sparse server-owned previews", () => {
  it("previews changed inspection limits and preserves explicit zero values", async () => {
    const fetch = vi.fn((url: string, _init?: RequestInit) =>
      Promise.resolve(json(url.endsWith("pending-restart") ? { pending: false } : preview)),
    );
    vi.stubGlobal("fetch", fetch);
    mount(true, inspectionCurrent, "managed");
    fireEvent.change(screen.getByLabelText("Inspection mode"), { target: { value: "observe" } });
    fireEvent.change(screen.getByLabelText(/Inspection byte budget/), { target: { value: "512m" } });
    fireEvent.change(screen.getByLabelText(/Guest capacity budget/), { target: { value: "2g" } });
    fireEvent.change(screen.getByLabelText(/Inspection response-header budget/), { target: { value: "128k" } });
    fireEvent.change(screen.getByLabelText(/Inspection evaluation slots/), { target: { value: "" } });
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Inspection evaluation slots/), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText(/Inspection evaluation queue timeout/), { target: { value: "0s" } });
    fireEvent.change(screen.getByLabelText(/Inspection body timeout/), { target: { value: "0s" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => { expect(fetch).toHaveBeenCalledTimes(2); });
    expect(JSON.parse(fetch.mock.calls[1]?.[1]?.body as string)).toEqual({
      base_version: "v1",
      ops: [{ op: "global_set", global: {
        inspection: {
          mode: "observe",
          max_buffered_bytes: "512m",
          max_guest_capacity_bytes: "2g",
          max_response_header_bytes: "128k",
          max_concurrent_evaluations: 0,
          evaluation_queue_timeout: "0s",
          body_timeout: "0s",
        },
      } }],
    });
  });

  it.each(["file_owned", undefined] as const)(
    "keeps inspection settings read-only when authority is %s",
    (authority) => {
      const fetch = vi.fn();
      vi.stubGlobal("fetch", fetch);
      mount(true, inspectionCurrent, authority);
      expect(screen.getByLabelText("Inspection mode")).toBeDisabled();
      expect(screen.getByLabelText(/Inspection body timeout/)).toBeDisabled();
      expect(screen.getByText(/inspection settings are read-only|File-owned configuration is read-only/i)).toBeVisible();
      expect(fetch).not.toHaveBeenCalled();
    },
  );

  it("shows incompatible or absent inspection settings as unavailable", () => {
    const old = GlobalSettingsProjectionSchema.parse({ inspection: { mode: "observe" } });
    mount(true, old, "managed");
    expect(screen.getByText("Inspection settings are unavailable on this server.")).toBeVisible();
    expect(screen.queryByLabelText("Inspection mode")).toBeNull();
  });

  it("keeps guest capacity unavailable on older servers without hiding other settings", () => {
    const old = GlobalSettingsProjectionSchema.parse({ inspection: {
      mode: "enforce",
      max_buffered_bytes: "256m",
      max_response_header_bytes: "64k",
      max_concurrent_evaluations: 4,
      evaluation_queue_timeout: "1s",
      body_timeout: "1m0s",
    } });
    mount(true, old, "managed");
    expect(screen.getByLabelText(/Guest capacity budget/)).toBeDisabled();
    expect(screen.getByLabelText("Inspection mode")).toBeEnabled();
  });

  it("keeps inspection read-only when the editable base version is missing", () => {
    mount(true, inspectionCurrent, "managed", true, "");
    expect(screen.getByLabelText("Inspection mode")).toBeDisabled();
    expect(screen.getByText(/configuration version is unavailable/)).toBeVisible();
  });

  it("requires explicit zero instead of a cleared timeout", () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    mount(true, inspectionCurrent, "managed");
    fireEvent.change(screen.getByLabelText(/Inspection body timeout/), { target: { value: " " } });
    expect(screen.getByText(/Enter a duration; use 0s to disable the body deadline/)).toBeVisible();
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("freezes the form while a preview is running", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    mount(true, inspectionCurrent, "managed");
    fireEvent.change(screen.getByLabelText("Inspection mode"), { target: { value: "observe" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => { expect(screen.getByLabelText("Inspection mode")).toBeDisabled(); });
    expect(screen.getByLabelText(/Worker threads/)).toBeDisabled();
  });

  it("keeps the original base version if the projection changes while editing", async () => {
    const fetch = vi.fn((url: string, _init?: RequestInit) =>
      Promise.resolve(json(url.endsWith("pending-restart") ? { pending: false } : preview)));
    vi.stubGlobal("fetch", fetch);
    const permissions = ["config:write", "config:raw"];
    const editor = (version: string) => (
      <MemoryRouter>
        <PermissionContext.Provider value={{
          ready: true, isLoading: false, identity: null, has: (p) => permissions.includes(p),
        }}>
          <GlobalSettingsEditor current={inspectionCurrent} configAuthority="managed" baseVersion={version} onClose={() => undefined} />
        </PermissionContext.Provider>
      </MemoryRouter>
    );
    const view = render(editor("v1"));
    fireEvent.change(screen.getByLabelText("Inspection mode"), { target: { value: "observe" } });
    view.rerender(editor("v2"));
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => { expect(fetch).toHaveBeenCalledTimes(2); });
    expect(JSON.parse(fetch.mock.calls[1]?.[1]?.body as string)).toMatchObject({ base_version: "v1" });
  });

  it.each([[false, false], [true, false]])(
    "keeps inspection read-only without required permissions (write=%s, raw=%s)",
    (write, raw) => {
      const fetch = vi.fn();
      vi.stubGlobal("fetch", fetch);
      mount(write, inspectionCurrent, "managed", raw);
      expect(screen.getByLabelText("Inspection mode")).toBeDisabled();
      expect(fetch).not.toHaveBeenCalled();
    },
  );

  it("reports backend inspection validation errors and allows correction", async () => {
    const fetch = vi.fn((url: string) => Promise.resolve(url.endsWith("pending-restart")
      ? json({ pending: false })
      : json({
          ok: false, message: "Invalid inspection settings",
          errors: [{ code: "duration", severity: "error", path: "global.inspection.body_timeout", summary: "invalid duration" }],
        }, 400)));
    vi.stubGlobal("fetch", fetch);
    mount(true, inspectionCurrent, "managed");
    fireEvent.change(screen.getByLabelText(/Inspection body timeout/), { target: { value: "bad" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    expect((await screen.findAllByText("invalid duration")).length).toBeGreaterThan(0);
    fireEvent.change(screen.getByLabelText(/Inspection body timeout/), { target: { value: "3s" } });
    expect(screen.queryByText("invalid duration")).toBeNull();
  });

  it("shows pathless backend operation details", async () => {
    const fetch = vi.fn((url: string) => Promise.resolve(url.endsWith("pending-restart")
      ? json({ pending: false })
      : json({
          ok: false, message: "Operation could not be applied", op_index: 0, op: "global_set",
          errors: [{ code: "unknown", severity: "error", summary: "inspection.max_concurrent_evaluations must be between 0 and 1024" }],
        }, 400)));
    vi.stubGlobal("fetch", fetch);
    mount(true, inspectionCurrent, "managed");
    fireEvent.change(screen.getByLabelText(/Inspection evaluation slots/), { target: { value: "1025" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    expect(await screen.findByText(/must be between 0 and 1024/)).toBeVisible();
  });

  it("validates worker input and hands off one sparse operation with all changed fields", async () => {
    const fetch = vi.fn((url: string, _init?: RequestInit) =>
      Promise.resolve(json(url.endsWith("pending-restart") ? { pending: false } : preview)),
    );
    vi.stubGlobal("fetch", fetch);
    mount();
    const review = screen.getByRole("button", { name: "Review changes" });
    expect(review).toBeDisabled();
    expect(screen.getByText("restart required")).toHaveAttribute(
      "title",
      "runtime: process setting",
    );
    fireEvent.change(screen.getByLabelText(/Worker threads/), { target: { value: "0" } });
    expect(screen.getByText("Enter auto or a positive integer.")).toBeInTheDocument();
    expect(review).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Worker threads/), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText(/Log level/), { target: { value: "debug" } });
    fireEvent.change(screen.getByLabelText(/Log format/), { target: { value: "json" } });
    fireEvent.change(screen.getByLabelText(/Shutdown timeout/), { target: { value: "10s" } });
    fireEvent.change(screen.getByLabelText(/Reload timeout/), { target: { value: "5s" } });
    fireEvent.change(screen.getByLabelText(/Minimum secret redaction length/), {
      target: { value: "8" },
    });
    fireEvent.click(review);
    await waitFor(() => {
      expect(fetch).toHaveBeenCalledTimes(2);
    });
    expect(fetch.mock.calls[1]?.[0]).toBe("/api/config/patch/preview");
    expect(JSON.parse(fetch.mock.calls[1]?.[1]?.body as string)).toEqual({
      ops: [
        {
          op: "global_set",
          global: {
            worker_threads: "4",
            log_level: "debug",
            log_format: "json",
            shutdown_timeout: "10s",
            reload_timeout: "5s",
            redact_min_secret_length: 8,
          },
        },
      ],
    });
    await waitFor(() => {
      expect(review).toBeEnabled();
    });
    expect(takePendingDraft()).toMatchObject({
      kind: "patch",
      baseVersion: "v1",
      summary: "global changes",
    });
  });

  it("known missing grant disables review and emits no request; cancel remains usable", () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const close = mount(false);
    fireEvent.change(screen.getByLabelText(/Log level/), { target: { value: "debug" } });
    const review = screen.getByRole("button", { name: "Review changes" });
    expect(review).toBeDisabled();
    fireEvent.click(review);
    expect(fetch).not.toHaveBeenCalled();
    expect(screen.getByText("config:write")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(close).toHaveBeenCalledOnce();
  });

  it.each([400, 403])(
    "server rejection %i is truthful, clears on edit, and permits a fresh preview",
    async (status) => {
      const body =
        status === 400
          ? {
              ok: false,
              message: "bad timeout",
              errors: [
                {
                  code: "duration",
                  severity: "error",
                  path: "global.reload_timeout",
                  summary: "invalid duration",
                  detail: "use seconds",
                },
              ],
            }
          : { error: "permission revoked" };
      let requests = 0;
      const fetch = vi.fn((url: string, _init?: RequestInit) => {
        if (url.endsWith("pending-restart")) return Promise.resolve(json({ pending: false }));
        requests++;
        return Promise.resolve(requests === 1 ? json(body, status) : json(preview));
      });
      vi.stubGlobal("fetch", fetch);
      mount();
      fireEvent.change(screen.getByLabelText(/Reload timeout/), { target: { value: "bad" } });
      fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
      expect(await screen.findByRole("alert")).toHaveTextContent(
        status === 400 ? "bad timeout" : "permission revoked",
      );
      if (status === 400)
        expect(screen.getAllByText("invalid duration — use seconds").length).toBeGreaterThan(0);
      expect(takePendingDraft()).toBeNull();
      expect(fetch).toHaveBeenCalledTimes(2);
      fireEvent.change(screen.getByLabelText(/Reload timeout/), { target: { value: "5s" } });
      expect(screen.queryByRole("alert")).toBeNull();
      fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
      await waitFor(() => {
        expect(fetch).toHaveBeenCalledTimes(4);
      });
    },
  );
});

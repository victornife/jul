/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { PermissionContext } from "@/auth/usePermission.ts";
import { GlobalSettingsEditor } from "@/features/config/GlobalSettingsEditor.tsx";
import { GlobalSettingsProjectionSchema } from "@/api/client.ts";
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
function mount(canWrite = true) {
  const onClose = vi.fn();
  const permissions = canWrite ? ["config:write", "config:raw"] : [];
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
        <GlobalSettingsEditor current={current} onClose={onClose} />
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
        expect(screen.getByText("invalid duration — use seconds")).toBeInTheDocument();
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

/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import {
  patchListenerClientAddress,
  describeApiError,
  type ListenerClientAddress,
} from "@/api/client.ts";

/**
 * Editor for one listener's trusted-proxy policy.
 *
 * The policy is scoped to the listen address, not to a virtual host: Jul derives
 * the client address before the Host header selects a server block, so every
 * block on the address shares one policy. The drawer says so explicitly, because
 * an operator editing "a server" would otherwise be surprised that the change
 * covers its siblings.
 *
 * No CIDR, enum or range validation happens here. The server validates the whole
 * candidate and its message is what the operator sees, so the Console can never
 * disagree with the configuration it is editing.
 */
export function ClientAddressEditor({
  listener,
  onClose,
}: {
  readonly listener: ListenerClientAddress;
  readonly onClose: () => void;
}) {
  const { has } = usePermission();
  const canEdit = has("config:trust");
  const queryClient = useQueryClient();

  const [trusted, setTrusted] = useState(() => listener.trusted_proxies.join("\n"));
  const [headers, setHeaders] = useState<string>(() =>
    listener.configured && listener.headers_disabled ? "none" : listener.forwarded_headers.join(","),
  );
  const [maxHops, setMaxHops] = useState(() => String(listener.max_hops));
  const [error, setError] = useState<string | null>(null);

  const apply = useMutation({
    mutationFn: (policy: Parameters<typeof patchListenerClientAddress>[1]) =>
      patchListenerClientAddress(listener.listen, policy),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["listeners"] });
      void queryClient.invalidateQueries({ queryKey: ["status"] });
      onClose();
    },
    onError: (e: unknown) => {
      setError(describeApiError(e, "the trusted-proxy policy").message);
    },
  });

  function save(): void {
    setError(null);
    const entries = trusted
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean);
    const hops = Number.parseInt(maxHops, 10);
    // "none" is the explicitly-empty list: read no forwarding header at all.
    const forwarded =
      headers === "none"
        ? []
        : headers
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
    apply.mutate(
      Number.isFinite(hops) && hops > 0
        ? { trusted_proxies: entries, forwarded_headers: forwarded, max_hops: hops }
        : { trusted_proxies: entries, forwarded_headers: forwarded },
    );
  }

  function clearPolicy(): void {
    setError(null);
    apply.mutate(null);
  }

  return (
    <Drawer
      title="Trusted proxies"
      subtitle={`Listener ${listener.listen}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            {listener.configured && (
              <button
                type="button"
                disabled={apply.isPending || !canEdit}
                onClick={clearPolicy}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
              >
                Trust no proxy
              </button>
            )}
            <button
              type="button"
              disabled={apply.isPending || !canEdit}
              onClick={save}
              className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {apply.isPending ? "Applying…" : "Apply to this listener"}
            </button>
          </div>
          <ForbiddenAction permission="config:trust" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          <strong>Trusted proxies are a security boundary.</strong> Every address listed here may
          claim to be any client, and that claim is what CIDR authentication, rate limiting, the WAF
          and the audit trail will believe. List only proxies you operate, and keep the ranges as
          narrow as possible.
        </p>
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          This policy applies to the whole listener — all{" "}
          <strong>
            {listener.server_blocks} server block{listener.server_blocks === 1 ? "" : "s"}
          </strong>{" "}
          on <span className="font-mono">{listener.listen}</span>. The client address is derived
          before the <span className="font-mono">Host</span> header selects a block, so a listener
          has exactly one policy.
        </p>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Trusted proxy ranges</span>
          <textarea
            value={trusted}
            rows={5}
            spellCheck={false}
            placeholder={"10.0.0.0/8\n2001:db8:100::/48"}
            onChange={(e) => {
              setTrusted(e.target.value);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
          <span className="text-xs text-jul-muted">
            One CIDR prefix or address per line. Prefixes must be canonical (
            <span className="font-mono">10.0.0.0/8</span>, not{" "}
            <span className="font-mono">10.1.2.3/8</span>). Leave empty to trust no proxy.
          </span>
        </label>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Forwarding headers</span>
          <select
            value={headers}
            onChange={(e) => {
              setHeaders(e.target.value);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="forwarded,x-forwarded-for">Forwarded, then X-Forwarded-For</option>
            <option value="x-forwarded-for,forwarded">X-Forwarded-For, then Forwarded</option>
            <option value="forwarded">Forwarded only</option>
            <option value="x-forwarded-for">X-Forwarded-For only</option>
            <option value="none">None — always use the transport peer</option>
          </select>
          <span className="text-xs text-jul-muted">
            The first header present on a request is the only one used; chains are never merged.
          </span>
        </label>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Maximum hops</span>
          <input
            type="number"
            min={1}
            max={255}
            value={maxHops}
            onChange={(e) => {
              setMaxHops(e.target.value);
            }}
            className="w-32 rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
          <span className="text-xs text-jul-muted">
            A longer chain fails closed to the transport peer. Default 16, maximum 255.
          </span>
        </label>
      </div>
    </Drawer>
  );
}

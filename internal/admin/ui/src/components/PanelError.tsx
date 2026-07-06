/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describeApiError, type ApiErrorKind } from "@/api/client.ts";
import { Button } from "@/components/ui.tsx";

// PanelError is the shared full-panel failure state. It turns a thrown query
// error into a taxonomy-aware message (see describeApiError) so a 401, a 404 for
// a disabled feature, a 409 stale read, a 5xx, and an offline network all read
// differently and tell the operator what to do — instead of every panel showing
// the same "Failed to load X". Retryable failures also offer a Retry action.

const TONE_CLASSES: Record<"info" | "warning" | "danger", string> = {
  info: "border-jul-border bg-jul-surface text-jul-muted",
  warning: "border-jul-warning/40 bg-jul-warning/10 text-jul-warning",
  danger: "border-jul-danger/40 bg-jul-danger/10 text-jul-danger",
};

// KIND_TONE keeps re-auth/permission/availability problems visually distinct
// from the louder server/network failures.
const KIND_TONE: Record<ApiErrorKind, "info" | "warning" | "danger"> = {
  unauthorized: "warning",
  forbidden: "warning",
  notFound: "info",
  conflict: "warning",
  rateLimited: "warning",
  server: "danger",
  network: "danger",
  unknown: "danger",
};

export function PanelError({
  error,
  resource,
  onRetry,
}: {
  readonly error: unknown;
  readonly resource: string;
  readonly onRetry?: () => void;
}) {
  const described = describeApiError(error, resource);
  const tone = KIND_TONE[described.kind];
  return (
    <div
      role="alert"
      data-error-kind={described.kind}
      className={`space-y-2 rounded-md border p-4 text-sm ${TONE_CLASSES[tone]}`}
    >
      <div className="font-semibold">{described.title}</div>
      <div className="text-jul-text">{described.message}</div>
      {onRetry && described.retryable && (
        <div className="pt-1">
          <Button variant="secondary" onClick={onRetry}>
            Retry
          </Button>
        </div>
      )}
    </div>
  );
}

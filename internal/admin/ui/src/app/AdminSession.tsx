/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import type { ReactNode } from "react";
import { AuthGate } from "@/app/AuthGate.tsx";
import { PermissionProvider } from "@/auth/PermissionProvider.tsx";

/** Credential recovery must remain mounted while data panels are withheld. */
export function AdminSession({ children }: { readonly children: ReactNode }) {
  return (
    <>
      <AuthGate />
      <PermissionProvider>{children}</PermissionProvider>
    </>
  );
}

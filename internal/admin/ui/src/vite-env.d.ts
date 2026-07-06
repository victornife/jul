/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/// <reference types="vite/client" />

declare module "*.css" {
  const classes: Readonly<Record<string, string>>;
  export default classes;
}

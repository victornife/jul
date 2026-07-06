/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useState } from "react";

/**
 * Returns a debounced copy of `value` that only updates after `delayMs` have
 * elapsed without further changes. Used to throttle validate/diff calls while
 * the operator is actively typing in the config editor.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => {
      setDebounced(value);
    }, delayMs);
    return () => {
      clearTimeout(timer);
    };
  }, [value, delayMs]);
  return debounced;
}

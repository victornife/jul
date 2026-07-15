/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * FeatureRoute describes where to navigate when the user acts on a row in the
 * Capabilities & Configuration section. Navigation always targets a panel
 * root; no tab pre-selection is used because the target panels do not
 * currently route via router state.
 */
export interface FeatureRoute {
  route: string;
  /** Action label rendered on the row button, e.g. "Configure Cache". */
  label: string;
}

/**
 * Name-level routes take priority over group-level routes. This is necessary
 * because TLS-related features appear inside the "Security" group in the API
 * response but belong on the /tls panel, not /security.
 *
 * Keys are the exact `name` strings returned by the Go backend (api_status.go).
 */
const FEATURE_NAME_ROUTES: Record<string, FeatureRoute> = {
  "Response cache": { route: "/traffic", label: "Configure Cache" },
  "Rate limiting": { route: "/traffic", label: "Configure Rate Limiting" },
  "Compression": { route: "/traffic", label: "Configure Compression" },
  "TLS": { route: "/tls", label: "Manage TLS" },
  "Automatic HTTPS (ACME)": { route: "/tls", label: "Manage TLS" },
  "Mutual TLS (client certs)": { route: "/tls", label: "Manage TLS" },
  "Access control (auth)": { route: "/security", label: "Configure Auth" },
  "Web application firewall (WAF)": { route: "/security", label: "Configure WAF" },
  "Upstream pools": { route: "/apps", label: "Manage Apps" },
  "Active health checks": { route: "/apps", label: "Manage Apps" },
  "Service discovery": { route: "/apps", label: "Manage Apps" },
  "WASM plugins": { route: "/plugins", label: "Manage Plugins" },
  "L4 stream proxy": { route: "/streams", label: "Manage Streams" },
  "gRPC transcoding": { route: "/transcode", label: "Configure Transcoding" },
};

/**
 * Group-level fallback routes when no name-level match exists.
 * Keys are the exact `group` strings returned by the Go backend.
 */
const GROUP_ROUTES: Record<string, FeatureRoute> = {
  Traffic: { route: "/traffic", label: "View Traffic" },
  Security: { route: "/security", label: "View Security" },
  Upstreams: { route: "/apps", label: "Manage Apps" },
  Observability: { route: "/operations", label: "View Operations" },
  Extensibility: { route: "/plugins", label: "Manage Plugins" },
};

/**
 * Returns the navigation target for a FeatureStatus row, checking the feature
 * name first, then the group. Returns undefined for informational-only rows
 * that have no actionable destination; those rows must not render a button.
 */
export function resolveFeatureRoute(
  group: string,
  name: string,
): FeatureRoute | undefined {
  return FEATURE_NAME_ROUTES[name] ?? GROUP_ROUTES[group];
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

// #158 keeps the admin listener/mux and mutable abuse/lease manager stable while
// publishing these four policy values through the same immutable per-request
// admin generation as authentication, Console mode and upload policy. Keep the
// promotion isolated and exact: admin.enabled/listen/history/audit remain under
// their own restart/gated work.
func init() {
	for i := range Registry {
		switch Registry[i].Path {
		case "admin.rate_limit_read_per_min", "admin.rate_limit_write_per_min", "admin.rate_limit_apply_per_min":
			Registry[i] = hot(Registry[i].Path, SubAdmin, "new admin requests use the rate policy from the immutable admin runtime generation captured at request start while stable per-client bucket state survives reload")
		case "admin.max_event_conns":
			Registry[i] = hot(Registry[i].Path, SubAdmin, "new SSE admissions use the captured per-client connection cap while existing leases and connection counts survive policy reload")
		}
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

const reasonAdminRuntimeSnapshot = "the live admin server reads this value from the immutable admin generation pinned once per request and Publish swaps the complete generation atomically (#157)"

// This initialization upgrades entries in Registry itself; it is not a second
// registry. The blank identifier intentionally makes the initializer's side
// effect explicit while avoiding an otherwise-unused package variable. All
// package variables initialize before lifecycle.init builds registryIndex.
//
// admin.enabled/admin.listen remain startup-consumed. A mixed candidate that
// changes either structural field therefore still fails the startup fingerprint
// gate before the four #157 operational fields can Publish.
var _ = func() struct{} {
	paths := map[string]struct{}{
		"admin.console":                {},
		"admin.plugin_upload_enabled":  {},
		"admin.plugin_upload_max_size": {},
		"admin.plugin_upload_dir":      {},
	}
	for i := range Registry {
		if _, ok := paths[Registry[i].Path]; !ok {
			continue
		}
		Registry[i].Class = HotReloadClass
		Registry[i].Reason = reasonAdminRuntimeSnapshot
		Registry[i].StartupConsumed = false
		Registry[i].AddressKeyed = false
		Registry[i].CollectionKeyed = false
		Registry[i].Conditional = false
	}
	return struct{}{}
}()

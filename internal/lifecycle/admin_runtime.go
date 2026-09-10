// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

// reasonAdminRuntimeSnapshot describes the #157 runtime seam: these fields are
// consumed from the same immutable per-request admin generation as auth and are
// installed with the existing PrepareAuth/CommitPreparedAuth pointer swap.
const reasonAdminRuntimeSnapshot = "the live admin server reads this value from the immutable admin generation pinned once per request and Publish swaps the complete generation atomically (#157)"

// adminRuntimeRegistryUpgrade is a package-initialization dependency on
// Registry, not a second registry. It changes the four #157 paths in-place
// before lifecycle.init builds registryIndex. Keeping the issue-specific
// promotion isolated makes the ownership boundary reviewable while Registry
// remains the single slice consumed by classification, fingerprints and
// generators.
//
// admin.enabled and admin.listen intentionally remain startup-consumed in
// registry.go. A candidate that combines either structural change with one of
// these four fields is therefore still rejected/staged as restart-required and
// cannot partially publish an admin policy.
var adminRuntimeRegistryUpgrade = func() struct{} {
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

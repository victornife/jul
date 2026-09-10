// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"sort"
	"strings"

	"jul/internal/config"
)

// adminPluginUploadPatch is deliberately narrow: it edits only upload
// admission/storage policy and cannot mutate listener/auth/audit/history state.
// Pointer fields preserve omission versus explicit false/zero/empty.
type adminPluginUploadPatch struct {
	Enabled   *bool   `json:"enabled,omitempty"`
	MaxSizeMB *int    `json:"max_size_mb,omitempty"`
	Directory *string `json:"directory,omitempty"`
}

func applyAdminRuntimePatch(c *config.Config, req patchRequest) (string, error) {
	switch req.Op {
	case "admin_console_set":
		if req.Enabled == nil {
			return "", fmt.Errorf("admin_console_set: enabled is required")
		}
		value := *req.Enabled
		c.Admin.Console = &value
		if value {
			return "admin Console enabled", nil
		}
		return "admin Console disabled", nil

	case "admin_plugin_upload_set":
		if req.AdminPluginUpload == nil {
			return "", fmt.Errorf("admin_plugin_upload_set: plugin_upload payload is required")
		}
		patch := req.AdminPluginUpload
		var changed []string
		if patch.Enabled != nil {
			value := *patch.Enabled
			c.Admin.PluginUploadEnabled = &value
			changed = append(changed, "enabled")
		}
		if patch.MaxSizeMB != nil {
			if *patch.MaxSizeMB < 0 {
				return "", fmt.Errorf("admin_plugin_upload_set: max_size_mb must be non-negative")
			}
			c.Admin.PluginUploadMaxSize = *patch.MaxSizeMB
			changed = append(changed, "max_size_mb")
		}
		if patch.Directory != nil {
			dir := strings.TrimSpace(*patch.Directory)
			if dir == "" {
				return "", fmt.Errorf("admin_plugin_upload_set: directory must not be empty")
			}
			c.Admin.PluginUploadDir = dir
			changed = append(changed, "directory")
		}
		if len(changed) == 0 {
			return "", fmt.Errorf("admin_plugin_upload_set: at least one field is required")
		}
		sort.Strings(changed)
		// Audit/preview summary intentionally names fields, never directory values.
		return "admin plugin upload policy updated (" + strings.Join(changed, ", ") + ")", nil
	}
	return "", fmt.Errorf("unsupported admin runtime patch operation %q", req.Op)
}

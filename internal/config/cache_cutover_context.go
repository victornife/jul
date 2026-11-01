// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"
	"fmt"
)

type cacheCutoverContextKey struct{}

// ValidateCacheCutoverMode validates an ephemeral preference for one live change.
// Staged changes use the configured durable reload_policy instead.
func ValidateCacheCutoverMode(mode, applyMode string) error {
	if mode != "" && mode != "retain" && mode != "invalidate" {
		return fmt.Errorf("cache_cutover must be retain or invalidate")
	}
	if mode != "" && applyMode == "stage_restart" {
		return fmt.Errorf("cache_cutover is a live-change option; configure reload_policy for a staged restart")
	}
	return nil
}

func WithCacheCutoverMode(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, cacheCutoverContextKey{}, mode)
}

func CacheCutoverMode(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	mode, _ := ctx.Value(cacheCutoverContextKey{}).(string)
	return mode
}

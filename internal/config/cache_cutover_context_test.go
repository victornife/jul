// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"
	"strings"
	"testing"
)

func TestValidateCacheCutoverMode(t *testing.T) {
	for _, tc := range []struct {
		name, mode, applyMode, wantError string
	}{
		{name: "configured live policy"},
		{name: "retain live", mode: "retain", applyMode: "live"},
		{name: "invalidate live", mode: "invalidate", applyMode: "live"},
		{name: "configured staged policy", applyMode: "stage_restart"},
		{name: "retain cannot be staged", mode: "retain", applyMode: "stage_restart", wantError: "live-change option"},
		{name: "invalidate cannot be staged", mode: "invalidate", applyMode: "stage_restart", wantError: "live-change option"},
		{name: "unknown mode", mode: "discard", wantError: "must be retain or invalidate"},
		{name: "case is significant", mode: "INVALIDATE", wantError: "must be retain or invalidate"},
		{name: "whitespace is rejected", mode: " retain ", wantError: "must be retain or invalidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCacheCutoverMode(tc.mode, tc.applyMode)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("valid cutover rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantError)
			}
		})
	}
}

func TestCacheCutoverModeDefaults(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background(), context.TODO()} {
		if got := CacheCutoverMode(ctx); got != "" {
			t.Fatalf("context without an override returned %q", got)
		}
	}
}

func TestCacheCutoverModeIsScopedToChange(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	retain := WithCacheCutoverMode(parent, "retain")
	invalidate := WithCacheCutoverMode(retain, "invalidate")
	configured := WithCacheCutoverMode(invalidate, "")
	for _, tc := range []struct {
		ctx  context.Context
		want string
	}{
		{parent, ""},
		{retain, "retain"},
		{invalidate, "invalidate"},
		{configured, ""},
	} {
		if got := CacheCutoverMode(tc.ctx); got != tc.want {
			t.Fatalf("cutover = %q, want %q", got, tc.want)
		}
	}
	cancel()
	for _, ctx := range []context.Context{retain, invalidate, configured} {
		if ctx.Err() != context.Canceled {
			t.Fatalf("cutover context lost parent cancellation: %v", ctx.Err())
		}
	}
}

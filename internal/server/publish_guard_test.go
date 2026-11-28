// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPublishGuardReleasesChecksInReverseOrderOnFailure(t *testing.T) {
	guard := NewPublishGuard()
	ctx := WithPublishGuard(context.Background(), guard)
	var released []int
	RegisterPublishCheck(ctx, func() (func(), error) {
		return func() { released = append(released, 1) }, nil
	})
	RegisterPublishCheck(ctx, func() (func(), error) {
		return func() { released = append(released, 2) }, errors.New("publication preflight failed")
	})

	if _, err := guard.PauseAndCheck(); err == nil {
		t.Fatal("failed preflight returned no error")
	}
	if !reflect.DeepEqual(released, []int{2, 1}) {
		t.Fatalf("release order = %v, want [2 1]", released)
	}
}

func TestPublishGuardSuccessfulChecksReleaseOnce(t *testing.T) {
	ctx := context.Background()
	if RegisterPublishCheck(ctx, func() (func(), error) {
		return nil, nil
	}) {
		t.Fatal("registered a check without a publish guard")
	}

	guard := NewPublishGuard()
	ctx = WithPublishGuard(ctx, guard)
	released := 0
	RegisterPublishCheck(ctx, func() (func(), error) {
		return nil, nil
	})
	RegisterPublishCheck(ctx, func() (func(), error) {
		return func() { released++ }, nil
	})
	release, err := guard.PauseAndCheck()
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	release()
	release()
	if released != 1 {
		t.Fatalf("release callback ran %d times, want once", released)
	}
	if _, err := guard.PauseAndCheck(); err == nil {
		t.Fatal("reused a completed publish guard")
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"fmt"
	"sync"
)

type publishGuardKey struct{}

type PublishGuard struct {
	mu     sync.Mutex
	checks []func() (func(), error)
	paused bool
}

func NewPublishGuard() *PublishGuard {
	return &PublishGuard{}
}

func WithPublishGuard(ctx context.Context, guard *PublishGuard) context.Context {
	return context.WithValue(ctx, publishGuardKey{}, guard)
}

func RegisterPublishCheck(ctx context.Context, check func() (func(), error)) bool {
	guard, _ := ctx.Value(publishGuardKey{}).(*PublishGuard)
	if guard == nil {
		return false
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.paused {
		panic("publish check registered after the publish guard was paused")
	}
	guard.checks = append(guard.checks, check)
	return true
}

func (g *PublishGuard) PauseAndCheck() (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	if g.paused {
		g.mu.Unlock()
		return nil, fmt.Errorf("publish guard already used")
	}
	g.paused = true
	checks := append([]func() (func(), error)(nil), g.checks...)
	g.mu.Unlock()

	var releases []func()
	for _, check := range checks {
		release, err := check()
		if release != nil {
			releases = append(releases, release)
		}
		if err != nil {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		})
	}, nil
}

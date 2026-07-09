// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import "io"

// PoolStager abstracts the generational staging span of the upstream pool
// registry (Begin -> Commit|Abort) so GenerationResources can orchestrate the
// handler-tree lifecycle without depending on the concrete registry type. The
// concrete *upstream.Registry satisfies it, and tests can substitute a fake to
// assert the ordering of the staging calls.
type PoolStager interface {
	// Begin opens a new staging generation of upstream pools.
	Begin()
	// Commit promotes the staged pools and closes pools the previous generation
	// no longer needs.
	Commit()
	// Abort discards the staged pools, closing any freshly created ones.
	Abort()
}

// GenerationResources owns the generational teardown lifecycle of the handler
// tree that the composition root rebuilds on every reload. Two kinds of resource
// have a generational lifetime:
//
//   - the upstream pool registry's staging span (Begin -> Commit|Abort), and
//   - the io.Closer set of the currently serving generation: gRPC-transcoding
//     backend connections, WASM plugin sets, and static-root directory handles.
//
// A build opens a generation with Begin, stages that generation's closers with
// Stage, and finishes with exactly one of Commit or Abort. Commit promotes the
// staged generation — committing the pools and adopting its closers as the live
// set — and returns a retire callback that closes the PREVIOUS generation's
// closers. The server invokes that callback only after the previous generation
// has drained, so a resource is never closed while an in-flight request still
// uses it. Abort discards the staged generation: it aborts the staged pools and
// closes the staged closers, leaving the live generation untouched. CloseLive
// tears down the final serving generation on shutdown.
//
// GenerationResources assumes a single in-flight generation at a time; the
// composition root serialises builds with a mutex, matching the single-writer
// invariant of the pool registry's staging span.
type GenerationResources struct {
	pools PoolStager
	live  []io.Closer
}

// NewGenerationResources binds the generational lifecycle to a pool stager.
func NewGenerationResources(pools PoolStager) *GenerationResources {
	return &GenerationResources{pools: pools}
}

// Generation is a single staging span opened by GenerationResources.Begin. The
// build stages its closers on it and finishes with exactly one of Commit or
// Abort; Abort is idempotent and a no-op once Commit has run, so the caller can
// `defer gen.Abort()` unconditionally and still Commit on the success path.
type Generation struct {
	parent    *GenerationResources
	staged    []io.Closer
	committed bool
}

// Begin opens a new staging generation, starting the pool registry's staging
// span. The caller must finish it with exactly one of Commit or Abort.
func (g *GenerationResources) Begin() *Generation {
	g.pools.Begin()
	return &Generation{parent: g}
}

// CloseLive tears down the currently serving generation's closers. The
// composition root defers it so the final live generation is released on
// shutdown.
func (g *GenerationResources) CloseLive() {
	for _, c := range g.live {
		_ = c.Close()
	}
}

// Stage registers a closer built in this generation for generational teardown. A
// nil closer is ignored so a caller can stage the result of an io.Closer type
// assertion without a separate guard.
func (g *Generation) Stage(c io.Closer) {
	if c == nil {
		return
	}
	g.staged = append(g.staged, c)
}

// Commit promotes this generation: it commits the staged pools, adopts the
// staged closers as the live set, and returns a retire callback that closes the
// PREVIOUS generation's closers. The retire callback must be invoked only after
// the previous generation has drained, so a resource is never closed while an
// in-flight request still uses it. Commit must be called at most once; a second
// call, or an Abort after Commit, is a no-op that returns a no-op callback.
func (g *Generation) Commit() func() {
	if g.committed {
		return func() {}
	}
	g.parent.pools.Commit()
	prev := g.parent.live
	g.parent.live = g.staged
	g.committed = true
	return func() {
		for _, c := range prev {
			_ = c.Close()
		}
	}
}

// Abort discards this generation: it aborts the staged pools and closes the
// staged closers, leaving the live generation untouched. It is a no-op once
// Commit has promoted the generation, so `defer gen.Abort()` is safe on the
// success path.
func (g *Generation) Abort() {
	if g.committed {
		return
	}
	g.parent.pools.Abort()
	for _, c := range g.staged {
		_ = c.Close()
	}
	g.staged = nil
}

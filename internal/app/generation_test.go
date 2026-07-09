// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These characterization tests pin the generational teardown semantics extracted
// from serve() into GenerationResources (ADR-0007, SEQ-04): the pool registry's
// Begin -> Commit|Abort staging span and the live/staged io.Closer sets. They
// must keep passing so the refactor preserves reload correctness.
package app

import "testing"

// fakeStager records the Begin/Commit/Abort calls so a test can assert the pool
// registry's staging span is driven exactly once per generation.
type fakeStager struct {
	begin, commit, abort int
}

func (f *fakeStager) Begin()  { f.begin++ }
func (f *fakeStager) Commit() { f.commit++ }
func (f *fakeStager) Abort()  { f.abort++ }

// fakeCloser counts how many times it was closed so a test can assert a resource
// is retired exactly once, and never while it is the live generation.
type fakeCloser struct {
	closed int
}

func (c *fakeCloser) Close() error { c.closed++; return nil }

func TestGenerationCommitPromotesAndRetiresPrevious(t *testing.T) {
	st := &fakeStager{}
	gr := NewGenerationResources(st)

	// First generation: commit adopts c1 as the live set. Its retire callback
	// closes the (empty) previous generation, so c1 is untouched.
	gen1 := gr.Begin()
	if st.begin != 1 {
		t.Fatalf("Begin count = %d, want 1", st.begin)
	}
	c1 := &fakeCloser{}
	gen1.Stage(c1)
	retire1 := gen1.Commit()
	if st.commit != 1 {
		t.Fatalf("Commit count = %d, want 1", st.commit)
	}
	retire1()
	if c1.closed != 0 {
		t.Fatalf("live closer c1 closed %d times after first commit, want 0", c1.closed)
	}

	// Second generation: its retire callback closes the PREVIOUS live set (c1)
	// exactly once; the new live set (c2) is untouched until shutdown.
	gen2 := gr.Begin()
	c2 := &fakeCloser{}
	gen2.Stage(c2)
	retire2 := gen2.Commit()
	if st.begin != 2 || st.commit != 2 {
		t.Fatalf("after second commit: begin=%d commit=%d, want 2/2", st.begin, st.commit)
	}
	retire2()
	if c1.closed != 1 {
		t.Fatalf("retired closer c1 closed %d times, want 1", c1.closed)
	}
	if c2.closed != 0 {
		t.Fatalf("live closer c2 closed %d times before shutdown, want 0", c2.closed)
	}

	// Shutdown tears down the final live generation.
	gr.CloseLive()
	if c2.closed != 1 {
		t.Fatalf("c2 closed %d times after CloseLive, want 1", c2.closed)
	}
	if st.abort != 0 {
		t.Fatalf("Abort called %d times on the all-commit path, want 0", st.abort)
	}
}

func TestGenerationAbortClosesStagedLeavesLive(t *testing.T) {
	st := &fakeStager{}
	gr := NewGenerationResources(st)

	gen1 := gr.Begin()
	c1 := &fakeCloser{}
	gen1.Stage(c1)
	gen1.Commit() // live = [c1]

	// A rejected reload aborts its staged generation: the staged closer is torn
	// down and the pool span aborted, while the live generation is untouched.
	gen2 := gr.Begin()
	c2 := &fakeCloser{}
	gen2.Stage(c2)
	gen2.Abort()
	if st.abort != 1 {
		t.Fatalf("Abort count = %d, want 1", st.abort)
	}
	if c2.closed != 1 {
		t.Fatalf("aborted staged closer c2 closed %d times, want 1", c2.closed)
	}
	if c1.closed != 0 {
		t.Fatalf("live closer c1 closed %d times after aborting a later generation, want 0", c1.closed)
	}

	gr.CloseLive()
	if c1.closed != 1 {
		t.Fatalf("c1 closed %d times after CloseLive, want 1", c1.closed)
	}
}

func TestGenerationAbortAfterCommitIsNoop(t *testing.T) {
	st := &fakeStager{}
	gr := NewGenerationResources(st)

	gen := gr.Begin()
	c := &fakeCloser{}
	gen.Stage(c)
	_ = gen.Commit()

	// serve() unconditionally `defer gen.Abort()`; after a commit that abort must
	// be inert so the promoted (now live) closer is not torn down early and the
	// pool span is not aborted after being committed.
	gen.Abort()
	gen.Abort()
	if st.abort != 0 {
		t.Fatalf("Abort ran %d times after commit, want 0", st.abort)
	}
	if c.closed != 0 {
		t.Fatalf("live closer closed %d times by a post-commit abort, want 0", c.closed)
	}
}

func TestGenerationPreflightAbortDiscardsStaged(t *testing.T) {
	st := &fakeStager{}
	gr := NewGenerationResources(st)

	// A preflight build (commit == false) stages closers then aborts: the staged
	// closers are released and the live set stays empty.
	gen := gr.Begin()
	c := &fakeCloser{}
	gen.Stage(c)
	gen.Abort()
	if st.abort != 1 || c.closed != 1 {
		t.Fatalf("preflight abort: abort=%d closed=%d, want 1/1", st.abort, c.closed)
	}

	// Nothing was promoted, so shutdown closes nothing and must not panic.
	gr.CloseLive()
	if c.closed != 1 {
		t.Fatalf("closer closed %d times after CloseLive, want 1", c.closed)
	}
}

func TestGenerationCommitIsIdempotent(t *testing.T) {
	st := &fakeStager{}
	gr := NewGenerationResources(st)

	gen := gr.Begin()
	c := &fakeCloser{}
	gen.Stage(c)
	_ = gen.Commit()
	retire2 := gen.Commit() // second Commit must be a no-op returning a no-op retire

	if st.commit != 1 {
		t.Fatalf("Commit ran %d times, want 1", st.commit)
	}
	retire2()
	if c.closed != 0 {
		t.Fatalf("no-op retire closed the live closer %d times, want 0", c.closed)
	}
}

func TestGenerationStageNilIsIgnored(t *testing.T) {
	st := &fakeStager{}
	gr := NewGenerationResources(st)

	gen := gr.Begin()
	gen.Stage(nil)
	retire := gen.Commit()
	retire()
	gr.CloseLive() // must not panic on a nil-staged generation
}

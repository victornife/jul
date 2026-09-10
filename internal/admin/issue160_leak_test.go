// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

func TestIssue160NoGoroutineOrOwnerLeakAcrossChurn(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	root := t.TempDir()
	a := newAuditLog(64)
	var prior *auditFileOwner
	for i := 0; i < 40; i++ {
		path := filepath.Join(root, fmt.Sprintf("audit-%d.jsonl", i%3))
		p, err := a.prepareTransition(mustAuditCfg(t, path, 1+(i%2), 2+(i%4)))
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			p.commit()
			p.retire(context.Background())
		}
		a.record(AuditEvent{Operation: "churn", Result: "success"})
		if a.currentSink != nil {
			a.currentSink.owner.lifeMu.Lock()
			refs, closed := a.currentSink.owner.refs, a.currentSink.owner.closed
			a.currentSink.owner.lifeMu.Unlock()
			if refs != 1 || closed {
				t.Fatalf("iteration %d active owner refs=%d closed=%v", i, refs, closed)
			}
			prior = a.currentSink.owner
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if prior != nil {
		prior.lifeMu.Lock()
		refs, closed := prior.refs, prior.closed
		prior.lifeMu.Unlock()
		if refs != 0 || !closed {
			t.Fatalf("final owner refs=%d closed=%v", refs, closed)
		}
	}
}

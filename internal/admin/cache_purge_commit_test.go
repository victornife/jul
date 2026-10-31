// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
)

type cutoffSnapshotPurger struct {
	*cache.Cache
	t        *testing.T
	src, dst string
}

func (p *cutoffSnapshotPurger) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	commit := opts.CommitFence
	opts.CommitFence = func(gen uint64, ids []string, intent cache.PurgeRecoveryIntent) error {
		if err := commit(gen, ids, intent); err != nil {
			return err
		}
		entries, err := os.ReadDir(p.src)
		if err != nil {
			p.t.Fatal(err)
		}
		for _, e := range entries {
			// Prove that the actual durable ledger independently carries identity.
			if e.IsDir() || e.Name() == ".jul-cache-purge-intents" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(p.src, e.Name()))
			if err != nil {
				p.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p.dst, e.Name()), data, 0600); err != nil {
				p.t.Fatal(err)
			}
		}
		cancel()
		return nil
	}
	return p.Cache.InvalidateScope(ctx, sel, opts)
}

func TestV1PurgeCutoffLedgerCarriesExecutablePrivateIdentity(t *testing.T) {
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 2 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20, DefaultTTL: config.Duration(time.Hour)}
	c, err := cache.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("old"))
	}))
	for _, method := range []string{"GET", "HEAD"} {
		old.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "http://example.com/article?secret=PRIVATE", nil))
	}
	snap := t.TempDir()
	p := &cutoffSnapshotPurger{Cache: c, t: t, src: cfg.DiskPath, dst: snap}
	s := newTestServer(t, config.AdminConfig{}, Deps{Cache: p, LoadConfig: func() (*config.Config, error) { return &config.Config{Cache: cfg}, nil }})
	rr := postV1JSON(t, s, "/api/v1/cache/purge", "", "", adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "key", Key: "GET\nexample.com\n/article?secret=PRIVATE"}})
	if rr.Code != 200 {
		t.Fatalf("purge=%d %s", rr.Code, rr.Body.String())
	}
	ledger, err := os.ReadFile(filepath.Join(snap, ".jul-cache-purge-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(ledger, []byte(`"recovery_intent"`)) || bytes.Contains(ledger, []byte("PRIVATE")) {
		t.Fatalf("ledger missing private recovery identity: %s", ledger)
	}
	cfg.DiskPath = snap
	for boot := 0; boot < 3; boot++ {
		restarted, err := cache.New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		h := restarted.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte("fresh"))
		}))
		get := httptest.NewRecorder()
		h.ServeHTTP(get, httptest.NewRequest("GET", "http://example.com/article?secret=PRIVATE", nil))
		if get.Body.String() != "fresh" {
			t.Fatal("old body survived ledger-only recovery")
		}
		head := httptest.NewRecorder()
		h.ServeHTTP(head, httptest.NewRequest("HEAD", "http://example.com/article?secret=PRIVATE", nil))
		if head.Header().Get("X-Cache") != "HIT" {
			t.Fatal("ledger recovery broadened key purge to HEAD")
		}
	}
}

type capacityPurger struct{ errScopedPurger }

func (capacityPurger) InvalidateScope(context.Context, cache.NormalizedSelector, cache.InvalidateOptions) (cache.InvalidateResult, error) {
	return cache.InvalidateResult{}, fmt.Errorf("%w: %w", cache.ErrFenceCommit, cache.ErrPurgeIntentCapacity)
}
func TestPurgeCapacityReturnsBackpressureAndGuidance(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{Cache: capacityPurger{}})
	rr := postV1JSON(t, s, "/api/v1/cache/purge", "", "", adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "all", All: true}, ConfirmBroad: true})
	if rr.Code != 503 || !strings.Contains(rr.Body.String(), "capacity exhausted") {
		t.Fatalf("versioned capacity response: %d %s", rr.Code, rr.Body.String())
	}
}

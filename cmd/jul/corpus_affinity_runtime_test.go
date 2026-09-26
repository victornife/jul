// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"jul/internal/affinity"
)

// TestNGINXCorpusHashAffinityRealE2E drives the imported
// upstream-hash-affinity-runtime candidate (NGINX `hash $http_x_tenant
// consistent` over three Unix-socket members) through a real Jul, twice:
//
//   - every tenant sticks to one backend across repeated requests;
//   - that backend is the rendezvous_v1 winner for the tenant, so the mapping
//     is the frozen contract rather than an accident of this process;
//   - tenants spread over every member, and empty-key requests are
//     round-robined rather than pinned (the NGINX behavior too);
//   - a second Jul process built from the same candidate maps every tenant
//     identically (cross-restart determinism).
//
// NGINX's own ketama placement is compared per tenant in the pinned reference
// lane (scripts/nginx-migration-e2e.sh); where it differs it is recorded there
// as the expected difference NGX_UPSTREAM_HASH.
func TestNGINXCorpusHashAffinityRealE2E(t *testing.T) {
	members := []string{"/tmp/jul432/a.sock", "/tmp/jul432/b.sock", "/tmp/jul432/c.sock"}
	cands := make([]affinity.Candidate, len(members))
	for i, m := range members {
		cands[i] = affinity.NewCandidate("unix", m, 1)
	}
	want := func(tenant string) string {
		return string("abc"[affinity.Rank(affinity.Sum(tenant), cands)[0]])
	}

	run := func(round int) map[string]string {
		cfg := loadCorpusRuntimeCandidate(t, "upstream-hash-affinity-runtime")
		baseURL, cleanup := startRealJulForCorpus(t, "upstream-hash-affinity-runtime", cfg)
		defer cleanup()
		client := &http.Client{Timeout: 2 * time.Second}
		defer client.CloseIdleConnections()

		get := func(tenant string) string {
			req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "affinity.test"
			if tenant != "" {
				req.Header.Set("X-Tenant", tenant)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("round %d tenant %q: %v", round, tenant, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("round %d tenant %q: status %d", round, tenant, resp.StatusCode)
			}
			return resp.Header.Get("X-Corpus-Backend-Id")
		}

		placement := map[string]string{}
		used := map[string]int{}
		for i := 0; i < 60; i++ {
			tenant := "tenant-" + strconv.Itoa(i)
			first := get(tenant)
			for j := 0; j < 2; j++ {
				if again := get(tenant); again != first {
					t.Fatalf("round %d: %s moved %s -> %s between requests", round, tenant, first, again)
				}
			}
			if first != want(tenant) {
				t.Fatalf("round %d: %s on %s, rendezvous_v1 says %s", round, tenant, first, want(tenant))
			}
			placement[tenant] = first
			used[first]++
		}
		if len(used) != 3 {
			t.Fatalf("round %d: 60 tenants used %d backends: %v", round, len(used), used)
		}
		empty := map[string]bool{}
		for i := 0; i < 6; i++ {
			empty[get("")] = true
		}
		if len(empty) != 3 {
			t.Fatalf("round %d: empty-key requests reached %d backends, want all 3 (round robin)", round, len(empty))
		}
		t.Logf("round %d placement spread: %v", round, used)
		return placement
	}

	first := run(1)
	second := run(2)
	var moved []string
	for tenant, backend := range first {
		if second[tenant] != backend {
			moved = append(moved, tenant)
		}
	}
	if len(moved) > 0 {
		t.Fatalf("a restarted Jul re-placed %d tenants: %s", len(moved), strings.Join(moved, ", "))
	}
}

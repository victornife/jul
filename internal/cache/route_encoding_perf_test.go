// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/versionid"
)

// Captured from 6888e58 before replacing formatted framing. Encoding changes
// would discard warm disk reuse, so pin both scopes and private matcher proofs.
func TestRouteEncodingCompatibility(t *testing.T) {
	value := "synthetic:value\n入口"
	id := "catalog:\n入口"
	longID := strings.Repeat("route:\n雪", 100)
	srv := config.ServerConfig{
		Listen:      "[2001:db8::1]:8080",
		ServerNames: []string{"WWW.EXAMPLE.COM.", "example.com", "*.Example.ORG"},
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Path: "/"}},
			{RouteID: &id, Match: config.MatchConfig{Type: "exact", Path: "/入口:\n", Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &value}}}},
			{Match: config.MatchConfig{Path: "/café", Methods: []string{"GET"}}},
			{Match: config.MatchConfig{Type: "prefix", Path: "/café", Methods: []string{"HEAD"}}, CORS: &config.CORSConfig{Enabled: true}},
			{Match: config.MatchConfig{Path: "/" + strings.Repeat("x:\n雪", 100)}},
			{RouteID: &longID, Match: config.MatchConfig{Path: "/", Methods: []string{}}},
		},
	}
	want := []struct{ scope, tag string }{
		{"d0c5ab4f360d836431b4dc29be4ad32f", "e472ce1e59ad8950ac1ef472ee4ed23c81b13dbb3860ea10f6b8a1dba18e205b"},
		{"96bb23ad972eb6e7683fe42255effae3", "3bed553fbf22f1ac237e17c6c6fb977cb65451228a3baf226cef3747934f99fb"},
		{"55ec30a46906937267621dd69d83d0a6", "99cd039e62cb9e47f0d15e8c40990b8b19d94fed4d8238580f7c6e8f9a22fe77"},
		{"f9752031439af1e90e7ae31fa2526754", "d35a10160b01881628422cec13de93b4f3e0ea2718081c23f3625b5c2b477a36"},
		{"b2d2b1da44a71390035c71d19fe4b9f4", "3665cd08e38b8ba645ec7675c4c3a0eb6847e324afc73f1297cced82fbd142d5"},
		{"cedd245aff13d40e388aa7b4c3646cb8", "ed73727f708d2ca6d5319643ff10de6ee3321169f4bee15559c5c1724b947506"},
	}
	ctx, err := versionid.New(bytes.Repeat([]byte{0x73}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for i, scope := range RouteScopes(srv) {
		if scope != want[i].scope || RouteScope(srv, srv.Locations[i]) != scope {
			t.Errorf("route %d: persistent scope encoding changed", i)
		}
		rc := CompileRouteCacheConfig(scope, true, nil).WithMatcher(srv.Locations[i], ctx)
		if rc.matcherProof != "ev1:664d8584f62a0401964404a106a28ed2:"+want[i].tag {
			t.Errorf("route %d: private compatibility encoding changed", i)
		}
	}
}

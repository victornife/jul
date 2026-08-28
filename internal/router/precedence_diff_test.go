// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"jul/internal/config"
)

// This file is the differential precedence gate ADR 0018 and #145 require to
// land before any router change: the old selector and the new tiered
// enumeration must choose the identical location for every predicate-free
// configuration, over every checked-in TOML plus generated pathological ones.
//
// Route precedence has the largest blast radius of anything in the record. Every
// other failure mode here produces a visible error; a precedence regression
// produces a working server routing traffic to the wrong backend, which is the
// failure that is not noticed until it matters. The harness is worth far more
// failing before the change than passing after it.
//
// legacySelect below is a frozen copy of serverRoute.matchLocation as it stood
// at 90ac9f5c. It is deliberately a copy and deliberately test-only: it will
// never change again, and the alternative — keeping it in the production
// package — ships a second selector that a future reader could call.

// legacySelect is the pre-ADR-0018 algorithm: exact, then longest non-root
// prefix, then regex in declaration order, then the "/" fallback — which was
// reassigned on every `prefix "/"` location, so the LAST one won, and which was
// returned without checking that the path was a prefix of it at all.
func legacySelect(s *serverRoute, path string) *locationRoute {
	for _, loc := range s.locations {
		if loc.matchType == "exact" && loc.path == path {
			return loc
		}
	}
	var best *locationRoute
	bestLen := -1
	for _, loc := range s.locations {
		if loc.matchType != "prefix" || loc.path == "/" {
			continue
		}
		if strings.HasPrefix(path, loc.path) && len(loc.path) > bestLen {
			best = loc
			bestLen = len(loc.path)
		}
	}
	if best != nil {
		return best
	}
	for _, loc := range s.locations {
		if loc.matchType == "regex" && loc.re.MatchString(path) {
			return loc
		}
	}
	return legacyFallback(s)
}

// legacyFallback reproduces buildServerRoute's old sr.fallback assignment: the
// field was overwritten by each `prefix "/"` location in turn.
func legacyFallback(s *serverRoute) *locationRoute {
	var fallback *locationRoute
	for _, loc := range s.locations {
		if loc.matchType == "prefix" && loc.path == "/" {
			fallback = loc
		}
	}
	return fallback
}

// matchServerRoute compiles a server block's matching structures without
// building any handler, so the harness can run over configurations whose
// upstreams, roots and sockets do not exist on this machine.
func matchServerRoute(t testing.TB, srv config.ServerConfig) *serverRoute {
	t.Helper()
	sr := &serverRoute{names: srv.ServerNames}
	for i, loc := range srv.Locations {
		lr := &locationRoute{matchType: loc.Match.Type, path: loc.Match.Path, index: i}
		if loc.Match.Type == "regex" {
			re, err := regexp.Compile(loc.Match.Path)
			if err != nil {
				t.Skipf("location regex %q does not compile: %v", loc.Match.Path, err)
			}
			lr.re = re
		}
		predicates, err := compilePredicates(loc)
		if err != nil {
			t.Fatalf("compile predicates for %q: %v", loc.Match.Path, err)
		}
		lr.predicates = predicates
		sr.locations = append(sr.locations, lr)
	}
	sr.indexLocations()
	return sr
}

// hasPredicates reports whether any location in the block constrains more than
// its path. The equivalence assertion only applies to predicate-free blocks:
// a predicate is by definition a behaviour the old selector cannot express.
func hasPredicates(srv config.ServerConfig) bool {
	for _, loc := range srv.Locations {
		if loc.Match.HasPredicates() {
			return true
		}
	}
	return false
}

// rootLocationCount counts `prefix "/"` locations, which is the one shape whose
// divergence is permitted.
func rootLocationCount(srv config.ServerConfig) int {
	n := 0
	for _, loc := range srv.Locations {
		if loc.Match.Type == "prefix" && loc.Match.Path == "/" {
			n++
		}
	}
	return n
}

// permittedDivergence is the explicitly enumerated allowlist. It is a property
// of the configuration, checked against the counterexample, rather than a
// runtime inference: a divergence that is merely plausible must still fail.
//
// Exactly two shapes may differ, and both are consequences ADR 0018 states.
//
//  1. Duplicate `prefix "/"`. The old fallback field was reassigned by each one
//     in turn so the last declared won, while lint.go has always named the first
//     as the winner. Tier 4 makes the router agree with the lint.
//  2. A request path that is not rooted. The old fallback was returned without
//     testing that the path was a prefix of "/" at all, so an authority-form
//     CONNECT (empty path) or a server-wide OPTIONS ("*") reached the catch-all.
//     §2 depends on neither matching any tier, so both now 404.
func permittedDivergence(srv config.ServerConfig, path string, old, current *locationRoute) (string, bool) {
	if !strings.HasPrefix(path, "/") {
		if current == nil && old != nil && old.path == "/" {
			return "non-rooted path no longer reaches the catch-all (ADR 0018 §2)", true
		}
		return "", false
	}
	if rootLocationCount(srv) > 1 && old != nil && current != nil &&
		old.path == "/" && current.path == "/" && current.index < old.index {
		return "duplicate `prefix \"/\"` now resolves to the first declared (ADR 0018 §6)", true
	}
	return "", false
}

// requestPaths derives the probe set for a server block: every configured path,
// plus the boundary variants a precedence bug hides in.
func requestPaths(srv config.ServerConfig) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || !strings.HasPrefix(p, "/") || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add("/")
	add("/jul-differential-matches-nothing")
	for _, loc := range srv.Locations {
		if loc.Match.Type == "regex" {
			// The "path" is a pattern, not a path; probing it verbatim tests
			// nothing. The literal probes and the other locations cover the
			// regex tier.
			continue
		}
		p := loc.Match.Path
		add(p)                                // the configured path itself
		add(strings.TrimSuffix(p, "/"))       // without a trailing slash
		add(p + "/")                          // with one
		add(p + "/leaf")                      // a trailing segment
		add(p + "x")                          // a longer path that still has it as a prefix
		add(parentPath(p))                    // the parent
		add(strictPrefix(p))                  // a strict prefix of a configured prefix
		add(p + "/index.php")                 // reaches the regex tier in the usual shapes
		add(p + "/asset.png")                 //
		add(strings.ToUpper(p))               // case, which paths are sensitive to
		add(strings.ReplaceAll(p, "/", "//")) // doubled separators
	}
	sort.Strings(out)
	return out
}

func parentPath(p string) string {
	trimmed := strings.TrimSuffix(p, "/")
	i := strings.LastIndex(trimmed, "/")
	if i <= 0 {
		return "/"
	}
	return trimmed[:i]
}

func strictPrefix(p string) string {
	trimmed := strings.TrimSuffix(p, "/")
	if len(trimmed) <= 1 {
		return "/"
	}
	return trimmed[:len(trimmed)-1]
}

// describe renders a location for a counterexample.
func describe(loc *locationRoute) string {
	if loc == nil {
		return "<no match, 404>"
	}
	return fmt.Sprintf("%s %q (declared at index %d)", normalizedMatchType(loc.matchType), loc.path, loc.index)
}

// tierOf reports which tier of the new enumeration a location was found at,
// which is the other half of a useful counterexample.
func tierOf(s *serverRoute, path string, want *locationRoute) int {
	tier := 0
	s.eachCandidate(path, func(candidateTier int, loc *locationRoute) bool {
		if loc == want {
			tier = candidateTier
			return true
		}
		return false
	})
	return tier
}

// assertSelectorsAgree is the equivalence assertion. On failure it prints the
// whole counterexample: a bisected precedence bug with no counterexample is
// hours of work; with one it is minutes.
func assertSelectorsAgree(t *testing.T, source string, srv config.ServerConfig) {
	t.Helper()
	if hasPredicates(srv) {
		return
	}
	sr := matchServerRoute(t, srv)
	for _, path := range requestPaths(srv) {
		old := legacySelect(sr, path)
		current := sr.selectLocation(selectRequest(path, ""))
		if old == current {
			continue
		}
		if reason, ok := permittedDivergence(srv, path, old, current); ok {
			t.Logf("allowlisted divergence in %s for path %q: %s", source, path, reason)
			continue
		}
		t.Errorf(`selectors disagree
  configuration: %s
  server block:  listen %q names %v
  locations:     %s
  request path:  %q
  old selector:  %s
  new selector:  %s (tier %d)`,
			source, srv.Listen, srv.ServerNames, renderLocations(srv), path,
			describe(old), describe(current), tierOf(sr, path, current))
	}
}

func renderLocations(srv config.ServerConfig) string {
	parts := make([]string, 0, len(srv.Locations))
	for i, loc := range srv.Locations {
		parts = append(parts, fmt.Sprintf("[%d] %s %q", i, normalizedMatchType(loc.Match.Type), loc.Match.Path))
	}
	return strings.Join(parts, ", ")
}

// TestSelectorEquivalenceOverCheckedInConfigurations runs both selectors over
// every TOML configuration in the repository — the burn-in profiles, the apps
// and dev-server profiles, testdata and the examples.
func TestSelectorEquivalenceOverCheckedInConfigurations(t *testing.T) {
	files := checkedInConfigs(t)
	if len(files) < 40 {
		t.Fatalf("found %d checked-in TOML configurations, want the whole corpus; the harness is only as good as what it runs over", len(files))
	}
	blocks := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		cfg, err := config.Parse(data)
		if err != nil {
			// Not every checked-in TOML is a server configuration; the ones that
			// are not carry no locations to compare.
			continue
		}
		for _, srv := range cfg.Servers {
			if len(srv.Locations) == 0 {
				continue
			}
			blocks++
			assertSelectorsAgree(t, path, srv)
		}
	}
	if blocks == 0 {
		t.Fatal("no server blocks with locations were compared")
	}
	t.Logf("compared %d server blocks across %d configuration files", blocks, len(files))
}

// checkedInConfigs returns every *.toml under the repository root, excluding the
// scratch directories that are not part of the checked-in corpus.
func checkedInConfigs(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "tmp", "soak-artifacts", "third_party", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".toml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	sort.Strings(files)
	return files
}

// TestSelectorEquivalenceOverGeneratedConfigurations covers the pathological
// shapes the checked-in corpus does not contain on its own: overlapping exact,
// prefix and regex locations, duplicate coordinates, and several `prefix "/"`
// entries in one block.
func TestSelectorEquivalenceOverGeneratedConfigurations(t *testing.T) {
	types := []string{"exact", "prefix", "regex"}
	paths := []string{"/", "/api", "/api/", "/api/v1", "/api/v1/users", `\.png$`, `^/api`}
	pathFor := func(matchType, p string) string {
		if matchType == "regex" {
			if !strings.HasPrefix(p, `\`) && !strings.HasPrefix(p, "^") {
				return "^" + regexp.QuoteMeta(p)
			}
			return p
		}
		if strings.HasPrefix(p, `\`) || strings.HasPrefix(p, "^") {
			return "/literal"
		}
		return p
	}

	generated := 0
	// Every ordered triple of (type, path) locations, which covers overlap,
	// duplication and shadowing without needing a random generator.
	for _, t1 := range types {
		for _, p1 := range paths {
			for _, t2 := range types {
				for _, p2 := range paths {
					for _, t3 := range types {
						srv := config.ServerConfig{
							Listen: "127.0.0.1:80",
							Locations: []config.LocationConfig{
								{Match: config.MatchConfig{Type: t1, Path: pathFor(t1, p1)}, Return: 200},
								{Match: config.MatchConfig{Type: t2, Path: pathFor(t2, p2)}, Return: 200},
								{Match: config.MatchConfig{Type: t3, Path: pathFor(t3, p1)}, Return: 200},
							},
						}
						generated++
						assertSelectorsAgree(t, fmt.Sprintf("generated[%s %s | %s %s | %s]", t1, p1, t2, p2, t3), srv)
					}
				}
			}
		}
	}

	// Three `prefix "/"` locations in one block: the enumerated divergence, and
	// the only one the allowlist admits.
	roots := config.ServerConfig{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 201},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 202},
		},
	}
	sr := matchServerRoute(t, roots)
	if old := legacySelect(sr, "/anything"); old == nil || old.index != 2 {
		t.Fatalf("the frozen legacy selector should pick the LAST declared root, got %s", describe(old))
	}
	if current := sr.selectLocation(selectRequest("/anything", "")); current == nil || current.index != 0 {
		t.Fatalf("the new selector should pick the FIRST declared root, got %s", describe(current))
	}
	assertSelectorsAgree(t, "generated[three roots]", roots)
	t.Logf("compared %d generated server blocks", generated)
}

// TestSelectionIsStableAcrossRebuilds asserts order stability directly rather
// than inferring it from a passing run: the same configuration compiled again
// produces the same selection, and no map is consulted to make one.
func TestSelectionIsStableAcrossRebuilds(t *testing.T) {
	srv := config.ServerConfig{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, Return: 200},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, Return: 200},
			{Match: config.MatchConfig{Type: "exact", Path: "/api"}, Return: 200},
			{Match: config.MatchConfig{Type: "regex", Path: `\.png$`}, Return: 200},
			{Match: config.MatchConfig{Type: "regex", Path: `\.(png|jpg)$`}, Return: 200},
		},
	}
	paths := requestPaths(srv)
	want := map[string]int{}
	for _, path := range paths {
		sr := matchServerRoute(t, srv)
		loc := sr.selectLocation(selectRequest(path, ""))
		want[path] = indexOrMinusOne(loc)
	}
	for round := 0; round < 32; round++ {
		sr := matchServerRoute(t, srv)
		for _, path := range paths {
			loc := sr.selectLocation(selectRequest(path, ""))
			if got := indexOrMinusOne(loc); got != want[path] {
				t.Fatalf("round %d: path %q selected location %d, want %d; selection depends on something other than declaration order",
					round, path, got, want[path])
			}
		}
	}
}

func indexOrMinusOne(loc *locationRoute) int {
	if loc == nil {
		return -1
	}
	return loc.index
}

// TestPredicateFailureFallsThroughToTheNextCandidate is the property the whole
// enumeration exists for: the old selector committed at each tier, so a route
// that fails a predicate would have consumed the request.
func TestPredicateFailureFallsThroughToTheNextCandidate(t *testing.T) {
	srv := config.ServerConfig{
		Listen: "127.0.0.1:80",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/api/", Methods: []string{"POST"}}, Return: 201},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api/"}, Return: 200},
		},
	}
	sr := matchServerRoute(t, srv)

	post := selectRequest("/api/users", "")
	post.Method = http.MethodPost
	if loc := sr.selectLocation(post); loc == nil || loc.index != 0 {
		t.Fatalf("POST selected %s, want the method-constrained route", describe(loc))
	}
	if loc := sr.selectLocation(selectRequest("/api/users", "")); loc == nil || loc.index != 1 {
		t.Fatalf("GET selected %s, want the unconstrained route below it", describe(loc))
	}
}

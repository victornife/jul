// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
	"testing/fstest"

	"jul/internal/config"
)

// goModVersion reads a module's required version from the repository go.mod.
func goModVersion(t *testing.T, module string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(module) + `\s+(v\S+)`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("%s not required in go.mod", module)
	}
	return string(m[1])
}

func buildInfo(t *testing.T, cfg config.WAFConfig) PolicyInfo {
	t.Helper()
	applyTestDefaults(&cfg)
	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return fw.Info()
}

func TestEmbeddedCRSVersionIsTheLinkedRuleSet(t *testing.T) {
	got := EmbeddedCRSVersion()
	if got == "" {
		t.Fatal("embedded CRS version not readable from the rule set signature")
	}
	// The coraza-coreruleset module is versioned with the CRS it embeds.
	if want := strings.TrimPrefix(goModVersion(t, "github.com/corazawaf/coraza-coreruleset/v4"), "v"); got != want {
		t.Fatalf("EmbeddedCRSVersion = %q, want %q (the module Jul links)", got, want)
	}
	if want := goModVersion(t, "github.com/corazawaf/coraza/v3"); EngineVersion() != want {
		// Test binaries may carry no dependency build info; "" is then the
		// truthful answer rather than a guess.
		if info, ok := debug.ReadBuildInfo(); ok && EngineVersion() == "" {
			for _, d := range info.Deps {
				if d.Path == "github.com/corazawaf/coraza/v3" {
					t.Fatalf("build info lists coraza %s but EngineVersion is empty", d.Version)
				}
			}
			return
		}
		t.Fatalf("EngineVersion = %q, want %q", EngineVersion(), want)
	}
}

func TestPolicyInfoCRSDefaults(t *testing.T) {
	info := buildInfo(t, config.WAFConfig{Enabled: true, CRSEnabled: true})
	if info.Mode != "block" || info.BlockStatus != 403 || !info.CRSEnabled || info.CRSVersion != EmbeddedCRSVersion() {
		t.Fatalf("info = %+v", info)
	}
	if info.Paranoia != 1 || !info.ParanoiaDefault {
		t.Fatalf("default paranoia = %d (default %v)", info.Paranoia, info.ParanoiaDefault)
	}
	if info.RequestBodyLimitBytes != 128<<10 {
		t.Fatalf("request body limit = %d", info.RequestBodyLimitBytes)
	}
	r := info.Rules
	if r.Embedded < 100 || r.External != 0 || r.Inline != 0 || r.Generated != 0 || r.Total != r.Embedded {
		t.Fatalf("CRS rule counts = %+v", r)
	}
	if info.ExternalFiles != 0 || info.ExternalDigest != "" {
		t.Fatalf("CRS-only policy read external files: %d %q", info.ExternalFiles, info.ExternalDigest)
	}
}

func TestPolicyInfoParanoiaIsJulGenerated(t *testing.T) {
	info := buildInfo(t, config.WAFConfig{Enabled: true, CRSEnabled: true, Paranoia: 3, Mode: "detect", InlineRules: `SecRule ARGS:x "@streq y" "id:10001,phase:1,deny"`})
	if info.Paranoia != 3 || info.ParanoiaDefault || info.Mode != "detect" {
		t.Fatalf("info = %+v", info)
	}
	if info.Rules.Generated != 1 || info.Rules.Inline != 1 || !info.InlineRules {
		t.Fatalf("rule counts = %+v", info.Rules)
	}
	if got := info.Rules.Embedded + info.Rules.External + info.Rules.Inline + info.Rules.Generated; got != info.Rules.Total {
		t.Fatalf("classes sum %d != total %d", got, info.Rules.Total)
	}
}

func TestPolicyInfoInlineOnly(t *testing.T) {
	info := buildInfo(t, config.WAFConfig{
		Enabled:           true,
		BlockStatus:       418,
		ResponseBodyCheck: true,
		InlineRules: `SecRule ARGS:a "@streq b" "id:10001,phase:1,deny"
SecAction "id:10002,phase:1,pass,nolog"`,
	})
	if info.CRSEnabled || info.CRSVersion != "" || info.Paranoia != 0 || info.BlockStatus != 418 || !info.ResponseBodyInspection {
		t.Fatalf("info = %+v", info)
	}
	if info.Rules != (RuleCounts{Total: 2, Inline: 2}) {
		t.Fatalf("rule counts = %+v", info.Rules)
	}
}

// Same path, different bytes: the digest must change, so an in-place rule
// edit picked up by a reload is observable (#440, the #429 lesson).
func TestPolicyInfoExternalContentIdentity(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "bad-words.data")
	rules := filepath.Join(dir, "rules.conf")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(data, "evil\n")
	write(rules, fmt.Sprintf(`SecRule ARGS "@pmFromFile %s" "id:20001,phase:2,deny,status:403"
SecRule REQUEST_URI "@contains /admin" "id:20002,phase:1,deny,status:403"
`, data))
	cfg := config.WAFConfig{Enabled: true, DirectivesFiles: []string{rules, " "}}

	first := buildInfo(t, cfg)
	if first.RuleFilesConfigured != 1 || first.ExternalFiles != 2 || first.Rules.External != 2 || first.Rules.Total != 2 {
		t.Fatalf("external info = %+v", first)
	}
	if !strings.HasPrefix(first.ExternalDigest, "sha256:") || len(first.ExternalDigest) != len("sha256:")+64 {
		t.Fatalf("digest = %q", first.ExternalDigest)
	}
	if again := buildInfo(t, cfg); again.ExternalDigest != first.ExternalDigest {
		t.Fatalf("unchanged bytes produced a different digest: %q vs %q", again.ExternalDigest, first.ExternalDigest)
	}
	write(data, "evil\nworse\n")
	changed := buildInfo(t, cfg)
	if changed.ExternalDigest == first.ExternalDigest || changed.ExternalFiles != 2 {
		t.Fatalf("same path, changed data file bytes not observable: %+v", changed)
	}
	if strings.Contains(fmt.Sprintf("%+v", changed), dir) {
		t.Fatalf("policy info leaks a rule file path: %+v", changed)
	}

	// A candidate that fails to compile yields no Firewall, so no info.
	write(rules, "SecRule ARGS \"@bogusOperator x\" \"id:20003,phase:1,deny\"\n")
	if _, err := New(context.Background(), cfg, Options{}); err == nil {
		t.Fatal("invalid rule file compiled")
	}
}

func TestPolicyInfoWithCRSAndExternalFile(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "tuning.conf")
	if err := os.WriteFile(rules, []byte(`SecRuleRemoveById 920350
SecRule REQUEST_URI "@contains /x" "id:30001,phase:1,deny,status:403"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	info := buildInfo(t, config.WAFConfig{Enabled: true, CRSEnabled: true, DirectivesFiles: []string{rules}})
	if info.ExternalFiles != 1 || info.Rules.External != 1 || info.Rules.Embedded < 100 {
		t.Fatalf("CRS + tuning file = %+v", info)
	}
	// The embedded CRS still enforces through the recording filesystem.
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rr, _ := buildAndServe(t, config.WAFConfig{Enabled: true, CRSEnabled: true, DirectivesFiles: []string{rules}}, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("tuning rule not enforced: %d", rr.Code)
	}
}

func TestSourceRecorderServesTheHashedBytes(t *testing.T) {
	mem := fstest.MapFS{
		"r/a.conf": {Data: []byte("alpha")},
		"r/b.conf": {Data: []byte("beta")},
	}
	rec := newSourceRecorder(mem)
	f, err := rec.Open("r/a.conf")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	if st, err := f.Stat(); err != nil || st.Name() != "a.conf" {
		t.Fatalf("stat = %v, %v", st, err)
	}
	_ = f.Close()
	if string(got) != "alpha" {
		t.Fatalf("served %q", got)
	}
	if b, err := rec.ReadFile("r/b.conf"); err != nil || string(b) != "beta" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if _, err := rec.ReadFile("r/missing"); err == nil {
		t.Fatal("missing file read")
	}
	if _, err := rec.Open("r/missing"); err == nil {
		t.Fatal("missing file opened")
	}
	// Directory, glob and stat access record nothing.
	d, err := rec.Open("r")
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if ents, err := rec.ReadDir("r"); err != nil || len(ents) != 2 {
		t.Fatalf("ReadDir = %v, %v", ents, err)
	}
	if m, err := rec.Glob("r/*.conf"); err != nil || len(m) != 2 {
		t.Fatalf("Glob = %v, %v", m, err)
	}
	if _, err := rec.Stat("r/a.conf"); err != nil {
		t.Fatal(err)
	}

	n, digest := rec.identity()
	chain := fmt.Sprintf("sha256:%s\nsha256:%s\n", hexSum("alpha"), hexSum("beta"))
	if n != 2 || digest != "sha256:"+hexSum(chain) {
		t.Fatalf("identity = %d %q", n, digest)
	}
	if n, d := newSourceRecorder(mem).identity(); n != 0 || d != "" {
		t.Fatalf("empty recorder identity = %d %q", n, d)
	}
}

func hexSum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// statFailFS returns a file whose Stat fails, covering the recorder's
// defensive close path.
type statFailFS struct{}

type statFailFile struct{ fs.File }

func (statFailFS) Open(string) (fs.File, error) { return statFailFile{}, nil }
func (statFailFile) Stat() (fs.FileInfo, error) { return nil, fs.ErrInvalid }
func (statFailFile) Close() error               { return nil }

func TestSourceRecorderStatFailure(t *testing.T) {
	if _, err := newSourceRecorder(statFailFS{}).Open("x"); err == nil {
		t.Fatal("stat failure not reported")
	}
}

func TestRuleCounterClassification(t *testing.T) {
	var c ruleCounter
	for _, f := range []string{"", "_inline_", "@owasp_crs/REQUEST-901-INITIALIZATION.conf", "@coraza.conf-recommended", "/etc/jul/rules.conf"} {
		c.observe(f)
	}
	if c.counts != (RuleCounts{Total: 5, Inline: 2, Embedded: 2, External: 1}) {
		t.Fatalf("counts = %+v", c.counts)
	}
}

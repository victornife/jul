// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
)

// renderValues flattens a fingerprint value map into a single string so a test
// can assert that no secret material appears anywhere inside it.
func renderValues(values map[string]any) string {
	return fmt.Sprint(values)
}

func TestComputeFingerprintCapturesLogFormat(t *testing.T) {
	cfg := fullConfig()
	fp := ComputeFingerprint(cfg)
	if fp.Values["global.log_format"] != "text" {
		t.Fatalf("global.log_format = %v, want \"text\"", fp.Values["global.log_format"])
	}
}

func TestWorkerThreadsIsNotStartupConsumed(t *testing.T) {
	fp := ComputeFingerprint(fullConfig())
	if _, ok := fp.Values["global.worker_threads"]; ok {
		t.Fatal("worker_threads is applied by OnReloaded and must not be in the startup fingerprint")
	}
}

func TestEffectiveWorkerThreadsResolvesAuto(t *testing.T) {
	cfg := fullConfig()
	cfg.Global.WorkerThreads = "auto"
	got := mustValue(t, cfg, "global.worker_threads")
	if got != InitialGOMAXPROCS() {
		t.Fatalf("auto resolved to %v, want the initial GOMAXPROCS %d", got, InitialGOMAXPROCS())
	}
	cfg.Global.WorkerThreads = "3"
	if got := mustValue(t, cfg, "global.worker_threads"); got != 3 {
		t.Fatalf("numeric worker_threads resolved to %v, want 3", got)
	}
}

// TestSecretPathsAreDigested proves no configured secret value leaves the
// process through the lifecycle extractors.
func TestSecretPathsAreDigested(t *testing.T) {
	cfg := fullConfig()
	cases := map[string]string{
		"admin.token":                            "shared-token",
		"admin.rbac.principals.*.token":          "principal-token",
		"upstreams.*.discovery.consul.token":     "consul-token",
		"upstreams.*.discovery.kubernetes.token": "kubernetes-token",
	}
	for path, secret := range cases {
		got := fmt.Sprint(mustValue(t, cfg, path))
		if strings.Contains(got, secret) {
			t.Errorf("%s leaked its configured value: %s", path, got)
		}
		if !strings.Contains(got, "sha256:") {
			t.Errorf("%s = %s, want a sha256 digest", path, got)
		}
	}
}

// TestSecretDigestStillDetectsRotation proves digesting does not hide a change.
func TestSecretDigestStillDetectsRotation(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Admin.TLS.ClientAuth.CAFile = "/etc/admin-ca-rotated.pem"
	if _, need := RestartRequired(ComputeFingerprint(before), ComputeFingerprint(after)); !need {
		t.Fatal("rotating admin.tls.client_auth.ca_file must be restart-required")
	}
}

// TestTLSCertContentRotationIsHotNotRestartRequired (#100) proves static
// certificate rotation no longer forces a restart: rotating file content in
// place is a real, detectable change (the digest differs) but is not a
// startup-consumed path any more, so the fingerprint does not compare it and
// RestartRequired never fires for it. The actual hot-swap detection lives in
// internal/server's prepareCertRotation/tlsIdentityFingerprint, not here.
func TestTLSCertContentRotationIsHotNotRestartRequired(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(cert, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fullConfig()
	cfg.Servers[0].TLS.Cert = cert
	before := ComputeFingerprint(cfg)

	if err := os.WriteFile(cert, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := ComputeFingerprint(cfg)

	if _, need := RestartRequired(before, after); need {
		t.Fatal("rotating the certificate file contents must not be restart-required (#100)")
	}
	if _, ok := before.Values["servers.*.tls.cert"]; ok {
		t.Fatal("servers.*.tls.cert must not be a startup-consumed fingerprint value any more (#100)")
	}
}

// TestClientCAAndCRLContentRotationIsDetected covers the remaining PKI material.
func TestClientCAAndCRLContentRotationIsDetected(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	crl := filepath.Join(dir, "crl.pem")
	for _, p := range []string{ca, crl} {
		if err := os.WriteFile(p, []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fullConfig()
	cfg.Servers[0].TLS.ClientAuth.CAFile = ca
	cfg.Servers[0].TLS.ClientAuth.CRLFile = crl

	for _, tc := range []struct{ file, path string }{
		{ca, "servers.*.tls.client_auth.ca_file"},
		{crl, "servers.*.tls.client_auth.crl_file"},
	} {
		before := ComputeFingerprint(cfg)
		if err := os.WriteFile(tc.file, []byte("v2-"+tc.path), 0o600); err != nil {
			t.Fatal(err)
		}
		changed := Diff(before, ComputeFingerprint(cfg))
		if !contains(changed, tc.path) {
			t.Errorf("rotating %s did not change %s (changed: %v)", tc.file, tc.path, changed)
		}
	}
}

// TestExactChangedPathIsReported proves the split replaced the coarse group: a
// changed minimum version names min_version, not the whole tls block.
func TestExactChangedPathIsReported(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].TLS.MinVersion = "1.2"

	changed := Diff(ComputeFingerprint(before), ComputeFingerprint(after))
	if len(changed) != 1 || changed[0] != "servers.*.tls.min_version" {
		t.Fatalf("changed paths = %v, want exactly [servers.*.tls.min_version]", changed)
	}
}

func TestInlinePEMIsDigestedAsText(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----"
	got := digestTLSMaterial(pem)
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("inline PEM digest = %q, want a text digest", got)
	}
	if strings.Contains(got, "abc") {
		t.Fatal("digest leaked the PEM body")
	}
}

func TestWindowsPathsAreTreatedAsCandidateFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test asserts the non-Windows fallback")
	}
	for _, p := range []string{`C:\certs\cert.pem`, `\\server\share\cert.pem`} {
		got := digestTLSMaterial(p)
		if !strings.HasPrefix(got, "sha256:") {
			t.Fatalf("digestTLSMaterial(%q) = %q, want a stable text digest fallback", p, got)
		}
	}
}

func TestRelativePathContentRotationIsDetected(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if err := os.WriteFile("cert.pem", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := digestTLSMaterial("cert.pem")
	if err := os.WriteFile("cert.pem", []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if second := digestTLSMaterial("cert.pem"); first == second {
		t.Fatal("a relative certificate path must be digested by content")
	}
}

// TestListenerAdditionIsNotRestartRequired pins R10-05: adding a listener must
// not strand the operator with a restart-required verdict on addresses nobody
// edited.
func TestListenerAdditionIsNotRestartRequired(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers = append(after.Servers, config.ServerConfig{Listen: ":9090", ServerNames: []string{"new.example.com"}})

	if _, need := RestartRequired(ComputeFingerprint(before), ComputeFingerprint(after)); need {
		t.Fatal("adding a listener must not be restart-required")
	}
}

// TestRetainedListenerTLSChangeIsRestartRequired is the complement: editing a
// kept listener's TLS material strands the running socket.
func TestRetainedListenerTLSChangeIsRestartRequired(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Servers[0].TLS.Enabled = false

	if _, need := RestartRequired(ComputeFingerprint(before), ComputeFingerprint(after)); !need {
		t.Fatal("disabling TLS on a kept listener must be restart-required")
	}
}

// TestVirtualHostOrderDoesNotCreateDiff proves per-address grouping is
// order-insensitive across server blocks sharing an address.
func TestVirtualHostOrderDoesNotCreateDiff(t *testing.T) {
	mk := func(names ...string) config.ServerConfig {
		return config.ServerConfig{
			Listen:      ":8443",
			ServerNames: names,
			TLS:         &config.TLSConfig{Enabled: true, Cert: "inline-cert", Key: "inline-key"},
		}
	}
	a := &config.Config{Servers: []config.ServerConfig{mk("a.example.com"), mk("b.example.com")}}
	b := &config.Config{Servers: []config.ServerConfig{mk("b.example.com"), mk("a.example.com")}}

	if changed := Diff(ComputeFingerprint(a), ComputeFingerprint(b)); len(changed) != 0 {
		t.Fatalf("reordering virtual hosts produced a diff: %v", changed)
	}
}

// TestServerNameOrderDoesNotCreateDiff proves an order-only edit of a
// semantically unordered list is not reported.
func TestServerNameOrderDoesNotCreateDiff(t *testing.T) {
	before := fullConfig()
	before.Servers[0].ServerNames = []string{"a.example.com", "b.example.com"}
	after := fullConfig()
	after.Servers[0].ServerNames = []string{"b.example.com", "a.example.com"}

	for _, ch := range DiffConfig(before, after) {
		if ch.Path == "servers.*.server_names" {
			t.Fatal("reordering server_names must not be reported as a change")
		}
	}
}

// TestUpstreamBackendOrderDoesNotCreateDiff proves an unordered backend list is
// compared as a set.
func TestUpstreamBackendOrderDoesNotCreateDiff(t *testing.T) {
	before := fullConfig()
	before.Upstreams[0].Servers = []config.UpstreamServer{{Address: "a:1", Weight: 1}, {Address: "b:2", Weight: 2}}
	after := fullConfig()
	after.Upstreams[0].Servers = []config.UpstreamServer{{Address: "b:2", Weight: 2}, {Address: "a:1", Weight: 1}}

	if changes := DiffConfig(before, after); len(changes) != 0 {
		t.Fatalf("reordering backends produced changes: %+v", changes)
	}
}

// TestRewriteOrderIsSignificant proves an ordered list keeps its order
// semantics: rewrites are evaluated top to bottom.
func TestRewriteOrderIsSignificant(t *testing.T) {
	before := fullConfig()
	before.Servers[0].Locations[0].Rewrites = []config.RewriteConfig{
		{Pattern: "^/a$", Replacement: "/1"},
		{Pattern: "^/b$", Replacement: "/2"},
	}
	after := fullConfig()
	after.Servers[0].Locations[0].Rewrites = []config.RewriteConfig{
		{Pattern: "^/b$", Replacement: "/2"},
		{Pattern: "^/a$", Replacement: "/1"},
	}

	if changes := DiffConfig(before, after); len(changes) == 0 {
		t.Fatal("reordering rewrite rules changes evaluation order and must be reported")
	}
}

// TestIgnoredFieldChangeCreatesNoRestart proves a deprecated log destination is
// reported as ignored and never as a pending restart.
func TestIgnoredFieldChangeCreatesNoRestart(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Global.AccessLog = "/var/log/other.log"
	after.Servers[0].ErrorLog = "/var/log/other-error.log"

	if _, need := RestartRequired(ComputeFingerprint(before), ComputeFingerprint(after)); need {
		t.Fatal("changing an ignored field must never be restart-required")
	}
	changes := DiffConfig(before, after)
	if len(changes) != 2 {
		t.Fatalf("expected both ignored fields to be reported, got %+v", changes)
	}
	for _, ch := range changes {
		if !ch.Ignored || ch.Class != IgnoredDeprecatedClass {
			t.Errorf("%s reported as %s (ignored=%v)", ch.Path, ch.Class, ch.Ignored)
		}
	}
}

// TestDiffConfigDetectsRegisteredChange keeps the completeness contract: a
// registered field that changed is always reported.
func TestDiffConfigDetectsRegisteredChange(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.WAF.DirectivesFiles = append(append([]string(nil), after.WAF.DirectivesFiles...), "/extra.conf")

	changes := DiffConfig(before, after)
	if len(changes) != 1 || changes[0].Path != "waf.directives_files" {
		t.Fatalf("changes = %+v, want exactly waf.directives_files", changes)
	}
}

// TestDiffConfigCarriesNoValues is the structural guarantee that preview output
// cannot contain configured values.
func TestDiffConfigCarriesNoValues(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.Admin.RBAC.Principals[0].Token = "rotated-secret"

	changes := DiffConfig(before, after)
	if len(changes) == 0 {
		t.Fatal("rotating a principal token must be reported")
	}
	if s := fmt.Sprint(changes); strings.Contains(s, "rotated-secret") || strings.Contains(s, "principal-token") {
		t.Fatalf("diff output carries secret material: %s", s)
	}
}

// TestStreamProtocolDefaultIsNormalized is the regression guard for the
// schema-walk migration: an omitted protocol means "tcp", so spelling the
// default out must not be reported as a change.
func TestStreamProtocolDefaultIsNormalized(t *testing.T) {
	before := &config.Config{Streams: []config.StreamServer{{Listen: ":5432", ProxyPass: "db"}}}
	after := &config.Config{Streams: []config.StreamServer{{Listen: ":5432", Protocol: "tcp", ProxyPass: "db"}}}
	if changes := DiffConfig(before, after); len(changes) != 0 {
		t.Fatalf("spelling out the default protocol produced changes: %+v", changes)
	}
	if changes := DiffConfig(after, &config.Config{Streams: []config.StreamServer{{Listen: ":5432", Protocol: "udp", ProxyPass: "db"}}}); len(changes) == 0 {
		t.Fatal("an actual protocol change must be reported")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

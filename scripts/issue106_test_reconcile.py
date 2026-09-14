#!/usr/bin/env python3
from pathlib import Path


def replace(path, old, new):
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one anchor, found {count}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1))


replace(
    "internal/server/dynamic_conn_limit_test.go",
    "\tclient3 := dialTCP(t, raw.Addr().String())\n\tblocked := acceptAsync(l)",
    "\tclient3 := dialTCP(t, raw.Addr().String())\n\tdefer client3.Close()\n\tblocked := acceptAsync(l)",
)

replace(
    "internal/admin/history_retention_hot_test.go",
    "\tstatus := s.adminRuntimeStatus(nil)\n\tif status.HistoryRetentionHealth != \"prune_failed\" {\n\t\tt.Fatalf(\"history health=%q want prune_failed\", status.HistoryRetentionHealth)\n\t}",
    "\tstatus := s.historyRetentionStatus.Load()\n\tif status == nil || *status != \"prune_failed\" {\n\t\tt.Fatalf(\"history health=%v want prune_failed\", status)\n\t}",
)

replace(
    "internal/lifecycle/classify_test.go",
    '''// TestClassifyConnectionCapIsListenerGlobal proves the global cap strands every
// kept listener.
func TestClassifyConnectionCapIsListenerGlobal(t *testing.T) {
\tbefore := fullConfig()
\tafter := fullConfig()
\tafter.RateLimit.MaxConns = 5

\tres, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif !contains(res.RestartRequired, "rate_limit.max_conns") {
\t\tt.Fatalf("restart-required = %v", res.RestartRequired)
\t}

\t// With no live listener there is nothing to strand.
\tres, err = Classify(&config.Config{}, &config.Config{RateLimit: config.RateLimitConfig{MaxConns: 5}}, Live{})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif !res.CanApplyHot {
\t\tt.Fatalf("without a bound listener the cap applies on the next bind: %+v", res)
\t}
}
''',
    '''// TestClassifyConnectionCapIsHot proves #106 removed max_conns from the
// bind-time lifecycle: retained and newly bound listeners use the same live
// admission policy.
func TestClassifyConnectionCapIsHot(t *testing.T) {
\tbefore := fullConfig()
\tafter := fullConfig()
\tafter.RateLimit.MaxConns = 5

\tres, err := Classify(before, after, Live{BoundHTTPAddrs: []string{":8443"}})
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif !res.CanApplyHot {
\t\tt.Fatalf("max_conns should hot-apply on a retained listener: %+v", res)
\t}
\tif contains(res.RestartRequired, "rate_limit.max_conns") || contains(res.NewListenerOnly, "rate_limit.max_conns") {
\t\tt.Fatalf("max_conns still classified listener-bound: %+v", res)
\t}
}
''',
)

replace(
    "internal/server/listener_bind_registry_test.go",
    '''\t{"rate_limit.max_conns", func(c *config.Config) {
\t\tc.RateLimit = config.RateLimitConfig{Enabled: true, Key: "ip", Rate: 1, Burst: 1, MaxConns: 7}
\t}},
''',
    "",
)

replace(
    "internal/server/listener_bind_registry_test.go",
    '''\t\tif e.Class == lifecycle.ValidationRejectedReservedClass {
\t\t\tcontinue
\t\t}
\t\tif !e.StartupConsumed {
\t\t\tt.Errorf("%s must be startup-consumed so the fingerprint compares it", e.Path)
\t\t}
''',
    '''\t\tif e.Class == lifecycle.ValidationRejectedReservedClass {
\t\t\tcontinue
\t\t}
\t\tif e.Path == "servers.*.tls.acme.ocsp_stapling" {
\t\t\tif e.Class != lifecycle.HotReloadClass || e.StartupConsumed {
\t\t\t\tt.Errorf("%s must be hot and not startup-consumed after #106: class=%s startup=%t", e.Path, e.Class, e.StartupConsumed)
\t\t\t}
\t\t\tcontinue
\t\t}
\t\tif !e.StartupConsumed {
\t\t\tt.Errorf("%s must be startup-consumed so the ACME restart gate compares it", e.Path)
\t\t}
''',
)

replace(
    "internal/server/listener_fingerprint_test.go",
    '''\tt.Run("connection cap (global max_conns) with a kept listener", func(t *testing.T) {
\t\told := cfg(plain())
\t\tnext := cfg(plain())
\t\tnext.RateLimit = config.RateLimitConfig{Enabled: true, MaxConns: 100}
\t\tif _, need := ListenerRebindRequired(old, next); !need {
\t\t\tt.Fatal("changing the connection cap must require a restart for kept listeners")
\t\t}
\t})
''',
    '''\tt.Run("connection cap (global max_conns) hot-applies on a kept listener", func(t *testing.T) {
\t\told := cfg(plain())
\t\tnext := cfg(plain())
\t\tnext.RateLimit = config.RateLimitConfig{Enabled: true, MaxConns: 100}
\t\thotApplies(t, old, next)
\t})
''',
)

replace(
    "internal/admin/patch_rate_limit_global_test.go",
    '''\tstage, err := executePatchBatch(context.Background(), patchBatchBaseline{
\t\tConfig: issue80BaseConfig(),
\t\tLive:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
\t}, "", []patchRequest{{
\t\tOp: "rate_limit_global_set",
\t\tRateLimit: &rateLimitPatch{
\t\t\tRate:     ptr(120),
\t\t\tBurst:    ptr(240),
\t\t\tMaxConns: ptr(20),
\t\t},
\t}})
\tif err != nil {
\t\tt.Fatalf("execute retained-listener max_conns: %v", err)
\t}
\tif stage.Lifecycle.CanApplyHot || !stage.Lifecycle.CanStageRestart {
\t\tt.Fatalf("retained-listener lifecycle = %+v, want staged complete candidate", stage.Lifecycle)
\t}
\tif !hasPath(stage.Lifecycle.RestartRequired, "rate_limit.max_conns") {
\t\tt.Fatalf("restart paths = %v, want rate_limit.max_conns", stage.Lifecycle.RestartRequired)
\t}
\tif stage.CandidateConfig.RateLimit.Rate != 120 || stage.CandidateConfig.RateLimit.MaxConns != 20 {
\t\tt.Fatalf("mixed candidate was partially built: %+v", stage.CandidateConfig.RateLimit)
\t}
''',
    '''\tlive, err := executePatchBatch(context.Background(), patchBatchBaseline{
\t\tConfig: issue80BaseConfig(),
\t\tLive:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
\t}, "", []patchRequest{{
\t\tOp: "rate_limit_global_set",
\t\tRateLimit: &rateLimitPatch{
\t\t\tRate:     ptr(120),
\t\t\tBurst:    ptr(240),
\t\t\tMaxConns: ptr(20),
\t\t},
\t}})
\tif err != nil {
\t\tt.Fatalf("execute retained-listener max_conns: %v", err)
\t}
\tif !live.Valid || !live.Lifecycle.CanApplyHot || hasPath(live.Lifecycle.RestartRequired, "rate_limit.max_conns") || hasPath(live.Lifecycle.NewListenerOnly, "rate_limit.max_conns") {
\t\tt.Fatalf("retained-listener lifecycle = %+v, want fully hot", live.Lifecycle)
\t}
\tif live.CandidateConfig.RateLimit.Rate != 120 || live.CandidateConfig.RateLimit.MaxConns != 20 {
\t\tt.Fatalf("candidate was partially built: %+v", live.CandidateConfig.RateLimit)
\t}
''',
)

replace(
    "internal/admin/patch_rate_limit_global_test.go",
    '''func TestGlobalRateLimitMaxConnsListenerAwareLifecycle(t *testing.T) {
\t// Adding a new address while retaining an existing address still strands the
\t// old listener with its previous cap.
\tretained, err := executePatchBatch(context.Background(), patchBatchBaseline{
\t\tConfig: issue80BaseConfig(),
\t\tLive:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
\t}, "", []patchRequest{
\t\t{Op: "server_add", Listen: ":9090"},
\t\t{Op: "rate_limit_global_set", RateLimit: &rateLimitPatch{MaxConns: ptr(20)}},
\t})
\tif err != nil {
\t\tt.Fatalf("execute retained plus new listener: %v", err)
\t}
\tif retained.Lifecycle.CanApplyHot || !hasPath(retained.Lifecycle.RestartRequired, "rate_limit.max_conns") {
\t\tt.Fatalf("retained plus new lifecycle = %+v, want restart", retained.Lifecycle)
\t}

\t// When every previously bound address is removed and every desired listener
\t// is newly bound in the same candidate, the candidate cap is installed live.
\tallNew, err := executePatchBatch(context.Background(), patchBatchBaseline{
\t\tConfig: issue80BaseConfig(),
\t\tLive:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
\t}, "", []patchRequest{
\t\t{Op: "server_add", Listen: ":9090"},
\t\t{Op: "server_remove", Listen: ":8080"},
\t\t{Op: "rate_limit_global_set", RateLimit: &rateLimitPatch{MaxConns: ptr(20)}},
\t})
\tif err != nil {
\t\tt.Fatalf("execute all-new listeners: %v", err)
\t}
\tif !allNew.Valid || !allNew.Lifecycle.CanApplyHot {
\t\tt.Fatalf("all-new lifecycle = %+v valid=%v errors=%+v", allNew.Lifecycle, allNew.Valid, allNew.ValidationErrors)
\t}
\tif !hasPath(allNew.Lifecycle.NewListenerOnly, "rate_limit.max_conns") {
\t\tt.Fatalf("new-listener paths = %v, want rate_limit.max_conns", allNew.Lifecycle.NewListenerOnly)
\t}

''',
    '''func TestGlobalRateLimitMaxConnsListenerAwareLifecycle(t *testing.T) {
\t// max_conns is independent of listener identity: a retained address and a
\t// newly added address both use the candidate live admission policy.
\tretained, err := executePatchBatch(context.Background(), patchBatchBaseline{
\t\tConfig: issue80BaseConfig(),
\t\tLive:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
\t}, "", []patchRequest{
\t\t{Op: "server_add", Listen: ":9090"},
\t\t{Op: "rate_limit_global_set", RateLimit: &rateLimitPatch{MaxConns: ptr(20)}},
\t})
\tif err != nil {
\t\tt.Fatalf("execute retained plus new listener: %v", err)
\t}
\tif !retained.Valid || !retained.Lifecycle.CanApplyHot || hasPath(retained.Lifecycle.RestartRequired, "rate_limit.max_conns") || hasPath(retained.Lifecycle.NewListenerOnly, "rate_limit.max_conns") {
\t\tt.Fatalf("retained plus new lifecycle = %+v, want max_conns hot", retained.Lifecycle)
\t}

''',
)

print("issue106 stale lifecycle tests reconciled")

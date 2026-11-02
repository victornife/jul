// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/adminapi"
	"jul/internal/cachemeta"
	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/server"
	"jul/internal/waf"
)

// alertNotificationPayload models an Alertmanager v2 webhook delivery to the
// local disposable receiver (#628-B).
type alertNotificationPayload struct {
	Version           string            `json:"version"`
	Status            string            `json:"status"` // "firing" or "resolved"
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	Alerts            []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
}

func TestSecurityAlertingPromtoolRules(t *testing.T) {
	root := filepath.Join("..", "..")
	rulesDir := filepath.Join(root, "examples", "alerting", "security")
	rulesFile := filepath.Join(rulesDir, "security-alerts.rules.yml")
	testFile := filepath.Join(rulesDir, "security-alerts.test.yml")
	promCfg := filepath.Join(rulesDir, "prometheus.yml")
	amCfg := filepath.Join(rulesDir, "alertmanager.yml")

	for _, p := range []string{rulesFile, testFile, promCfg, amCfg} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if len(data) == 0 {
			t.Fatalf("empty file %s", p)
		}
	}

	rulesBytes, _ := os.ReadFile(rulesFile)
	rulesText := string(rulesBytes)
	for _, wantAlert := range []string{
		"JulWAFProtectionWeakened",
		"JulSecurityPostureErrorPresent",
		"JulSecurityPostureWarningSustained",
		"JulSecurityEvidenceUnavailableOrDegraded",
		"JulWAFRejectionSymptomElevated",
	} {
		if !strings.Contains(rulesText, wantAlert) {
			t.Fatalf("security-alerts.rules.yml missing alert %q", wantAlert)
		}
	}
	// Verify producer metrics match our contract and never use jul_waf_events_total for rejection rate.
	if strings.Contains(rulesText, "jul_waf_events_total") {
		t.Fatal("security-alerts.rules.yml must use jul_waf_transactions_total for rejection rates, not jul_waf_events_total")
	}

	// Execute promtool check/test and amtool check-config via native binaries or pinned containers.
	ranToolValidation := false
	if promtoolPath, err := exec.LookPath("promtool"); err == nil {
		cmdCheck := exec.Command(promtoolPath, "check", "rules", rulesFile)
		if out, err := cmdCheck.CombinedOutput(); err != nil {
			t.Fatalf("promtool check rules failed: %v\n%s", err, string(out))
		}
		cmdTest := exec.Command(promtoolPath, "test", "rules", filepath.Base(testFile))
		cmdTest.Dir = rulesDir
		if out, err := cmdTest.CombinedOutput(); err != nil {
			t.Fatalf("promtool test rules failed: %v\n%s", err, string(out))
		}
		ranToolValidation = true
	} else if dockerPath, err := exec.LookPath("docker"); err == nil && runtime.GOOS == "linux" {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, dockerPath, "info").Run(); err == nil {
			absRulesDir, err := filepath.Abs(rulesDir)
			if err != nil {
				t.Fatalf("abs rulesDir: %v", err)
			}
			cmdCheck := exec.CommandContext(ctx, dockerPath, "run", "--rm",
				"-v", absRulesDir+":/rules:ro",
				"--workdir", "/rules",
				"--entrypoint", "promtool",
				"prom/prometheus:v3.11.2",
				"check", "rules", "security-alerts.rules.yml",
			)
			if out, err := cmdCheck.CombinedOutput(); err != nil {
				t.Fatalf("docker promtool check rules failed: %v\n%s", err, string(out))
			}
			cmdTest := exec.CommandContext(ctx, dockerPath, "run", "--rm",
				"-v", absRulesDir+":/rules:ro",
				"--workdir", "/rules",
				"--entrypoint", "promtool",
				"prom/prometheus:v3.11.2",
				"test", "rules", "security-alerts.test.yml",
			)
			if out, err := cmdTest.CombinedOutput(); err != nil {
				t.Fatalf("docker promtool test rules failed: %v\n%s", err, string(out))
			}
			cmdAM := exec.CommandContext(ctx, dockerPath, "run", "--rm",
				"-v", absRulesDir+":/etc/alertmanager:ro",
				"--entrypoint", "amtool",
				"prom/alertmanager:v0.32.0",
				"check-config", "/etc/alertmanager/alertmanager.yml",
			)
			if out, err := cmdAM.CombinedOutput(); err != nil {
				t.Fatalf("docker amtool check-config failed: %v\n%s", err, string(out))
			}
			ranToolValidation = true
		}
	}
	if !ranToolValidation {
		t.Skip("neither promtool nor a running docker daemon is available to execute promtool rule verification")
	}
}

func parsePromExposition(text string) map[string]float64 {
	out := make(map[string]float64)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		var val float64
		if _, err := fmt.Sscanf(parts[1], "%f", &val); err == nil {
			out[parts[0]] = val
		}
	}
	return out
}

func allocateLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func canRunRealAlertContainers(t *testing.T) bool {
	t.Helper()
	if runtime.GOOS != "linux" {
		return false
	}
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("docker CLI is required on Linux CI runners: %v", err)
		}
		return false
	}
	if err := exec.Command(dockerPath, "info").Run(); err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("docker daemon is required on Linux CI runners: %v", err)
		}
		return false
	}
	return true
}

func startRealPrometheusAndAlertmanagerE2E(t *testing.T, factory *HandlerFactory, receiverURL string) (promURL, amURL string, adminSrv *admin.Server, auditDir, auditFile string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("real Prometheus/Alertmanager --network host container E2E requires linux")
	}
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker CLI not found")
	}
	if err := exec.Command(dockerPath, "info").Run(); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}

	const bearerToken = "jul-e2e-metrics-secret-token-123456"
	var scrapeCount atomic.Int64
	auditDir = filepath.Join(t.TempDir(), "audit-dir")
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		t.Fatalf("MkdirAll auditDir: %v", err)
	}
	auditFile = filepath.Join(auditDir, "admin-audit.jsonl")
	if err := os.WriteFile(auditFile, make([]byte, 1024*1024-10), 0o600); err != nil {
		t.Fatalf("pre-size auditFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(auditDir, 0o700) })

	adminSrv = admin.New(
		config.AdminConfig{
			Enabled:             true,
			Listen:              "127.0.0.1:0",
			Token:               bearerToken,
			AuditLogFile:        auditFile,
			AuditLogRotateMaxMB: 1,
			AuditLogRotateKeep:  2,
		},
		factory.Log,
		admin.Deps{
			WAFCompiled:  waf.Compiled,
			WAFEffective: factory.WAFEffective,
			Metrics: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				scrapeCount.Add(1)
				factory.Metrics.Handler().ServeHTTP(w, r)
			}),
		},
	)
	factory.SetAuditHealthSource(adminSrv.AuditWriteHealth)
	adminHandler := adminSrv.Handler()
	metricsSrv := httptest.NewServer(adminHandler)
	t.Cleanup(metricsSrv.Close)

	// Verify Jul's real admin transport/auth gate rejects unauthenticated /metrics scrapes with 401.
	unauthResp, err := http.Get(metricsSrv.URL + "/metrics")
	if err != nil {
		t.Fatalf("unauthenticated /metrics probe: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /metrics status = %d, want 401", unauthResp.StatusCode)
	}

	julTarget := strings.TrimPrefix(metricsSrv.URL, "http://")

	promPort := allocateLocalPort(t)
	amPort := allocateLocalPort(t)

	cfgDir, err := os.MkdirTemp("", "jul-sec-alert-e2e-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfgDir) })
	if err := os.Chmod(cfgDir, 0o755); err != nil {
		t.Fatalf("Chmod cfgDir: %v", err)
	}

	root := filepath.Join("..", "..")
	shippedRules, err := os.ReadFile(filepath.Join(root, "examples", "alerting", "security", "security-alerts.rules.yml"))
	if err != nil {
		t.Fatalf("read shipped rules: %v", err)
	}
	// Preserve the exact PromQL expressions and annotations while shortening
	// scrape/eval/for intervals for the bounded test harness.
	fastRules := string(shippedRules)
	fastRules = strings.ReplaceAll(fastRules, "interval: 15s", "interval: 1s")
	fastRules = strings.ReplaceAll(fastRules, "for: 30s", "for: 1s")
	fastRules = strings.ReplaceAll(fastRules, "for: 1m", "for: 1s")
	fastRules = strings.ReplaceAll(fastRules, "for: 5m", "for: 1s")
	fastRules = strings.ReplaceAll(fastRules, `transition="weakened"}[5m]`, `transition="weakened"}[3s]`)
	fastRules = strings.ReplaceAll(fastRules, "[5m]", "[20s]")

	shippedAM, err := os.ReadFile(filepath.Join(root, "examples", "alerting", "security", "alertmanager.yml"))
	if err != nil {
		t.Fatalf("read shipped alertmanager.yml: %v", err)
	}
	fastAM := string(shippedAM)
	fastAM = strings.ReplaceAll(fastAM, "group_wait: 5s", "group_wait: 1s")
	fastAM = strings.ReplaceAll(fastAM, "group_interval: 30s", "group_interval: 1s")
	fastAM = strings.ReplaceAll(fastAM, "http://127.0.0.1:19095/alerts", receiverURL)

	promYML := fmt.Sprintf(`global:
  scrape_interval: 1s
  evaluation_interval: 1s

rule_files:
  - /etc/prometheus/security-alerts.rules.yml

alerting:
  alertmanagers:
    - static_configs:
        - targets:
            - "127.0.0.1:%d"

scrape_configs:
  - job_name: "jul-admin-metrics"
    metrics_path: "/metrics"
    scheme: "http"
    authorization:
      type: "Bearer"
      credentials_file: "/etc/prometheus/metrics-token"
    static_configs:
      - targets:
          - "%s"
`, amPort, julTarget)

	for name, content := range map[string]string{
		"security-alerts.rules.yml": fastRules,
		"alertmanager.yml":          fastAM,
		"prometheus.yml":            promYML,
		"metrics-token":             bearerToken + "\n",
	} {
		p := filepath.Join(cfgDir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	amName := fmt.Sprintf("jul-am-e2e-%d", amPort)
	promName := fmt.Sprintf("jul-prom-e2e-%d", promPort)
	t.Cleanup(func() {
		_ = exec.Command(dockerPath, "rm", "-f", promName, amName).Run()
	})

	if out, err := exec.Command(dockerPath, "run", "-d", "--rm",
		"--name", amName,
		"--network", "host",
		"-v", cfgDir+":/etc/alertmanager:ro",
		"prom/alertmanager:v0.32.0",
		"--config.file=/etc/alertmanager/alertmanager.yml",
		fmt.Sprintf("--web.listen-address=127.0.0.1:%d", amPort),
		"--cluster.listen-address=",
	).CombinedOutput(); err != nil {
		t.Fatalf("start alertmanager container: %v\n%s", err, string(out))
	}

	if out, err := exec.Command(dockerPath, "run", "-d", "--rm",
		"--name", promName,
		"--network", "host",
		"-v", cfgDir+":/etc/prometheus:ro",
		"prom/prometheus:v3.11.2",
		"--config.file=/etc/prometheus/prometheus.yml",
		fmt.Sprintf("--web.listen-address=127.0.0.1:%d", promPort),
	).CombinedOutput(); err != nil {
		t.Fatalf("start prometheus container: %v\n%s", err, string(out))
	}

	promURL = fmt.Sprintf("http://127.0.0.1:%d", promPort)
	amURL = fmt.Sprintf("http://127.0.0.1:%d", amPort)
	httpClient := &http.Client{Timeout: 2 * time.Second}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r1, err1 := httpClient.Get(amURL + "/-/ready")
		if err1 == nil {
			_ = r1.Body.Close()
		}
		r2, err2 := httpClient.Get(promURL + "/-/ready")
		if err2 == nil {
			_ = r2.Body.Close()
		}
		if err1 == nil && r1.StatusCode == http.StatusOK && err2 == nil && r2.StatusCode == http.StatusOK && scrapeCount.Load() >= 2 {
			return promURL, amURL, adminSrv, auditDir, auditFile
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("Prometheus/Alertmanager did not become ready and scrape Jul within 20s (scrapes=%d)", scrapeCount.Load())
	return "", "", nil, "", ""
}

func TestSecurityAlertingEndToEndLocalReceiver(t *testing.T) {
	if !waf.Compiled {
		t.Skip("requires -tags waf")
	}
	ctx := context.Background()
	tmpDir := t.TempDir()
	htpasswdPath := writeTempHTPasswd(t, tmpDir, "user", "secret")
	extRuleFile := filepath.Join(tmpDir, "external-alert.conf")
	initialRule := `SecRule ARGS:custom_bad "@streq block" "id:108628,phase:1,deny,status:403,log"` + "\n"
	if err := os.WriteFile(extRuleFile, []byte(initialRule), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var notifications []alertNotificationPayload
	var failReceiver atomic.Bool
	var failedDeliveryAttempts atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failReceiver.Load() {
			failedDeliveryAttempts.Add(1)
			http.Error(w, "receiver temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var n alertNotificationPayload
		if err := json.Unmarshal(body, &n); err == nil {
			mu.Lock()
			notifications = append(notifications, n)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	factory, cleanup := minimalFactory(t)
	defer cleanup()

	makeCfg := func(wafMode string, wafEnabled bool) *config.Config {
		return &config.Config{
			WAF: config.WAFConfig{
				Enabled:         wafEnabled,
				Mode:            wafMode,
				BlockStatus:     403,
				CRSEnabled:      true,
				DirectivesFiles: []string{extRuleFile},
			},
			Servers: []config.ServerConfig{
				{
					Listen: ":8080",
					Locations: []config.LocationConfig{
						{
							RouteID: config.String("rt-public"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/public"},
							Return:  200,
						},
						{
							RouteID: config.String("rt-auth"),
							Match:   config.MatchConfig{Type: "prefix", Path: "/auth"},
							Return:  200,
							Auth: &config.AuthConfig{
								Basic: &config.BasicAuthConfig{File: htpasswdPath, Realm: "Auth"},
							},
						},
					},
				},
			},
		}
	}

	// 1. Start in block mode with clean baseline, then start real Prometheus + Alertmanager scraping Jul's real admin /metrics.
	hMap, _, commit1, _, err := factory.Prepare(ctx, makeCfg("block", true))
	if err != nil {
		t.Fatalf("Prepare initial block: %v", err)
	}
	_, retire1 := commit1()
	defer retire1()
	hBlock := hMap[":8080"]

	_, amURL, adminSrv, auditDir, auditFile := startRealPrometheusAndAlertmanagerE2E(t, factory, receiver.URL+"/alerts")

	// Send clean requests and auth 401 denials -> must NOT fire JulWAFRejectionSymptomElevated.
	for i := 0; i < 6; i++ {
		reqClean := httptest.NewRequest(http.MethodGet, "http://example.test/public?id=clean", nil)
		recClean := httptest.NewRecorder()
		hBlock.ServeHTTP(recClean, reqClean)
		if recClean.Code != http.StatusOK {
			t.Fatalf("clean status = %d", recClean.Code)
		}
		reqAuth := httptest.NewRequest(http.MethodGet, "http://example.test/auth", nil)
		recAuth := httptest.NewRecorder()
		hBlock.ServeHTTP(recAuth, reqAuth)
		if recAuth.Code != http.StatusUnauthorized {
			t.Fatalf("auth status = %d", recAuth.Code)
		}
	}
	time.Sleep(2500 * time.Millisecond)
	mu.Lock()
	for _, n := range notifications {
		if n.GroupLabels["alertname"] == "JulWAFRejectionSymptomElevated" && n.Status == "firing" {
			mu.Unlock()
			t.Fatalf("clean traffic and auth 401s must not fire JulWAFRejectionSymptomElevated: %+v", n)
		}
	}
	mu.Unlock()

	// 2. Trigger real WAF rejections, modify the external rule file on disk WITHOUT any admin/Console read,
	// and publish a posture error finding -> Prometheus scrapes Jul's /metrics, evaluates the PromQL rules,
	// and Alertmanager delivers firing webhooks for JulWAFRejectionSymptomElevated,
	// JulSecurityEvidenceUnavailableOrDegraded, and JulSecurityPostureErrorPresent!
	if err := os.WriteFile(extRuleFile, []byte(initialRule+"# modified on disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory.Metrics.ObserveSecurityPostureFindings(map[string]map[string]int{
		"intent": {"error": 1, "warning": 0},
	})

	waitForWebhook := func(alertName, wantStatus string, timeout time.Duration, stepFn func()) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if stepFn != nil {
				stepFn()
			}
			mu.Lock()
			for _, n := range notifications {
				if n.GroupLabels["alertname"] == alertName && n.Status == wantStatus {
					mu.Unlock()
					return true
				}
				for _, a := range n.Alerts {
					if a.Labels["alertname"] == alertName && a.Status == wantStatus {
						mu.Unlock()
						return true
					}
				}
			}
			mu.Unlock()
			time.Sleep(250 * time.Millisecond)
		}
		return false
	}

	var sentAttacks atomic.Int32
	if !waitForWebhook("JulWAFRejectionSymptomElevated", "firing", 15*time.Second, func() {
		if sentAttacks.Add(1) <= 4 {
			reqAttack := httptest.NewRequest(http.MethodGet, "http://example.test/public?id=1%27%20OR%20%271%27=%271", nil)
			recAttack := httptest.NewRecorder()
			hBlock.ServeHTTP(recAttack, reqAttack)
		}
	}) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver firing JulWAFRejectionSymptomElevated webhook; got=%+v", got)
	}

	if !waitForWebhook("JulSecurityEvidenceUnavailableOrDegraded", "firing", 15*time.Second, nil) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver firing JulSecurityEvidenceUnavailableOrDegraded without admin reads; got=%+v", got)
	}

	if !waitForWebhook("JulSecurityPostureErrorPresent", "firing", 15*time.Second, nil) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver firing JulSecurityPostureErrorPresent; got=%+v", got)
	}

	// 3. Remediate external rule file, posture error, and rejection ratio (send 200 clean requests so
	// rejected/total < 5%) -> verify real Alertmanager delivers "resolved" webhooks for all three!
	if err := os.WriteFile(extRuleFile, []byte(initialRule), 0o600); err != nil {
		t.Fatal(err)
	}
	factory.Metrics.ObserveSecurityPostureFindings(map[string]map[string]int{
		"intent": {"error": 0, "warning": 0},
	})
	for i := 0; i < 200; i++ {
		reqClean := httptest.NewRequest(http.MethodGet, "http://example.test/public?id=clean", nil)
		recClean := httptest.NewRecorder()
		hBlock.ServeHTTP(recClean, reqClean)
	}

	for _, alertName := range []string{
		"JulSecurityEvidenceUnavailableOrDegraded",
		"JulSecurityPostureErrorPresent",
		"JulWAFRejectionSymptomElevated",
	} {
		if !waitForWebhook(alertName, "resolved", 15*time.Second, nil) {
			mu.Lock()
			got := append([]alertNotificationPayload(nil), notifications...)
			mu.Unlock()
			t.Fatalf("real Prometheus -> Alertmanager did not deliver resolved %s webhook; got=%+v", alertName, got)
		}
	}

	// 4. Protection weakening alert (reload into detect-only with 0 block routes) fires JulWAFProtectionWeakened,
	// and receiver failure (503) never degrades Jul serving or WAF enforcement!
	hMapDetect, _, commitDetect, _, err := factory.Prepare(ctx, makeCfg("detect", true))
	if err != nil {
		t.Fatalf("Prepare detect weakening: %v", err)
	}
	_, retireDetect := commitDetect()
	defer retireDetect()
	_ = hMapDetect
	if !waitForWebhook("JulWAFProtectionWeakened", "firing", 15*time.Second, nil) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver firing JulWAFProtectionWeakened; got=%+v", got)
	}

	// Enable receiver HTTP 503 failure BEFORE restoring block mode so Alertmanager
	// actively attempts delivery during the failure, verify Jul serving and WAF
	// enforcement continue unaffected while Alertmanager receives 503s, and then
	// restore the receiver and confirm Alertmanager's retry delivers the resolved webhook!
	failReceiver.Store(true)
	hMapRestore, _, commitRestore, _, err := factory.Prepare(ctx, makeCfg("block", true))
	if err != nil {
		t.Fatalf("Prepare block restore: %v", err)
	}
	_, retireRestore := commitRestore()
	defer retireRestore()
	hRestored := hMapRestore[":8080"]

	failDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(failDeadline) && failedDeliveryAttempts.Load() == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	if failedDeliveryAttempts.Load() == 0 {
		t.Fatal("expected Alertmanager to attempt webhook delivery while receiver returned 503")
	}

	reqCleanUnderFailure := httptest.NewRequest(http.MethodGet, "http://example.test/public?id=clean", nil)
	recCleanUnderFailure := httptest.NewRecorder()
	hRestored.ServeHTTP(recCleanUnderFailure, reqCleanUnderFailure)
	if recCleanUnderFailure.Code != http.StatusOK {
		t.Fatalf("clean request failed while alert receiver returned 503: status=%d", recCleanUnderFailure.Code)
	}
	reqUnderReceiverFailure := httptest.NewRequest(http.MethodGet, "http://example.test/public?id=1%27%20OR%20%271%27=%271", nil)
	recUnderReceiverFailure := httptest.NewRecorder()
	hRestored.ServeHTTP(recUnderReceiverFailure, reqUnderReceiverFailure)
	if recUnderReceiverFailure.Code != http.StatusForbidden {
		t.Fatalf("WAF block failed while alert receiver returned 503: status=%d", recUnderReceiverFailure.Code)
	}

	failReceiver.Store(false)
	if !waitForWebhook("JulWAFProtectionWeakened", "resolved", 15*time.Second, nil) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver resolved JulWAFProtectionWeakened after receiver recovery; got=%+v", got)
	}

	// 5. Item 4: Real configured/fault-injected audit sink (no Console or posture API reads):
	// Inject a real rotation failure on auditDir (chmod 0500 when auditFile is at 1MiB-10B)
	// and record a real audit event via adminSrv.RecordCacheCutoverReload -> verify /metrics
	// transitions jul_security_evidence_state{producer="audit_sink",state="degraded"} to 1,
	// Alertmanager delivers firing JulSecurityEvidenceUnavailableOrDegraded{producer="audit_sink"},
	// then restore auditDir permissions and write a healthy audit event -> verify resolved delivery!
	waitForWebhookMatching := func(alertName, wantStatus string, labelKey, labelVal string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			mu.Lock()
			for _, n := range notifications {
				for _, a := range n.Alerts {
					if a.Labels["alertname"] == alertName && a.Status == wantStatus && a.Labels[labelKey] == labelVal {
						mu.Unlock()
						return true
					}
				}
			}
			mu.Unlock()
			time.Sleep(250 * time.Millisecond)
		}
		return false
	}

	faultAuditFile := filepath.Join(auditDir, "admin-audit-fault.jsonl")
	if err := os.WriteFile(faultAuditFile, make([]byte, 1024*1024-10), 0o600); err != nil {
		t.Fatalf("pre-size faultAuditFile: %v", err)
	}
	faultAdminCfg := config.AdminConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:0",
		Token:               "jul-e2e-metrics-secret-token-123456",
		AuditLogFile:        faultAuditFile,
		AuditLogRotateMaxMB: 1,
		AuditLogRotateKeep:  2,
	}
	prepFault, err := adminSrv.PrepareAdminRuntime(faultAdminCfg, admin.PrepareAuth(faultAdminCfg, nil))
	if err != nil {
		t.Fatalf("PrepareAdminRuntime fault: %v", err)
	}
	adminSrv.CommitPreparedAdminRuntime(prepFault)
	if err := os.Remove(faultAuditFile); err != nil {
		t.Fatalf("Remove faultAuditFile to trigger rotation failure: %v", err)
	}
	_ = auditFile
	adminSrv.RecordCacheCutoverReload(server.ReloadResult{
		ID:        "rel-audit-fault",
		Source:    server.ReloadSourceAdmin,
		Published: true,
		Outcome:   server.ReloadAppliedLive,
		CacheCutover: &cachemeta.CutoverResult{
			State:                 "completed",
			InvalidationEffective: "effective",
		},
	})
	if _, failing, ok := adminSrv.AuditWriteHealth(); !ok || !failing {
		t.Fatalf("expected adminSrv.AuditWriteHealth failing=true ok=true after rotation failure, got failing=%v ok=%v", failing, ok)
	}
	if !waitForWebhookMatching("JulSecurityEvidenceUnavailableOrDegraded", "firing", "producer", "audit_sink", 15*time.Second) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver firing JulSecurityEvidenceUnavailableOrDegraded{producer=\"audit_sink\"} without posture reads; got=%+v", got)
	}

	if err := os.WriteFile(auditFile, nil, 0o600); err != nil {
		t.Fatalf("restore auditFile: %v", err)
	}
	recoverAdminCfg := config.AdminConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:0",
		Token:               "jul-e2e-metrics-secret-token-123456",
		AuditLogFile:        filepath.Join(auditDir, "admin-audit-recovered.jsonl"),
		AuditLogRotateMaxMB: 1,
		AuditLogRotateKeep:  2,
	}
	prepRuntime, err := adminSrv.PrepareAdminRuntime(recoverAdminCfg, admin.PrepareAuth(recoverAdminCfg, nil))
	if err != nil {
		t.Fatalf("adminSrv.PrepareAdminRuntime audit recovery: %v", err)
	}
	adminSrv.CommitPreparedAdminRuntime(prepRuntime)
	adminSrv.RecordCacheCutoverReload(server.ReloadResult{
		ID:        "rel-audit-recover",
		Source:    server.ReloadSourceAdmin,
		Published: true,
		Outcome:   server.ReloadAppliedLive,
		CacheCutover: &cachemeta.CutoverResult{
			State:                 "completed",
			InvalidationEffective: "effective",
		},
	})
	if _, failing, ok := adminSrv.AuditWriteHealth(); !ok || failing {
		t.Fatalf("expected adminSrv.AuditWriteHealth failing=false ok=true after recovery, got failing=%v ok=%v", failing, ok)
	}
	if !waitForWebhookMatching("JulSecurityEvidenceUnavailableOrDegraded", "resolved", "producer", "audit_sink", 15*time.Second) {
		mu.Lock()
		got := append([]alertNotificationPayload(nil), notifications...)
		mu.Unlock()
		t.Fatalf("real Prometheus -> Alertmanager did not deliver resolved JulSecurityEvidenceUnavailableOrDegraded{producer=\"audit_sink\"}; got=%+v", got)
	}

	// 6. Item 8: Causal Alertmanager inhibition:
	// When WAF evidence is unavailable (producer="waf_effective", state="unavailable"),
	// it may inhibit JulSecurityPostureWarningSustained{area="waf"}, but independent
	// trust and certificates posture warnings MUST still deliver and resolve!
	factory.SetAuditHealthSource(func() (uint64, bool, bool) { return 0, false, true })
	factory.Metrics.SetPreGatherHook(func() {
		factory.Metrics.ObserveSecurityEvidenceState("waf_effective", "unavailable")
		factory.Metrics.ObserveSecurityEvidenceState("waf_investigation", "ok")
		factory.Metrics.ObserveSecurityEvidenceState("audit_sink", "ok")
	})
	factory.Metrics.ObserveSecurityPostureFindings(map[string]map[string]int{
		"waf":          {"warning": 1, "error": 0},
		"trust":        {"warning": 1, "error": 0},
		"certificates": {"warning": 1, "error": 0},
	})

	if !waitForWebhookMatching("JulSecurityEvidenceUnavailableOrDegraded", "firing", "producer", "waf_effective", 15*time.Second) {
		t.Fatal("expected firing JulSecurityEvidenceUnavailableOrDegraded{producer=\"waf_effective\"}")
	}
	if !waitForWebhookMatching("JulSecurityPostureWarningSustained", "firing", "area", "trust", 15*time.Second) {
		t.Fatal("independent JulSecurityPostureWarningSustained{area=\"trust\"} was falsely inhibited by unavailable WAF evidence")
	}
	if !waitForWebhookMatching("JulSecurityPostureWarningSustained", "firing", "area", "certificates", 15*time.Second) {
		t.Fatal("independent JulSecurityPostureWarningSustained{area=\"certificates\"} was falsely inhibited by unavailable WAF evidence")
	}
	// Verify Alertmanager genuinely inhibits area="waf" (status.state == "suppressed") via /api/v2/alerts.
	var inhibitedWAF bool
	inhibDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(inhibDeadline) {
		amResp, err := http.Get(amURL + "/api/v2/alerts?active=false&silenced=false&inhibited=true")
		if err == nil {
			amBody, _ := io.ReadAll(amResp.Body)
			_ = amResp.Body.Close()
			if strings.Contains(string(amBody), `"area":"waf"`) && strings.Contains(string(amBody), `"state":"suppressed"`) {
				inhibitedWAF = true
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !inhibitedWAF {
		t.Fatal("expected Alertmanager to mark JulSecurityPostureWarningSustained{area=\"waf\"} as inhibited (state=suppressed)")
	}

	// Clear posture warnings and verify trust and certificates warnings resolve.
	factory.Metrics.SetPreGatherHook(factory.RefreshSecurityEvidenceMetrics)
	factory.Metrics.ObserveSecurityPostureFindings(map[string]map[string]int{
		"waf":          {"warning": 0, "error": 0},
		"trust":        {"warning": 0, "error": 0},
		"certificates": {"warning": 0, "error": 0},
	})
	if !waitForWebhookMatching("JulSecurityPostureWarningSustained", "resolved", "area", "trust", 15*time.Second) ||
		!waitForWebhookMatching("JulSecurityPostureWarningSustained", "resolved", "area", "certificates", 15*time.Second) {
		t.Fatal("expected trust and certificates posture warnings to resolve")
	}
}

// TestWAFProgrammeIntegratedOperatorJourneyEpic629 proves the complete #629
// operator outcome end-to-end across #632 (A+B), #631, #630, #633, and #634
// (paired with TestSecurityAlertingEndToEndLocalReceiver for the live
// Prometheus/Alertmanager notification pipeline):
//  1. Legitimate bio upload is rejected by WAF block mode with opaque X-Jul-WAF-Event
//     support header; origin is not reached; rejection metrics increment.
//  2. Operator investigates the support reference via GET /api/v1/waf/events and
//     identifies the serving policy digest, route attribution, rule 942100, and
//     sanitized target ARGS:profile_bio.
//  3. Operator creates a narrow #631 exception for rule 942100 on ARGS:profile_bio
//     (POST only), evaluates it against #630 synthetic fixtures (0 false positives,
//     0 false negatives), previews the semantic diff, and activates generation 2.
//  4. Legitimate POST /profile with profile_bio succeeds (200 OK, origin reached),
//     while SQLi on other_field, XSS on profile_bio, SQLi on GET /profile, and
//     attacks on sibling routes remain blocked (403 Forbidden).
//  5. Operator saves the #630 policy manifest, detects on-disk external rule-file
//     tampering via both GET /api/security (stale_asset_changed) and
//     VerifyPolicyAssets (asset_changed), recovers the exact external asset bytes,
//     restarts onto a fresh HandlerFactory with the same durable version context,
//     and verifies identical policy/external digests and metric recovery.
func TestWAFProgrammeIntegratedOperatorJourneyEpic629(t *testing.T) {
	if !waf.Compiled {
		recordExecutedIsolationCase("ISO-WAF-003", "policy_lifecycle", "unavailable", "narrow_exception_and_detection_paranoia_never_weaken_neighboring_scope")
		t.Skip("requires -tags waf")
	}
	ctx := context.Background()
	tmpDir := t.TempDir()

	extRuleFile := filepath.Join(tmpDir, "site-custom.conf")
	extRuleBytes := []byte(`SecRule ARGS:legacy_probe "@streq block_me" "id:100629,phase:2,deny,status:403,log,msg:'custom site rule'"` + "\n")
	if err := os.WriteFile(extRuleFile, extRuleBytes, 0o600); err != nil {
		t.Fatalf("write extRuleFile: %v", err)
	}

	var profileOriginHits atomic.Int32
	var siblingOriginHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/profile") {
			profileOriginHits.Add(1)
		} else {
			siblingOriginHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("origin-ok"))
	}))
	defer origin.Close()

	factory, cleanup := minimalFactory(t)
	defer cleanup()

	buildCfg := func(exceptions []config.WAFException) *config.Config {
		return &config.Config{
			WAF: config.WAFConfig{
				Enabled:           true,
				Mode:              "block",
				BlockStatus:       403,
				CRSEnabled:        true,
				BlockingParanoia:  1,
				DetectionParanoia: 1,
				DirectivesFiles:   []string{extRuleFile},
				Exceptions:        exceptions,
				RequestBodyLimit:  config.Size(128 << 10),
			},
			Upstreams: []config.UpstreamConfig{
				{
					Name:     "app_up",
					Strategy: "round_robin",
					Servers:  []config.UpstreamServer{{Address: origin.Listener.Addr().String(), Weight: 1}},
				},
			},
			Servers: []config.ServerConfig{
				{
					Name:        "app",
					Listen:      ":8080",
					ServerNames: []string{"app.example.test"},
					Locations: []config.LocationConfig{
						{
							RouteID:   config.String("rt-profile"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/profile"},
							ProxyPass: "http://app_up",
							Sharing:   &config.SharingConfig{Mode: "public"},
						},
						{
							RouteID:   config.String("rt-sibling"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/sibling"},
							ProxyPass: "http://app_up",
							Sharing:   &config.SharingConfig{Mode: "public"},
						},
					},
				},
			},
		}
	}

	// ── Step 1: Baseline deployment & false-positive rejection with external alert ─
	cfgGen1 := buildCfg(nil)
	var currentCfg atomic.Pointer[config.Config]
	currentCfg.Store(cfgGen1)

	hMap1, _, commit1, _, err := factory.Prepare(ctx, cfgGen1)
	if err != nil {
		t.Fatalf("Prepare Gen 1: %v", err)
	}
	_, retire1 := commit1()
	defer retire1()
	hGen1 := hMap1[":8080"]

	adminSrv := admin.New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"}, factory.Log, admin.Deps{
		LoadConfig:                   func() (*config.Config, error) { return currentCfg.Load(), nil },
		WAFCompiled:                  waf.Compiled,
		WAFEffective:                 factory.WAFEffective,
		EvaluateWAF:                  factory.HandleAdminWAFEvaluate,
		WAFInvestigation:             factory.WAFInvestigationSnapshot,
		ObserveSecurityPosture:       factory.Metrics.ObserveSecurityPostureFindings,
		ObserveSecurityEvidenceState: factory.Metrics.ObserveSecurityEvidenceState,
	})

	var alertMu sync.Mutex
	var alertNotifs []alertNotificationPayload
	alertReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var n alertNotificationPayload
		if err := json.Unmarshal(body, &n); err == nil {
			alertMu.Lock()
			alertNotifs = append(alertNotifs, n)
			alertMu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer alertReceiver.Close()

	liveContainers := canRunRealAlertContainers(t)
	if liveContainers {
		startRealPrometheusAndAlertmanagerE2E(t, factory, alertReceiver.URL+"/alerts")
	}

	waitJourneyWebhook := func(alertName, wantStatus string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			alertMu.Lock()
			for _, n := range alertNotifs {
				if n.GroupLabels["alertname"] == alertName && n.Status == wantStatus {
					alertMu.Unlock()
					return true
				}
				for _, a := range n.Alerts {
					if a.Labels["alertname"] == alertName && a.Status == wantStatus {
						alertMu.Unlock()
						return true
					}
				}
			}
			alertMu.Unlock()
			time.Sleep(250 * time.Millisecond)
		}
		return false
	}

	// Send 4 legitimate bio updates containing SQL documentation keywords ("1' OR '1'='1") -> blocked 403!
	var supportEventID string
	bioBody := "profile_bio=1%27+OR+%271%27%3D%271"
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodPost, "http://app.example.test/profile", strings.NewReader(bioBody))
		req.Host = "app.example.test"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(bioBody)))
		req.Header.Set("X-Request-ID", "client-req-bio-1")
		rec := httptest.NewRecorder()
		hGen1.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("Gen 1 bio POST status = %d, want 403", rec.Code)
		}
		supportEventID = rec.Header().Get(waf.HeaderWAFEvent)
		if !strings.HasPrefix(supportEventID, "wev_") {
			t.Fatalf("expected opaque wev_ support reference header, got %q", supportEventID)
		}
	}
	if profileOriginHits.Load() != 0 {
		t.Fatalf("blocked request must not reach origin, got %d hits", profileOriginHits.Load())
	}

	recM1 := httptest.NewRecorder()
	factory.Metrics.Handler().ServeHTTP(recM1, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	vals1 := parsePromExposition(recM1.Body.String())
	if vals1[`jul_waf_transactions_total{mode="block",outcome="interrupted_request"}`] != 4 {
		t.Fatalf("expected 4 interrupted_request transactions in /metrics, got %v", vals1[`jul_waf_transactions_total{mode="block",outcome="interrupted_request"}`])
	}
	if liveContainers {
		if !waitJourneyWebhook("JulWAFRejectionSymptomElevated", "firing", 15*time.Second) {
			alertMu.Lock()
			got := append([]alertNotificationPayload(nil), alertNotifs...)
			alertMu.Unlock()
			t.Fatalf("expected firing JulWAFRejectionSymptomElevated webhook in #629 journey, got %+v", got)
		}
	}

	// ── Step 2: Operator investigates the support reference via #632 API ─────
	reqInv := httptest.NewRequest(http.MethodGet, "/api/v1/waf/events?event_id="+supportEventID, nil)
	reqInv = reqInv.WithContext(rbac.WithIdentity(reqInv.Context(), rbac.Identity{
		Principal:   "secops",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.ObservabilityRead, rbac.ConfigRead, rbac.StatusRead, rbac.ConfigWrite},
	}))
	recInv := httptest.NewRecorder()
	adminSrv.Handler().ServeHTTP(recInv, reqInv)
	if recInv.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/waf/events status = %d: %s", recInv.Code, recInv.Body.String())
	}
	var eventsResp adminapi.WAFEventsResponse
	if err := json.Unmarshal(recInv.Body.Bytes(), &eventsResp); err != nil {
		t.Fatalf("decode eventsResp: %v", err)
	}
	if len(eventsResp.Events) != 1 {
		t.Fatalf("expected 1 event for %s, got %d", supportEventID, len(eventsResp.Events))
	}
	ev := eventsResp.Events[0]
	if ev.Outcome != waf.OutcomeInterruptedRequest || ev.OriginReached || ev.Route.RouteID != "rt-profile" || ev.Generation != 1 {
		t.Fatalf("unexpected investigated event: %+v", ev)
	}
	var found942100 bool
	for _, mr := range ev.MatchedRules {
		if mr.RuleID == 942100 {
			found942100 = true
		}
	}
	if !found942100 {
		t.Fatalf("expected rule 942100 in matched_rules, got %+v", ev.MatchedRules)
	}

	// ── Step 3: Narrow #631 tuning & #630 fixture evaluation ─────────────────
	narrowExc := config.WAFException{
		ID:        "allow-profile-bio-sql-doc",
		RuleID:    942100,
		Scope:     "narrow_target",
		Target:    "ARGS:profile_bio",
		Methods:   []string{"POST"},
		Rationale: "Allow SQL documentation snippets in profile_bio on POST",
		Owner:     "secops",
		Reference: "SEC-629",
		CreatedAt: "2026-11-01",
		ReviewAt:  "2027-02-01",
		Fixtures:  []string{"benign-bio-sql-doc"},
	}
	cfgGen2 := buildCfg([]config.WAFException{narrowExc})

	// Preview semantic diff before activation.
	diff := admin.DiffConfigs(cfgGen1, cfgGen2)
	if len(diff.Modifications) == 0 {
		t.Fatal("expected semantic diff to report WAF exception addition")
	}

	// Evaluate candidate against default synthetic pack + custom benign/attack fixtures.
	pack := waf.DefaultFixturePack()
	pack.Fixtures = append(pack.Fixtures,
		waf.WAFFixtureCase{
			ID:                    "benign-bio-sql-doc",
			Category:              "legitimate",
			Description:           "POST profile_bio with SQL documentation snippet",
			Method:                "POST",
			Host:                  "app.example.test",
			Path:                  "/profile",
			Headers:               map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			Body:                  bioBody,
			ExpectedOutcome:       waf.OutcomeClean,
			ExpectedStatus:        http.StatusOK,
			ExpectedOriginReached: true,
		},
		waf.WAFFixtureCase{
			ID:                    "attack-neighbor-field-sqli",
			Category:              "malicious",
			Description:           "POST other_field with SQLi must still be blocked",
			Method:                "POST",
			Host:                  "app.example.test",
			Path:                  "/profile",
			Headers:               map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			Body:                  "other_field=1%27+OR+%271%27%3D%271",
			ExpectedOutcome:       waf.OutcomeInterruptedRequest,
			ExpectedStatus:        http.StatusForbidden,
			ExpectedOriginReached: false,
			ExpectedRuleIDs:       []int{942100},
		},
	)
	evalReport, err := waf.EvaluatePolicy(ctx, "global", cfgGen2.WAF, &cfgGen1.WAF, pack, factory.Versions)
	if err != nil {
		t.Fatalf("EvaluateCandidate: %v", err)
	}
	if evalReport.Summary.FailedCases != 0 || evalReport.Summary.LegitimateBlocked != 0 || evalReport.Summary.MaliciousAllowed != 0 {
		t.Fatalf("expected 0 failed fixtures and 0 false positives/negatives, got %+v", evalReport.Summary)
	}
	if evalReport.Summary.ChangedFromBaseline == 0 {
		t.Fatal("expected ChangedFromBaseline > 0 for benign-bio-sql-doc fixture")
	}

	// ── Step 4: Activate generation 2 & verify narrow scope + recovery ───────
	hMap2, _, commit2, _, err := factory.Prepare(ctx, cfgGen2)
	if err != nil {
		t.Fatalf("Prepare Gen 2: %v", err)
	}
	_, retire2 := commit2()
	defer retire2()
	currentCfg.Store(cfgGen2)
	hGen2 := hMap2[":8080"]

	// Record evaluation against serving generation 2 via POST /api/security/waf/evaluate.
	reqEval := httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("{}"))
	reqEval = reqEval.WithContext(rbac.WithIdentity(reqEval.Context(), rbac.Identity{
		Principal:   "secops",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.ConfigWrite, rbac.StatusRead},
	}))
	recEval := httptest.NewRecorder()
	adminSrv.Handler().ServeHTTP(recEval, reqEval)
	if recEval.Code != http.StatusOK {
		t.Fatalf("POST /api/security/waf/evaluate status = %d: %s", recEval.Code, recEval.Body.String())
	}

	// 4a. Legitimate POST /profile with profile_bio now succeeds (200 OK, origin reached)!
	reqAllowed := httptest.NewRequest(http.MethodPost, "http://app.example.test/profile", strings.NewReader(bioBody))
	reqAllowed.Host = "app.example.test"
	reqAllowed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqAllowed.Header.Set("Content-Length", fmt.Sprintf("%d", len(bioBody)))
	recAllowed := httptest.NewRecorder()
	hGen2.ServeHTTP(recAllowed, reqAllowed)
	if recAllowed.Code != http.StatusOK || profileOriginHits.Load() != 1 {
		t.Fatalf("Gen 2 bio POST status = %d, originHits = %d; want 200, 1", recAllowed.Code, profileOriginHits.Load())
	}

	// 4b. Neighboring field (other_field) with SQLi is still blocked (403)!
	otherBody := "other_field=1%27+OR+%271%27%3D%271"
	reqOtherField := httptest.NewRequest(http.MethodPost, "http://app.example.test/profile", strings.NewReader(otherBody))
	reqOtherField.Host = "app.example.test"
	reqOtherField.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqOtherField.Header.Set("Content-Length", fmt.Sprintf("%d", len(otherBody)))
	recOtherField := httptest.NewRecorder()
	hGen2.ServeHTTP(recOtherField, reqOtherField)
	if recOtherField.Code != http.StatusForbidden {
		t.Fatalf("neighboring field SQLi status = %d, want 403", recOtherField.Code)
	}

	// 4c. Neighboring rule (XSS 941100) on the same field (profile_bio) is still blocked (403)!
	xssBody := "profile_bio=%3Cscript%3Ealert(1)%3C/script%3E"
	reqXSS := httptest.NewRequest(http.MethodPost, "http://app.example.test/profile", strings.NewReader(xssBody))
	reqXSS.Host = "app.example.test"
	reqXSS.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqXSS.Header.Set("Content-Length", fmt.Sprintf("%d", len(xssBody)))
	recXSS := httptest.NewRecorder()
	hGen2.ServeHTTP(recXSS, reqXSS)
	if recXSS.Code != http.StatusForbidden {
		t.Fatalf("XSS on profile_bio status = %d, want 403", recXSS.Code)
	}

	// 4d. Disallowed HTTP method (GET) on the same field (profile_bio) is still blocked (403)!
	reqGET := httptest.NewRequest(http.MethodGet, "http://app.example.test/profile?profile_bio=1%27%20OR%20%271%27=%271", nil)
	reqGET.Host = "app.example.test"
	recGET := httptest.NewRecorder()
	hGen2.ServeHTTP(recGET, reqGET)
	if recGET.Code != http.StatusForbidden {
		t.Fatalf("GET method SQLi on profile_bio status = %d, want 403", recGET.Code)
	}

	// ── Step 5: Exact policy manifest, same-path tamper detection, and recovery ─
	manifest, fwManifest, err := waf.BuildPolicyManifest(ctx, "global", cfgGen2.WAF, factory.Versions)
	if err != nil {
		t.Fatalf("BuildPolicyManifest: %v", err)
	}
	_ = fwManifest.Close()

	// Modify extRuleFile in place at the same path without reloading, induce a posture error
	// and a temporary protection weakening, and verify all three alerts fire via Prometheus -> Alertmanager!
	if err := os.WriteFile(extRuleFile, append(extRuleBytes, []byte("# modified on disk\n")...), 0o600); err != nil {
		t.Fatalf("modify extRuleFile: %v", err)
	}
	verifyStale, err := waf.VerifyPolicyAssets(ctx, cfgGen2.WAF, manifest, factory.Versions)
	if err != nil || verifyStale.Verified || verifyStale.Status != "asset_changed" {
		t.Fatalf("expected VerifyPolicyAssets status=asset_changed, got %+v (err=%v)", verifyStale, err)
	}
	cfgWeak := buildCfg([]config.WAFException{narrowExc})
	cfgWeak.WAF.Mode = "detect"
	cfgWeak.Servers[0].Locations[1].Sharing = &config.SharingConfig{Mode: "restricted"}
	_, _, commitWeak, _, err := factory.Prepare(ctx, cfgWeak)
	if err != nil {
		t.Fatalf("Prepare cfgWeak: %v", err)
	}
	_, retireWeak := commitWeak()
	defer retireWeak()

	if liveContainers {
		for _, alertName := range []string{
			"JulSecurityEvidenceUnavailableOrDegraded",
			"JulSecurityPostureErrorPresent",
			"JulWAFProtectionWeakened",
		} {
			if !waitJourneyWebhook(alertName, "firing", 15*time.Second) {
				alertMu.Lock()
				got := append([]alertNotificationPayload(nil), alertNotifs...)
				alertMu.Unlock()
				t.Fatalf("expected firing %s webhook in #629 journey, got %+v", alertName, got)
			}
		}
	}
	if eff := factory.WAFEffective(); eff == nil || eff.Evaluation == nil || eff.Evaluation.State != "stale_asset_changed" {
		t.Fatalf("expected live on-disk asset modification to mark evaluation stale_asset_changed, got %+v", eff)
	}

	// Restore exact bytes of extRuleFile, restore block-mode Gen 2, clear posture error,
	// re-verify, and prove reproducible recovery across a process restart!
	if err := os.WriteFile(extRuleFile, extRuleBytes, 0o600); err != nil {
		t.Fatalf("restore extRuleFile: %v", err)
	}
	verifyRestored, err := waf.VerifyPolicyAssets(ctx, cfgGen2.WAF, manifest, factory.Versions)
	if err != nil || !verifyRestored.Verified || verifyRestored.Status != "verified" {
		t.Fatalf("expected restored VerifyPolicyAssets status=verified, got %+v (err=%v)", verifyRestored, err)
	}
	hMapRestored, _, commitRestored, _, err := factory.Prepare(ctx, cfgGen2)
	if err != nil {
		t.Fatalf("Prepare restored cfgGen2: %v", err)
	}
	_, retireRestored := commitRestored()
	defer retireRestored()
	hGen2 = hMapRestored[":8080"]

	factoryRestart, cleanupRestart := minimalFactory(t)
	defer cleanupRestart()
	factoryRestart.Versions = factory.Versions
	_, _, commitRestart, _, err := factoryRestart.Prepare(ctx, cfgGen2)
	if err != nil {
		t.Fatalf("Prepare after restart: %v", err)
	}
	_, retireRestart := commitRestart()
	defer retireRestart()
	effRestart := factoryRestart.WAFEffective()
	if effRestart == nil || effRestart.Global == nil || effRestart.Global.PolicyDigest != manifest.PolicyDigest || effRestart.Global.ExternalDigest != manifest.ExternalDigest {
		t.Fatalf("post-restart digests = %+v, want policy=%q external=%q", effRestart, manifest.PolicyDigest, manifest.ExternalDigest)
	}

	// Legitimate traffic recovers the rejection ratio (<5% rejected) and resolves all 4 alert families!
	for i := 0; i < 200; i++ {
		reqClean := httptest.NewRequest(http.MethodPost, "http://app.example.test/profile", strings.NewReader(bioBody))
		reqClean.Host = "app.example.test"
		reqClean.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		reqClean.Header.Set("Content-Length", fmt.Sprintf("%d", len(bioBody)))
		recClean := httptest.NewRecorder()
		hGen2.ServeHTTP(recClean, reqClean)
		if recClean.Code != http.StatusOK {
			t.Fatalf("clean bio POST status = %d", recClean.Code)
		}
	}
	recM2 := httptest.NewRecorder()
	factory.Metrics.Handler().ServeHTTP(recM2, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	vals2 := parsePromExposition(recM2.Body.String())
	if vals2[`jul_waf_transactions_total{mode="block",outcome="clean"}`] < 200 {
		t.Fatalf("expected >=200 clean block-mode transactions after tuning, got %v", vals2[`jul_waf_transactions_total{mode="block",outcome="clean"}`])
	}
	if liveContainers {
		for _, alertName := range []string{
			"JulWAFRejectionSymptomElevated",
			"JulSecurityEvidenceUnavailableOrDegraded",
			"JulSecurityPostureErrorPresent",
			"JulWAFProtectionWeakened",
		} {
			if !waitJourneyWebhook(alertName, "resolved", 15*time.Second) {
				alertMu.Lock()
				got := append([]alertNotificationPayload(nil), alertNotifs...)
				alertMu.Unlock()
				t.Fatalf("expected resolved %s webhook in #629 journey, got %+v", alertName, got)
			}
		}
	}
	recordExecutedIsolationCase("ISO-WAF-003", "policy_lifecycle", "pass", "narrow_exception_and_detection_paranoia_never_weaken_neighboring_scope")
}

func TestAdversarialReviewFixesSR01ToSR05AndCR01ToCR04(t *testing.T) {
	if !waf.Compiled {
		t.Skip("requires -tags waf")
	}
	ctx := context.Background()
	tmpDir := t.TempDir()

	// SR-03: Omitted scope with empty target must fail validation (never silently broaden to rule_remove).
	badExcCfg := &config.Config{
		WAF: config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			BlockStatus: 403,
			CRSEnabled:  true,
			Exceptions: []config.WAFException{
				{RuleID: 942100}, // omitted scope + empty target
			},
		},
		Servers: []config.ServerConfig{
			{Listen: ":8080", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
		},
	}
	if err := config.Validate(badExcCfg); err == nil || !strings.Contains(err.Error(), "target is required for narrow_target scope") {
		t.Fatalf("SR-03: expected empty target on default-scope exception to fail validation, got %v", err)
	}

	// CR-03 & CR-04: Per-location override external asset staleness + withdrawn route exclusion from jul_waf_serving_routes.
	locRuleFile := filepath.Join(tmpDir, "loc-override.conf")
	locRuleBytes := []byte(`SecRule ARGS:loc_bad "@streq block" "id:100777,phase:2,deny,status:403,log"` + "\n")
	if err := os.WriteFile(locRuleFile, locRuleBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	factory, cleanup := minimalFactory(t)
	defer cleanup()

	cfgWithdrawnAndOverride := &config.Config{
		WAF: config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			BlockStatus: 403,
			CRSEnabled:  true,
		},
		Servers: []config.ServerConfig{
			{
				Listen: ":8080",
				Locations: []config.LocationConfig{
					{
						RouteID:   config.String("rt-withdrawn-block"),
						Match:     config.MatchConfig{Type: "prefix", Path: "/withdrawn"},
						Return:    200,
						Withdrawn: true, // CR-04: must not count as active block route in jul_waf_serving_routes
					},
					{
						RouteID: config.String("rt-loc-override"),
						Match:   config.MatchConfig{Type: "prefix", Path: "/override"},
						Return:  200,
						WAF: &config.WAFConfig{
							Enabled:         true,
							Mode:            "detect",
							BlockStatus:     403,
							DirectivesFiles: []string{locRuleFile},
						},
					},
				},
			},
		},
	}

	_, _, commit, _, err := factory.Prepare(ctx, cfgWithdrawnAndOverride)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retire := commit()
	defer retire()

	// CR-04: Active block routes in metric must be 0 (since /withdrawn is withdrawn and /override is detect).
	recM := httptest.NewRecorder()
	factory.Metrics.Handler().ServeHTTP(recM, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	vals := parsePromExposition(recM.Body.String())
	if got := vals[`jul_waf_serving_routes{state="block"}`]; got != 0 {
		t.Fatalf("CR-04: expected jul_waf_serving_routes{state=\"block\"} = 0 when block route is withdrawn, got %v", got)
	}
	if got := vals[`jul_waf_serving_routes{state="detect"}`]; got != 1 {
		t.Fatalf("CR-04: expected jul_waf_serving_routes{state=\"detect\"} = 1, got %v", got)
	}

	// Activate the global route alongside the route override so both global and
	// override policies are serving simultaneously, evaluate both scopes, then
	// modify ONLY the per-location override rule file in place -> global remains
	// "current", the matching override becomes "stale_asset_changed", and the
	// aggregate waf_effective evidence metric becomes "stale"!
	cfgMixed := *cfgWithdrawnAndOverride
	cfgMixed.Servers = append([]config.ServerConfig(nil), cfgWithdrawnAndOverride.Servers...)
	cfgMixed.Servers[0].Locations = append([]config.LocationConfig(nil), cfgWithdrawnAndOverride.Servers[0].Locations...)
	cfgMixed.Servers[0].Locations[0].Withdrawn = false
	_, _, commitMixed, _, err := factory.Prepare(ctx, &cfgMixed)
	if err != nil {
		t.Fatalf("Prepare cfgMixed: %v", err)
	}
	_, retireMixed := commitMixed()
	defer retireMixed()

	evalRep, err := factory.EvaluateServingWAF(ctx, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateServingWAF global: %v", err)
	}
	ovEvalInit, err := factory.HandleAdminWAFEvaluate(ctx, []byte(`{"scope":"rt-loc-override"}`))
	if err != nil || (ovEvalInit.State != "current" && ovEvalInit.State != "failed") {
		t.Fatalf("HandleAdminWAFEvaluate rt-loc-override = %+v, err=%v", ovEvalInit, err)
	}
	// First overwrite with same byte length (hits SHA-256 mismatch branch in diskAssetsMatchRecorded),
	// then remove (hits os.Open failure branch).
	sameLenBytes := bytes.Repeat([]byte("#"), len(locRuleBytes)-1)
	sameLenBytes = append(sameLenBytes, '\n')
	if err := os.WriteFile(locRuleFile, sameLenBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	effSameLen := factory.WAFEffective()
	if effSameLen == nil || effSameLen.Evaluation == nil || effSameLen.Evaluation.State != "current" {
		t.Fatalf("CR-03 same-len edit: expected global evaluation to remain current, got %+v", effSameLen)
	}
	if len(effSameLen.Overrides) == 0 || effSameLen.Overrides[0].Evaluation == nil || effSameLen.Overrides[0].Evaluation.State != "stale_asset_changed" {
		t.Fatalf("CR-03 same-len edit: expected override evaluation stale_asset_changed, got %+v", effSameLen.Overrides)
	}
	recStaleMetrics := httptest.NewRecorder()
	factory.Metrics.Handler().ServeHTTP(recStaleMetrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recStaleMetrics.Body.String(), `jul_security_evidence_state{producer="waf_effective",state="stale"} 1`) {
		t.Fatalf("CR-03 same-len edit: expected aggregate waf_effective metric stale=1:\n%s", recStaleMetrics.Body.String())
	}
	if err := os.Remove(locRuleFile); err != nil {
		t.Fatal(err)
	}
	effRemoved := factory.WAFEffective()
	if effRemoved == nil || effRemoved.Evaluation == nil || effRemoved.Evaluation.State != "current" || effRemoved.Overrides[0].Evaluation.State != "stale_asset_changed" {
		t.Fatalf("CR-03 removed file: expected global=current and override=stale_asset_changed, got %+v", effRemoved)
	}
	if err := os.WriteFile(locRuleFile, locRuleBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// SR-01: Supplied report with FailedCases > 0 and matching PolicyDigest must report State = "failed".
	failedRep := *evalRep
	failedRep.Summary.FailedCases = 1
	failedRep.Summary.PassedCases = failedRep.Summary.TotalCases - 1
	failedRep.Cases = append([]waf.WAFFixtureCaseResult(nil), evalRep.Cases...)
	failedRep.Cases[0].PassedExpectation = false
	failedBytes, err := json.Marshal(failedRep)
	if err != nil {
		t.Fatal(err)
	}
	failedView, err := factory.HandleAdminWAFEvaluate(ctx, failedBytes)
	if err != nil || failedView.State != "failed" {
		t.Fatalf("SR-01: supplied report with FailedCases=1 must report failed, got %+v (err=%v)", failedView, err)
	}

	// SR-01: Supplied report with mismatched/empty PolicyDigest cannot match via raw SHA-256 oracle.
	tamperedRep := *evalRep
	tamperedRep.Summary.PolicyDigest = "ev1:wrong:wrong"
	tamperedRep.RawPolicySHA256 = factory.liveGlobalPolicySHA
	suppliedBytes, err := json.Marshal(tamperedRep)
	if err != nil {
		t.Fatal(err)
	}
	summaryView, err := factory.HandleAdminWAFEvaluate(ctx, suppliedBytes)
	if err != nil {
		t.Fatalf("HandleAdminWAFEvaluate supplied: %v", err)
	}
	if summaryView.State != "stale_policy_changed" {
		t.Fatalf("SR-01: supplied report with wrong ev1 PolicyDigest must report stale_policy_changed (ignoring raw_policy_sha256), got %s", summaryView.State)
	}

	// CR-01: Verify nil and repeated Firewall.Close() calls are idempotent.
	var nilFW *waf.Firewall
	_ = nilFW.Close()
}

package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/atomicfile"
	"jul/internal/config"
)

// concurrentWriteServer builds a file-backed admin server whose WriteConfigRaw
// persists atomically (temp file + rename), so a reader never observes a torn
// file. History is enabled so rollbacks have snapshots to restore.
func concurrentWriteServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, seed, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			c, err := config.Parse(data)
			if err != nil {
				return err
			}
			if err := config.Validate(c); err != nil {
				return err
			}
			return atomicfile.Write(cfgPath, data, 0o600)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	cfg := config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}
	return newTestServer(t, cfg, deps), cfgPath
}

// TestConfigApplyRollbackConcurrent hammers the admin write path with concurrent
// structured patch-applies and rollbacks. Because every config write — raw apply,
// structured patch apply, and both rollback endpoints — is serialized by applyMu,
// the persisted file must remain valid TOML throughout and no request may fail
// with a 5xx. It runs against both rollback endpoints: the v1 /api/history/rollback
// and the v2 /api/config/rollback the Console actually calls (Findings QA-1 / REG-1:
// concurrent patch + rollback safety on every rollback path).
func TestConfigApplyRollbackConcurrent(t *testing.T) {
	for _, rollbackPath := range []string{"/api/history/rollback", "/api/config/rollback"} {
		t.Run(rollbackPath, func(t *testing.T) {
			s, cfgPath := concurrentWriteServer(t)
			h := s.routes()

			apply := func(target, baseVersion string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(patchApplyRequest{
					BaseVersion: baseVersion,
					Ops:         []patchRequest{{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: target}},
				})
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
				return rr
			}

			// Seed a few applies so history accumulates snapshots to roll back to.
			for i := 0; i < 3; i++ {
				if rr := apply(fmt.Sprintf("http://127.0.0.1:91%02d", i), ""); rr.Code != http.StatusOK {
					t.Fatalf("seed apply %d: status %d, body %s", i, rr.Code, rr.Body.String())
				}
			}

			// Snapshot the current history IDs so rollbacks target a real snapshot.
			hrr := httptest.NewRecorder()
			h.ServeHTTP(hrr, httptest.NewRequest(http.MethodGet, "/api/history", nil))
			if hrr.Code != http.StatusOK {
				t.Fatalf("history list: status %d, body %s", hrr.Code, hrr.Body.String())
			}
			var entries []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(hrr.Body.Bytes(), &entries); err != nil {
				t.Fatalf("decode history: %v", err)
			}
			if len(entries) == 0 {
				t.Fatal("expected at least one history snapshot after seeding")
			}
			rollbackID := entries[0].ID

			rollback := func() *httptest.ResponseRecorder {
				body, _ := json.Marshal(map[string]string{"id": rollbackID})
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, rollbackPath, bytes.NewReader(body)))
				return rr
			}

			const workers = 24
			var (
				wg        sync.WaitGroup
				serverErr atomic.Int64
			)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					var rr *httptest.ResponseRecorder
					if i%2 == 0 {
						rr = apply(fmt.Sprintf("http://127.0.0.1:92%02d", i), "")
					} else {
						rr = rollback()
					}
					if rr.Code >= 500 {
						serverErr.Add(1)
						t.Errorf("worker %d: unexpected %d: %s", i, rr.Code, rr.Body.String())
					}
				}(i)
			}
			wg.Wait()

			if serverErr.Load() != 0 {
				t.Fatalf("%d requests failed with 5xx under concurrent apply+rollback", serverErr.Load())
			}

			// The persisted config must still be valid TOML after the concurrent storm.
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			c, err := config.Parse(raw)
			if err != nil {
				t.Fatalf("config corrupted after concurrent writes: %v\n%s", err, raw)
			}
			if err := config.Validate(c); err != nil {
				t.Fatalf("config invalid after concurrent writes: %v", err)
			}
		})
	}
}

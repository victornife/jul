package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"jul/internal/config"
)

// UpstreamStatus is the console view of one upstream pool. The composition root
// adapts its internal pool snapshot into this shape so the admin package stays
// decoupled from the upstream package.
type UpstreamStatus struct {
	Name     string          `json:"name"`
	Strategy string          `json:"strategy"`
	Backends []BackendStatus `json:"backends"`
}

// BackendStatus is the console view of one backend within a pool.
type BackendStatus struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Healthy  bool   `json:"healthy"`
	Inflight int64  `json:"inflight"`
}

// CertStatus is the console view of one configured certificate. It never
// carries private-key material.
type CertStatus struct {
	ServerNames []string  `json:"server_names"`
	Source      string    `json:"source"`
	Subject     string    `json:"subject,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	DNSNames    []string  `json:"dns_names,omitempty"`
	NotBefore   time.Time `json:"not_before,omitempty"`
	NotAfter    time.Time `json:"not_after,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// handleUpstreams serves the live upstream pools and backend health as JSON. It
// returns an empty array (not null) when no hook is wired so the console renders
// a clean empty state.
func (s *Server) handleUpstreams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []UpstreamStatus{}
	if s.deps.Upstreams != nil {
		if got := s.deps.Upstreams(); got != nil {
			out = got
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCerts serves the configured-certificate panel data as JSON.
func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []CertStatus{}
	if s.deps.Certs != nil {
		if got := s.deps.Certs(); got != nil {
			out = got
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// wizardInput is the setup-wizard request: serve a directory or proxy a target.
type wizardInput struct {
	Mode   string `json:"mode"`   // "serve" | "proxy"
	Path   string `json:"path"`   // serve: directory to serve
	Target string `json:"target"` // proxy: upstream target
	Listen string `json:"listen"` // optional listen address
}

// handleWizard synthesizes a starter configuration from the wizard inputs and
// returns it as TOML for review in the editor. It is non-mutating: the operator
// applies it through the validated /api/config/raw path, which also snapshots
// the prior config for rollback. The generated config is validated here so the
// wizard never proposes a config the editor would reject.
func (s *Server) handleWizard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var in wizardInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var cfg *config.Config
	switch strings.ToLower(strings.TrimSpace(in.Mode)) {
	case "serve":
		if strings.TrimSpace(in.Path) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "serve mode requires a directory path"})
			return
		}
		cfg = config.ServeDir(in.Path, in.Listen)
	case "proxy":
		if strings.TrimSpace(in.Target) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "proxy mode requires a target"})
			return
		}
		cfg = config.ProxyTarget(in.Target, in.Listen)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "serve" or "proxy"`})
		return
	}
	if err := config.Validate(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	toml, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"toml": string(toml)})
}

// handleHistoryList serves the configuration snapshot index, newest first.
func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	entries, err := s.hist.list()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []historyEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleHistoryGet serves the raw TOML of a single snapshot for preview, keyed
// by the ?id= query parameter.
func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.URL.Query().Get("id")
	raw, err := s.hist.get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "raw": string(raw)})
}

// handleHistoryRollback re-applies a stored snapshot through the validated raw
// write path, which reloads on success. The running config is snapshotted first
// so a rollback is itself reversible.
func (s *Server) handleHistoryRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	raw, err := s.hist.get(req.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled back", "id": req.ID})
}

// currentRaw reads the running raw configuration, or nil when unavailable. It is
// used to snapshot the prior config just before a successful edit.
func (s *Server) currentRaw() []byte {
	if s.deps.ReadConfigRaw == nil {
		return nil
	}
	raw, err := s.deps.ReadConfigRaw()
	if err != nil {
		return nil
	}
	return raw
}

// recordHistory snapshots the prior configuration after a successful edit.
// Snapshot failures are logged but never surfaced to the operator: the edit
// already succeeded, and a missing snapshot must not look like a failed save.
func (s *Server) recordHistory(prev []byte) {
	if len(prev) == 0 || !s.hist.enabled() {
		return
	}
	if _, err := s.hist.snapshot(prev); err != nil && s.log != nil {
		s.log.Warn("config history snapshot failed", "error", err)
	}
}

// methodNotAllowed writes a 405 with the permitted method advertised.
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
}

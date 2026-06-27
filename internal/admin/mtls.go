package admin

import (
	"fmt"
	"net/http"
	"strings"

	"jul/internal/config"
)

// clientAuthDef is the server_set_client_auth payload: the guided editor's view
// of a server block's mutual-TLS (client-certificate) settings. It mirrors
// config.ClientAuthConfig. A "none"/empty mode disables mutual TLS. The
// near-side checks (mode keyword, ca_file present for request/require) mirror
// the validator so the operator gets a clear message before the diff; the
// validated SaveConfig re-parse still enforces that ca_file/crl_file are
// readable, so a structured edit never bypasses validation.
type clientAuthDef struct {
	Mode      string   `json:"mode"` // none (default) | request | require
	CAFile    string   `json:"ca_file,omitempty"`
	CRLFile   string   `json:"crl_file,omitempty"`
	VerifySAN []string `json:"verify_san,omitempty"`
}

// buildClientAuth constructs a *config.ClientAuthConfig from the guided editor
// payload, returning nil when mutual TLS is disabled (mode none/empty) so the
// serialized config omits the [tls.client_auth] table entirely. It returns a
// short label for the audit summary.
func buildClientAuth(in clientAuthDef) (*config.ClientAuthConfig, string, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	switch mode {
	case "", "none":
		return nil, "off", nil
	case "request", "require":
	default:
		return nil, "", fmt.Errorf("mode must be %q, %q, or %q", "none", "request", "require")
	}
	caFile := strings.TrimSpace(in.CAFile)
	if caFile == "" {
		return nil, "", fmt.Errorf("ca_file is required for mode %q", mode)
	}
	ca := &config.ClientAuthConfig{
		Mode:      mode,
		CAFile:    caFile,
		CRLFile:   strings.TrimSpace(in.CRLFile),
		VerifySAN: trimSANList(in.VerifySAN),
	}
	return ca, clientAuthSummary(ca), nil
}

// trimSANList returns a copy of in with blank entries dropped and the rest
// trimmed, or nil when nothing remains, so a serialized client_auth omits an
// empty verify_san list.
func trimSANList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clientAuthSummary renders a client_auth block for an audit summary: its mode
// plus the SAN allow-list size when present.
func clientAuthSummary(ca *config.ClientAuthConfig) string {
	if !ca.Active() {
		return "off"
	}
	out := strings.ToLower(strings.TrimSpace(ca.Mode))
	if n := len(ca.VerifySAN); n > 0 {
		out += fmt.Sprintf(" + %d SAN%s", n, plural(n))
	}
	return out
}

// serverRequireClientCertPaths returns the match paths of the server's
// locations that require a client certificate, so server_set_client_auth can
// refuse to disable mutual TLS while a route still depends on it (mirroring the
// validator, with a clearer message).
func serverRequireClientCertPaths(srv *config.ServerConfig) []string {
	var out []string
	for i := range srv.Locations {
		if srv.Locations[i].RequireClientCert {
			out = append(out, srv.Locations[i].Match.Path)
		}
	}
	return out
}

// ── mTLS projection (v2 API contract) ────────────────────────────────────────

// MTLSProjection is the Console v2 Mutual TLS payload: every TLS-enabled server
// block with its client-certificate posture and the per-location
// require_client_cert flags, so the editor can seed and the panel can list.
type MTLSProjection struct {
	Servers []MTLSServerProjection `json:"servers"`
}

// MTLSServerProjection is one TLS-enabled server's mutual-TLS state. Mode is
// normalized to "none" when client_auth is absent or inactive, so the editor
// seeds a faithful round-trip and can enable it on a server that lacks it.
type MTLSServerProjection struct {
	Listen      string                   `json:"listen"`
	ServerNames []string                 `json:"server_names"`
	Mode        string                   `json:"mode"` // none | request | require
	CAFile      string                   `json:"ca_file,omitempty"`
	CRLFile     string                   `json:"crl_file,omitempty"`
	VerifySAN   []string                 `json:"verify_san,omitempty"`
	Locations   []MTLSLocationProjection `json:"locations"`
}

// MTLSLocationProjection is one location's per-route client-certificate
// requirement, addressed by its match type + path.
type MTLSLocationProjection struct {
	Match             string `json:"match"`
	Type              string `json:"type"`
	RequireClientCert bool   `json:"require_client_cert"`
}

// projectMTLS builds the Mutual TLS projection: every TLS-enabled server (mTLS
// only applies on a TLS listener), with its client_auth posture and locations.
func projectMTLS(c *config.Config) MTLSProjection {
	out := MTLSProjection{Servers: make([]MTLSServerProjection, 0)}
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		sp := MTLSServerProjection{
			Listen:      srv.Listen,
			ServerNames: srv.ServerNames,
			Mode:        "none",
			Locations:   make([]MTLSLocationProjection, 0, len(srv.Locations)),
		}
		if ca := srv.TLS.ClientAuth; ca != nil && ca.Active() {
			sp.Mode = strings.ToLower(strings.TrimSpace(ca.Mode))
			sp.CAFile = ca.CAFile
			sp.CRLFile = ca.CRLFile
			sp.VerifySAN = ca.VerifySAN
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			sp.Locations = append(sp.Locations, MTLSLocationProjection{
				Match:             loc.Match.Path,
				Type:              loc.Match.Type,
				RequireClientCert: loc.RequireClientCert,
			})
		}
		out.Servers = append(out.Servers, sp)
	}
	return out
}

// handleMTLS serves the Mutual TLS panel projection. GET /api/mtls
func (s *Server) handleMTLS(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectMTLS(c))
	})(w, r)
}

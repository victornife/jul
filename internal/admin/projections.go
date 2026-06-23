package admin

import (
	"fmt"
	"jul/internal/config"
)

// ── Projection types (v2 API contract) ──────────────────────────────────────

// RouteProjection is a structured route for the Console v2 Routes panel.
type RouteProjection struct {
	Listen      string               `json:"listen"`
	ServerNames []string             `json:"server_names,omitempty"`
	TLS         *TLSProjection       `json:"tls,omitempty"`
	HTTP3       bool                 `json:"http3"`
	H2C         bool                 `json:"h2c"`
	Locations   []LocationProjection `json:"locations"`
}

// LocationProjection is a structured location within a route.
type LocationProjection struct {
	Match  string `json:"match"`
	Type   string `json:"type"`   // exact, prefix, regex
	Action string `json:"action"` // static, proxy, grpc, grpc_transcode, fastcgi, redirect, deny
	Target string `json:"target,omitempty"`
	Auth   bool   `json:"auth"`
	Cache  bool   `json:"cache"`
	Secure bool   `json:"secure"` // TLS required
}

// AppProjection is a structured upstream/app for the Console v2 Apps panel.
type AppProjection struct {
	Name        string              `json:"name"`
	Strategy    string              `json:"strategy"`
	Backends    []BackendProjection `json:"backends"`
	HealthCheck bool                `json:"health_check"`
	Discovery   string              `json:"discovery,omitempty"`
}

// BackendProjection is one backend server in an upstream pool.
type BackendProjection struct {
	Address string `json:"address"`
	Weight  int    `json:"weight"`
}

// TLSProjection is certificate/TLS state for the TLS & Certificates panel.
type TLSProjection struct {
	Enabled    bool   `json:"enabled"`
	ACME       bool   `json:"acme"`
	ClientAuth string `json:"client_auth,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
}

// CertProjection is one certificate entry for the TLS panel.
type CertProjection struct {
	ServerNames []string `json:"server_names"`
	Source      string   `json:"source"`
	Issuer      string   `json:"issuer,omitempty"`
	NotAfter    string   `json:"not_after,omitempty"`
	DaysLeft    int      `json:"days_left,omitempty"`
}

// SecurityProjection is the security posture for the Security panel.
type SecurityProjection struct {
	AuthEnabled      bool   `json:"auth_enabled"`
	ClientAuth       string `json:"client_auth,omitempty"`
	BodyLimit        string `json:"body_limit,omitempty"`
	RequireCertCount int    `json:"require_cert_count"`
}

// TrafficControlsProjection is the traffic/observability settings panel.
type TrafficControlsProjection struct {
	Compression *CompressionProjection `json:"compression,omitempty"`
	RateLimit   *RateLimitProjection   `json:"rate_limit,omitempty"`
	Cache       *CacheProjection       `json:"cache,omitempty"`
}

// CompressionProjection is compression configuration.
type CompressionProjection struct {
	Enabled  bool     `json:"enabled"`
	Encoders []string `json:"encoders,omitempty"`
}

// RateLimitProjection is rate limiting configuration.
type RateLimitProjection struct {
	Enabled bool   `json:"enabled"`
	Rate    int    `json:"rate,omitempty"`
	Burst   int    `json:"burst,omitempty"`
	Key     string `json:"key,omitempty"`
}

// CacheProjection is cache configuration.
type CacheProjection struct {
	Enabled    bool   `json:"enabled"`
	DefaultTTL string `json:"default_ttl,omitempty"`
	MemoryMax  string `json:"memory_max,omitempty"`
	DiskPath   string `json:"disk_path,omitempty"`
}

// RuntimeOverview is the top-level dashboard summary.
type RuntimeOverview struct {
	Product string          `json:"product"`
	Version string          `json:"version"`
	Status  []FeatureStatus `json:"status"` // existing 21-row backbone
	Stats   interface{}     `json:"stats,omitempty"`
}

// ── Projection helpers ──────────────────────────────────────────────────────

func projectRoutes(c *config.Config) []RouteProjection {
	out := make([]RouteProjection, 0, len(c.Servers))
	for i := range c.Servers {
		srv := &c.Servers[i]
		rp := RouteProjection{
			Listen:      srv.Listen,
			ServerNames: srv.ServerNames,
			H2C:         srv.H2C,
			Locations:   make([]LocationProjection, 0, len(srv.Locations)),
		}
		if srv.HTTP3 != nil && srv.HTTP3.Enabled {
			rp.HTTP3 = true
		}
		if srv.TLS != nil && srv.TLS.Enabled {
			tls := &TLSProjection{Enabled: true, MinVersion: srv.TLS.MinVersion}
			tls.ACME = srv.TLS.ACME != nil && srv.TLS.ACME.Enabled
			if srv.TLS.ClientAuth != nil && srv.TLS.ClientAuth.Active() {
				tls.ClientAuth = srv.TLS.ClientAuth.Mode
			}
			rp.TLS = tls
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			lp := LocationProjection{
				Match:  loc.Match.Path,
				Type:   loc.Match.Type,
				Auth:   loc.Auth != nil,
				Cache:  loc.Cache,
				Secure: srv.TLS != nil && srv.TLS.Enabled,
			}
			switch {
			case loc.GRPCTranscode != nil:
				lp.Action = "grpc_transcode"
				lp.Target = loc.ProxyPass
			case loc.GRPC:
				lp.Action = "grpc"
				lp.Target = loc.ProxyPass
			case loc.ProxyPass != "":
				lp.Action = "proxy"
				lp.Target = loc.ProxyPass
			case loc.FastCGIPass != "", loc.UWSGIPass != "":
				lp.Action = "fastcgi"
				lp.Target = firstNonEmpty(loc.FastCGIPass, loc.UWSGIPass)
			case loc.Redirect != "":
				lp.Action = "redirect"
				lp.Target = loc.Redirect
			case loc.Deny:
				lp.Action = "deny"
			case loc.Root != "":
				lp.Action = "static"
				lp.Target = loc.Root
			default:
				lp.Action = "unknown"
			}
			rp.Locations = append(rp.Locations, lp)
		}
		out = append(out, rp)
	}
	return out
}

func projectApps(c *config.Config) []AppProjection {
	out := make([]AppProjection, 0, len(c.Upstreams))
	for i := range c.Upstreams {
		up := &c.Upstreams[i]
		ap := AppProjection{
			Name:     up.Name,
			Strategy: up.Strategy,
			Backends: make([]BackendProjection, 0, len(up.Servers)),
		}
		if up.HealthCheck != nil {
			ap.HealthCheck = up.HealthCheck.Enabled
		}
		if up.Discovery != nil {
			ap.Discovery = up.Discovery.Type
		}
		for _, b := range up.Servers {
			ap.Backends = append(ap.Backends, BackendProjection{
				Address: b.Address,
				Weight:  b.Weight,
			})
		}
		out = append(out, ap)
	}
	return out
}

func projectTLS(c *config.Config) []CertProjection {
	var certs []CertProjection
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		cp := CertProjection{
			ServerNames: srv.ServerNames,
		}
		if srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			cp.Source = "acme"
		} else {
			cp.Source = "file"
		}
		certs = append(certs, cp)
	}
	return certs
}

func projectSecurity(c *config.Config) SecurityProjection {
	sp := SecurityProjection{}
	for i := range c.Servers {
		srv := &c.Servers[i]
		if srv.TLS != nil && srv.TLS.ClientAuth != nil && srv.TLS.ClientAuth.Mode != "" && srv.TLS.ClientAuth.Mode != "none" {
			sp.ClientAuth = srv.TLS.ClientAuth.Mode
		}
		if srv.ClientMaxBodySize > 0 {
			sp.BodyLimit = fmt.Sprintf("%d", srv.ClientMaxBodySize)
		}
		for j := range srv.Locations {
			loc := &srv.Locations[j]
			if loc.Auth != nil {
				sp.AuthEnabled = true
			}
			if loc.RequireClientCert {
				sp.RequireCertCount++
			}
		}
	}
	return sp
}

func projectTrafficControls(c *config.Config) TrafficControlsProjection {
	tcp := TrafficControlsProjection{}
	if c.Compression.Enabled {
		tcp.Compression = &CompressionProjection{}
		tcp.Compression.Enabled = true
		tcp.Compression.Encoders = c.Compression.Encoders
	}
	if c.RateLimit.Enabled {
		tcp.RateLimit = &RateLimitProjection{}
		tcp.RateLimit.Enabled = true
		tcp.RateLimit.Rate = c.RateLimit.Rate
		tcp.RateLimit.Burst = c.RateLimit.Burst
		tcp.RateLimit.Key = c.RateLimit.Key
	}
	if c.Cache.Enabled {
		tcp.Cache = &CacheProjection{}
		tcp.Cache.Enabled = true
		tcp.Cache.DefaultTTL = string(mustMarshal(c.Cache.DefaultTTL.MarshalText()))
		tcp.Cache.MemoryMax = string(mustMarshal(c.Cache.MemoryMaxSize.MarshalText()))
		tcp.Cache.DiskPath = c.Cache.DiskPath
	}
	return tcp
}

func mustMarshal(b []byte, _ error) []byte { return b }
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

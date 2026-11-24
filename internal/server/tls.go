// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"jul/internal/config"
)

// CertProvider supplies certificates for TLS handshakes. v1 ships a
// file-backed implementation; future versions (e.g. ACME/autocert) can satisfy
// the same interface without touching the listener wiring.
type CertProvider interface {
	// GetCertificate selects a certificate for a ClientHello (SNI).
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// fileCertProvider loads certificates from disk and selects them by SNI server
// name, supporting exact and wildcard (*.example.com) matches.
type fileCertProvider struct {
	mu      sync.RWMutex
	byName  map[string]*tls.Certificate // server_name -> cert
	wild    map[string]*tls.Certificate // ".example.com" -> cert
	fallbck *tls.Certificate            // first loaded cert
	fp      string                      // tlsIdentityFingerprint of the exact loaded cert/key bytes
	// summaries is built once at construction from the certificates actually
	// loaded here, never by re-reading the configured paths later. It is the
	// live-status source of truth (#100): LiveCertSummaries reads this instead
	// of the file the configured path currently names, so an out-of-band
	// rewrite between reloads cannot make an unpublished certificate appear
	// live. Immutable after construction; safe for concurrent reads.
	summaries []CertSummary
}

// certBinding pairs a TLS config with the server names it should serve.
type certBinding struct {
	tls   *config.TLSConfig
	names []string
}

// NewSingleCertProvider loads exactly one cert/key pair with no SNI
// multiplexing — the shape a listener serving one identity needs, rather
// than the data plane's per-virtual-host mapping. Exported for the admin
// listener (#336), which reuses this and DynamicCertProvider instead of
// building a second certificate-loading path.
func NewSingleCertProvider(cert, key string) (CertProvider, error) {
	return newFileCertProvider([]certBinding{{tls: &config.TLSConfig{Enabled: true, Cert: cert, Key: key}}})
}

// newFileCertProvider loads every distinct cert/key pair referenced by the
// given bindings and indexes them by server name for SNI selection.
func newFileCertProvider(bindings []certBinding) (*fileCertProvider, error) {
	p := &fileCertProvider{
		byName: make(map[string]*tls.Certificate),
		wild:   make(map[string]*tls.Certificate),
	}
	loaded := make(map[string]*tls.Certificate) // cert+key path -> cert (dedup)
	loadedHashes := make(map[string][2]string)  // cert+key path -> [certHash, keyHash]
	namesFor := make(map[string][]string)       // cert+key path -> aggregate configured-case names
	var order []string                          // cache keys in first-seen order, for deterministic summaries
	var fpEntries []string

	for _, b := range bindings {
		if b.tls == nil || !b.tls.Enabled {
			continue
		}
		cacheKey := b.tls.Cert + "\x00" + b.tls.Key
		cert, ok := loaded[cacheKey]
		hashes := loadedHashes[cacheKey]
		if !ok {
			certPEM, err := readBoundedMTLSFile(b.tls.Cert, 4<<20)
			if err != nil {
				return nil, fmt.Errorf("load cert %q/%q: %w", b.tls.Cert, b.tls.Key, err)
			}
			keyPEM, err := readBoundedMTLSFile(b.tls.Key, 4<<20)
			if err != nil {
				return nil, fmt.Errorf("load cert %q/%q: %w", b.tls.Cert, b.tls.Key, err)
			}
			c, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("load cert %q/%q: %w", b.tls.Cert, b.tls.Key, err)
			}
			cSum := sha256.Sum256(certPEM)
			kSum := sha256.Sum256(keyPEM)
			hashes = [2]string{hex.EncodeToString(cSum[:]), hex.EncodeToString(kSum[:])}
			cert = &c
			loaded[cacheKey] = cert
			loadedHashes[cacheKey] = hashes
			order = append(order, cacheKey)
		}
		if p.fallbck == nil {
			p.fallbck = cert
		}
		namesFor[cacheKey] = append(namesFor[cacheKey], b.names...)
		normNames := append([]string(nil), b.names...)
		for i := range normNames {
			normNames[i] = strings.ToLower(strings.TrimSpace(normNames[i]))
		}
		sort.Strings(normNames)
		fpEntries = append(fpEntries, fmt.Sprintf("names=%s;cert=%s;key=%s", strings.Join(normNames, ","), hashes[0], hashes[1]))
		for _, name := range b.names {
			name = strings.ToLower(name)
			if strings.HasPrefix(name, "*.") {
				p.wild[name[1:]] = cert // store ".example.com"
			} else {
				p.byName[name] = cert
			}
		}
	}
	if p.fallbck == nil {
		return nil, fmt.Errorf("tls enabled but no certificates loaded")
	}
	sort.Strings(fpEntries)
	p.fp = "file:" + strings.Join(fpEntries, "|")
	for _, key := range order {
		p.summaries = append(p.summaries, certSummaryFromCertificate(namesFor[key], loaded[key]))
	}
	return p, nil
}

// Summaries returns a live, secret-safe metadata summary of every distinct
// certificate this provider holds, derived from the certificate this
// provider actually has installed in memory. A certificate serving several
// server names is reported once, with the full aggregate name set (#100).
func (p *fileCertProvider) Summaries() []CertSummary {
	return append([]CertSummary(nil), p.summaries...)
}

// GetCertificate implements CertProvider, matching exact then wildcard names,
// falling back to the first loaded certificate.
func (p *fileCertProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSpace(hello.ServerName))
	p.mu.RLock()
	defer p.mu.RUnlock()

	if name != "" {
		if cert, ok := p.byName[name]; ok {
			return cert, nil
		}
		if i := strings.IndexByte(name, '.'); i >= 0 {
			if cert, ok := p.wild[name[i:]]; ok {
				return cert, nil
			}
		}
	}
	return p.fallbck, nil
}

// PreflightTLS validates that every file-based TLS certificate the configuration
// would load can actually be parsed, without binding any listener. It mirrors
// certProviderFor's per-address selection: an address that enables ACME obtains
// its certificate at handshake time, so its server blocks are skipped here;
// every other TLS-enabled server block must reference a loadable cert/key pair.
//
// This lets an apply fail fast with a clear error instead of surfacing a broken
// certificate only at the asynchronous reload — where the old runtime keeps
// serving while audit/history have already recorded the apply as successful.
func PreflightTLS(servers []config.ServerConfig) error {
	for i := range servers {
		srv := &servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		// ACME-served addresses obtain certificates at handshake time, so there
		// is no file pair to validate here (matching certProviderFor).
		if acmeEnabledForAddr(servers, srv.Listen) {
			continue
		}
		if _, err := tls.LoadX509KeyPair(srv.TLS.Cert, srv.TLS.Key); err != nil {
			return fmt.Errorf("server %s: tls certificate: %w", srv.Listen, err)
		}
	}
	return nil
}

// acmeFingerprint compares each listener's certificate identity and the
// process-owned manager's settings without exposing credential bytes.
func acmeFingerprint(servers []config.ServerConfig) string {
	byAddr := make(map[string]map[string]bool)
	var settings *config.ACMEConfig
	for i := range servers {
		srv := &servers[i]
		if srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		a := srv.TLS.ACME
		if settings == nil {
			settings = a
		}
		if byAddr[srv.Listen] == nil {
			byAddr[srv.Listen] = make(map[string]bool)
		}
		for _, d := range a.Domains {
			if a.Challenge == "dns-01" {
				d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
			}
			byAddr[srv.Listen][d] = true
		}
	}
	if settings == nil {
		return ""
	}
	h := sha256.New()
	groups := make(map[string]bool)
	for addr := range byAddr {
		domains := make([]string, 0, len(byAddr[addr]))
		for domain := range byAddr[addr] {
			domains = append(domains, domain)
			if settings.Challenge != "dns-01" {
				groups["domain\x00"+domain] = true
			}
		}
		sort.Strings(domains)
		if settings.Challenge == "dns-01" {
			groups[strings.Join(domains, "\x00")] = true
		} else {
			groups["address\x00"+addr] = true
		}
	}
	identities := make([]string, 0, len(groups))
	for group := range groups {
		identities = append(identities, group)
	}
	sort.Strings(identities)
	for _, identity := range identities {
		fmt.Fprintf(h, "%q,", identity)
	}
	fmt.Fprintf(h, "%q|%q|%q|%q|%q", settings.Email, settings.CA, settings.Challenge, settings.CacheDir, settings.DNSProvider)
	if d := settings.RFC2136; d != nil {
		fmt.Fprintf(h, "|%q|%q|%q|%q|%d|%d", d.Server, d.Zone, d.TSIGKeyName, d.TSIGSecret, d.PropagationTimeout, d.PollInterval)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ACMERestartRequired reports whether moving from old to next changes the ACME
// configuration in a way that cannot take effect without a restart. The autocert
// manager's issued-domain set and issuer are frozen when it is built at startup,
// so introducing ACME or changing its domains/issuer requires a restart and the
// returned reason is non-empty.
//
// Removing ACME does not require a restart: the per-address provider selection
// (certProviderFor) swaps to file certificates on the next reload, and
// PreflightTLS validates that those file certificates load. An unchanged ACME
// configuration likewise needs no restart.
func ACMERestartRequired(old, next []config.ServerConfig) (string, bool) {
	nextFP := acmeFingerprint(next)
	if nextFP == "" {
		return "", false // candidate uses no ACME (removal hot-applies)
	}
	if nextFP == acmeFingerprint(old) {
		return "", false // ACME configuration unchanged
	}
	return "automatic HTTPS (ACME) domains, issuer, cache, or DNS settings changed; the manager is fixed when the server starts", true
}

// MinTLSVersion maps a config string to a crypto/tls version constant,
// defaulting to TLS 1.2. Exported so the admin listener (#336) uses the exact
// same mapping as the data plane.
func MinTLSVersion(v string) uint16 {
	switch strings.TrimSpace(v) {
	case "1.3":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS12
	}
}

// DynamicCertProvider holds a swappable CertProvider so certificates can be
// reloaded without rebinding the listener: the listener's tls.Config keeps a
// stable GetCertificate callback that reads the current provider. The provider
// may be file-backed or ACME-backed; the listener wiring does not care which.
// Exported so a second listener outside internal/server's own address map
// (the admin listener, #336) can reuse this exact rotation seam instead of
// building a parallel one.
type DynamicCertProvider struct {
	current atomic.Pointer[certProviderHolder]
}

// certProviderHolder boxes a CertProvider so it can live in an atomic.Pointer
// (which needs a concrete element type) while still holding any implementation
// behind the interface.
type certProviderHolder struct{ p CertProvider }

// Set installs p as the provider consulted by the next handshake. Safe to
// call concurrently with GetCertificate; a handshake already in progress may
// still observe the previous provider (#100, #336).
func (d *DynamicCertProvider) Set(p CertProvider) {
	d.current.Store(&certProviderHolder{p: p})
}

func (d *DynamicCertProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	h := d.current.Load()
	if h == nil || h.p == nil {
		return nil, fmt.Errorf("no certificate provider configured")
	}
	return h.p.GetCertificate(hello)
}

type clientHelloValidator interface {
	validateClientHello(*tls.ClientHelloInfo) error
}

func (d *DynamicCertProvider) configForClient(next func(*tls.ClientHelloInfo) (*tls.Config, error)) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		h := d.current.Load()
		if h == nil || h.p == nil {
			return nil, fmt.Errorf("no certificate provider configured")
		}
		if validator, ok := h.p.(clientHelloValidator); ok {
			if err := validator.validateClientHello(hello); err != nil {
				return nil, err
			}
		}
		return next(hello)
	}
}

// ACMEManager is the seam between the listener wiring and an ACME certificate
// source (autocert today, a different implementation later) that keeps ACME
// specifics out of bind()/reload. It is satisfied by the acme build-tagged
// implementation and supplied by the composition root via Server.ACME.
type ACMEManager interface {
	// Provider returns a CertProvider that obtains and renews certificates for
	// the given domains on demand during the TLS handshake.
	Provider(domains []string) CertProvider
	// ChallengeHandler wraps next with the configured plain HTTP challenge
	// behavior. In HTTP-01 mode it answers requests under
	// /.well-known/acme-challenge/; in TLS-ALPN-01 mode it returns next unchanged.
	ChallengeHandler(next http.Handler) http.Handler
}

// ACMEOptions supplies process-owned resources for DNS-01.
type ACMEOptions struct {
	DNSDialContext func(context.Context, string, string) (net.Conn, error)
	Logger         *slog.Logger
}

type listenerScopedACMEManager interface {
	ProviderForListener(domains, serverNames []string) CertProvider
}

type stoppableACMEManager interface {
	Stop()
}

type acmeOCSPPolicy interface {
	SetOCSPStapling(bool)
}

func acmeOCSPStaplingForServers(servers []config.ServerConfig) (bool, bool) {
	for i := range servers {
		srv := &servers[i]
		if srv.TLS != nil && srv.TLS.Enabled && srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			return srv.TLS.ACME.OCSPStaplingEnabled(), true
		}
	}
	return false, false
}

// updateACMEOCSPPolicy changes only the stable manager's stapling policy. Lean
// builds and alternate ACMEManager implementations simply do not expose this
// optional seam.
func (s *Server) updateACMEOCSPPolicy(servers []config.ServerConfig) {
	policy, ok := s.ACME.(acmeOCSPPolicy)
	if !ok {
		return
	}
	enabled, configured := acmeOCSPStaplingForServers(servers)
	if configured {
		policy.SetOCSPStapling(enabled)
	}
}

func (s *Server) stopUnusedACMEManager(servers []config.ServerConfig) {
	if acmeFingerprint(servers) == "" {
		if manager, ok := s.ACME.(stoppableACMEManager); ok {
			manager.Stop()
		}
	}
}

func acmeListenerFingerprint(servers []config.ServerConfig, addr string) string {
	names := acmeServerNamesForAddr(servers, addr)
	for i := range names {
		names[i] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(names[i]), "."))
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return hex.EncodeToString(sum[:])
}

// certProviderFor selects the certificate provider for a TLS listen address.
// This is the single seam where the certificate source is chosen: ACME when a
// server block on the address enables it, static files otherwise. New sources
// (a secrets store, an internal CA, ...) plug in here without touching the
// listener lifecycle.
func (s *Server) certProviderFor(addr string, bindings []certBinding) (CertProvider, error) {
	if s.ACME != nil && acmeEnabledForAddr(s.cfg.Servers, addr) {
		domains := acmeDomainsForAddr(s.cfg.Servers, addr)
		if scoped, ok := s.ACME.(listenerScopedACMEManager); ok {
			return scoped.ProviderForListener(domains, acmeServerNamesForAddr(s.cfg.Servers, addr)), nil
		}
		return s.ACME.Provider(domains), nil
	}
	return newFileCertProvider(bindings)
}

// listenerNextProtos returns the ALPN protocols to advertise on addr's TLS
// listener. The reserved "acme-tls/1" protocol is exposed only when ACME is
// active and TLS-ALPN-01 is the configured challenge. HTTP-01 listeners offer
// only the ordinary HTTP protocols so the non-selected challenge surface is
// absent. The s.ACME nil-guard keeps this tag-free — lean builds never
// advertise acme-tls/1.
func (s *Server) listenerNextProtos(addr string) []string {
	if s.ACME != nil && acmeChallengeForAddr(s.cfg.Servers, addr) == "tls-alpn-01" {
		return []string{"acme-tls/1", "h2", "http/1.1"}
	}
	return []string{"h2", "http/1.1"}
}

// acmeEnabledForAddr reports whether any server block on addr enables ACME.
func acmeEnabledForAddr(servers []config.ServerConfig, addr string) bool {
	for _, srv := range servers {
		if srv.Listen == addr && srv.TLS != nil && srv.TLS.Enabled &&
			srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
			return true
		}
	}
	return false
}

// acmeChallengeForAddr returns the selected challenge for an ACME-enabled TLS
// address. Configuration validation requires all enabled ACME blocks to agree;
// the empty value is treated as the documented HTTP-01 default for defensive
// callers that construct Config values without running applyDefaults.
func acmeChallengeForAddr(servers []config.ServerConfig, addr string) string {
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		challenge := strings.TrimSpace(srv.TLS.ACME.Challenge)
		if challenge == "" {
			return "http-01"
		}
		return challenge
	}
	return ""
}

// acmeDomainsForAddr collects the de-duplicated ACME domains across every
// acme-enabled server block on addr, in first-seen order.
func acmeDomainsForAddr(servers []config.ServerConfig, addr string) []string {
	var domains []string
	seen := make(map[string]bool)
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		for _, d := range srv.TLS.ACME.Domains {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	return domains
}

func acmeServerNamesForAddr(servers []config.ServerConfig, addr string) []string {
	var names []string
	seen := make(map[string]bool)
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled ||
			srv.TLS.ACME == nil || !srv.TLS.ACME.Enabled {
			continue
		}
		for _, name := range srv.ServerNames {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

// tlsBindingsForAddr returns the cert bindings and min TLS version for a listen
// address, or ok=false when the address serves plain HTTP.
func tlsBindingsForAddr(servers []config.ServerConfig, addr string) (bindings []certBinding, minVer uint16, ok bool) {
	minVer = tls.VersionTLS12
	for _, srv := range servers {
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		ok = true
		bindings = append(bindings, certBinding{tls: srv.TLS, names: srv.ServerNames})
		if v := MinTLSVersion(srv.TLS.MinVersion); v > minVer {
			minVer = v
		}
	}
	return bindings, minVer, ok
}

// tlsConfigForAddr builds a *tls.Config for a listen address if any server
// block on it enables TLS, or returns nil when the listener is plain HTTP.
func tlsConfigForAddr(servers []config.ServerConfig, addr string) (*tls.Config, error) {
	bindings, minVer, ok := tlsBindingsForAddr(servers, addr)
	if !ok {
		return nil, nil
	}
	provider, err := newFileCertProvider(bindings)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: provider.GetCertificate,
		MinVersion:     minVer,
		NextProtos:     []string{"h2", "http/1.1"},
	}, nil
}

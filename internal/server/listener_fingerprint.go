// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"jul/internal/config"
)

// ListenerRebindRequired reports whether moving from old to next changes a
// bind-time-frozen listener setting on an address that is kept across the
// reload. bind() reads these settings once when it creates the net.Listener and
// http.Server for an address; doReload only swaps handlers, refreshes
// certificates, binds newly added addresses, and drains removed ones, so an
// edit to a frozen setting on a kept listener is persisted but never takes
// effect until the process restarts and rebinds. Rather than accept such an
// edit silently, the apply path rejects it with restart_required, mirroring
// ACMERestartRequired.
//
// Frozen settings covered: read/write/idle/header timeouts, max header bytes,
// h2c, HTTP/3 (and its Alt-Svc max-age), whether the listener is TLS at all,
// the TLS minimum version, the mutual-TLS bundle (mode, CA file, SAN allow-list,
// CRL file), and the per-listener connection cap (rate_limit.max_conns). Only
// addresses present in BOTH old and next are compared: a newly added address is
// bound fresh (picking up its settings) and a removed address is drained, both
// of which doReload's listener diff already handles.
//
// ACME-driven differences (the issued-domain set, the ALPN acme-tls/1 token)
// are intentionally out of scope here; ACMERestartRequired gates them so the
// restart reason stays specific.
func ListenerRebindRequired(old, next *config.Config) (string, bool) {
	oldAddrs := setOf(uniqueListenAddrs(old.Servers))
	for _, addr := range uniqueListenAddrs(next.Servers) {
		if _, kept := oldAddrs[addr]; !kept {
			continue // newly added address: bind() applies its settings
		}
		if listenerBindFingerprint(old, addr) != listenerBindFingerprint(next, addr) {
			return fmt.Sprintf(
				"listener %s has bind-time settings (timeouts, header limits, h2c, HTTP/3, TLS, mutual TLS, or connection cap) that changed; these are fixed when the listener binds and take effect on restart",
				addr,
			), true
		}
	}
	return "", false
}

// listenerBindFingerprint serializes every bind-time-frozen property of the
// listener that bind() would create for addr from cfg, so two fingerprints
// differ exactly when a hot reload could not reproduce the running listener.
// It reuses bind()'s own per-address resolver methods through a read-only
// Server value so the fingerprint can never drift from what bind() actually
// sets; the resolvers only read s.cfg.
func listenerBindFingerprint(cfg *config.Config, addr string) string {
	s := &Server{cfg: cfg}

	var b strings.Builder

	// The connection cap is global (rate_limit.max_conns) yet applied to every
	// listener at bind time, so a change forces a rebind of each kept listener.
	maxConns := 0
	if rl := cfg.RateLimit; rl.Enabled && rl.MaxConns > 0 {
		maxConns = rl.MaxConns
	}
	fmt.Fprintf(&b, "maxconns=%d;", maxConns)

	// Listener-level timeouts and the header-byte cap resolve from the first
	// server block on addr, exactly as the http.Server fields are set in bind().
	fmt.Fprintf(&b, "rht=%d;rt=%d;wt=%d;it=%d;mhb=%d;",
		s.readHeaderTimeout(addr), s.readTimeout(addr), s.writeTimeout(addr),
		s.idleTimeout(addr), s.maxHeaderBytes(addr))

	// The PROXY-protocol wrapper and the trust set it enforces are installed on
	// the raw listener at bind time, so both belong here: changing either must
	// rebind rather than leave the previous wrapper serving.
	fmt.Fprintf(&b, "proxyproto=%s,trust=%s;",
		strings.ToLower(strings.TrimSpace(s.proxyProtocolModeForAddr(addr))),
		s.proxyProtocolTrustForAddr(addr))

	_, minVer, tlsOK := tlsBindingsForAddr(cfg.Servers, addr)
	fmt.Fprintf(&b, "tls=%t;", tlsOK)
	if tlsOK {
		// TLS minimum version, the mutual-TLS bundle, and HTTP/3 are wired into
		// the TLS listener at bind time, so a change to any of them forces a
		// rebind. The static certificate identity (cert/key/SNI mapping) is
		// deliberately NOT part of this fingerprint: #100 hot-swaps it into the
		// existing dynamicCertProvider without a rebind (see
		// prepareCertRotation and listenerEntry.certFingerprint), while an
		// ACME domain-set change is still gated separately by
		// ACMERestartRequired so its restart reason stays specific.
		fmt.Fprintf(&b, "minver=%d;mtls=%s;", minVer, mtlsConfigFingerprint(cfg.Servers, addr))
		if s.http3EnabledForAddr(addr) {
			fmt.Fprintf(&b, "h3=1,ma=%d;", s.http3MaxAgeForAddr(addr))
		} else {
			b.WriteString("h3=0;")
		}
	} else {
		// h2c only takes effect on a plaintext listener (bind enables it solely
		// on the non-TLS path), so it is part of the fingerprint only when the
		// listener is not TLS.
		fmt.Fprintf(&b, "h2c=%t;", s.h2cEnabledForAddr(addr))
	}
	return b.String()
}

// mtlsConfigFingerprint serializes the mutual-TLS configuration that
// clientAuthForAddr would resolve for addr, without performing the file I/O that
// builds the actual CA pool: it mirrors that helper's aggregation across every
// TLS-enabled block on addr — the strongest mode, and the order-insensitive
// union of CA files, SAN allow-list entries, and CRL files. A change to any of
// these means the listener's tls.Config.ClientAuth/ClientCAs/VerifyPeerCertificate
// would differ, which only takes effect on a rebind. Like acmeFingerprint, it
// compares configured paths rather than file contents.
func mtlsConfigFingerprint(servers []config.ServerConfig, addr string) string {
	strongest := 0
	var caFiles, sans, crlFiles []string
	active := false
	for i := range servers {
		srv := &servers[i]
		if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled {
			continue
		}
		ca := srv.TLS.ClientAuth
		if !ca.Active() {
			continue
		}
		active = true
		if r := mtlsModeRank(ca.Mode); r > strongest {
			strongest = r
		}
		if f := strings.TrimSpace(ca.CAFile); f != "" {
			caFiles = append(caFiles, f)
		}
		for _, name := range ca.VerifySAN {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				sans = append(sans, name)
			}
		}
		if f := strings.TrimSpace(ca.CRLFile); f != "" {
			crlFiles = append(crlFiles, f)
		}
	}
	if !active {
		return "off"
	}
	sort.Strings(caFiles)
	sort.Strings(sans)
	sort.Strings(crlFiles)
	// Include file content hashes so that same-path CA/CRL rotation (rotating
	// the file contents without changing the configured path) is detected.
	// Without content hashing, rotating a CRL in place would not trigger a
	// restart check, leaving the old trust material active indefinitely.
	caHashes := make([]string, len(caFiles))
	for i, f := range caFiles {
		caHashes[i] = f + ":" + hashFileContent(f)
	}
	crlHashes := make([]string, len(crlFiles))
	for i, f := range crlFiles {
		crlHashes[i] = f + ":" + hashFileContent(f)
	}
	return fmt.Sprintf("mode=%d,ca=%s,san=%s,crl=%s", strongest,
		strings.Join(caHashes, "+"), strings.Join(sans, "+"), strings.Join(crlHashes, "+"))
}

// hashFileContent returns a hex-encoded full SHA-256 digest of the file
// contents for use in fingerprints (R9-12). Returns an error marker when the
// file cannot be read so a non-readable file looks different from any readable
// one.
func hashFileContent(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		// Unreadable: return a marker that changes if the path changes, so a
		// later successful read of the same path still looks different.
		return "err:" + path
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SingleCertFingerprint digests one cert/key pair's file content, the same
// way tlsIdentityFingerprint digests a listener's full binding set, so an
// in-place same-path rotation is detected. Exported for the admin listener
// (#336), which serves one certificate rather than a per-vhost SNI mapping.
func SingleCertFingerprint(cert, key string) string {
	return "file:cert=" + hashFileContent(cert) + ";key=" + hashFileContent(key)
}

// tlsIdentityFingerprint captures the certificate identity used by a TLS
// listener at bind time. For file-backed certificates it digests the cert and
// key bytes together with the SNI name set, so rotating either file in place
// changes the fingerprint. For ACME-backed listeners it captures the ACME
// domain set and issuer parameters. This makes the bound fingerprint sensitive
// to certificate rotations that would otherwise be invisible to a config-only
// comparison (R9-05).
func tlsIdentityFingerprint(bindings []certBinding) string {
	if len(bindings) == 0 {
		return "none"
	}

	// ACME identity is shared across all blocks on the address; report it once.
	if a := bindings[0].tls.ACME; a != nil && a.Enabled {
		domains := append([]string(nil), a.Domains...)
		sort.Strings(domains)
		return fmt.Sprintf("acme:%s:%s:%s:%s", strings.Join(domains, ","), a.Email, a.CA, a.Challenge)
	}

	// File-backed: one entry per (cert,key,names) binding, sorted for stability.
	entries := make([]string, 0, len(bindings))
	for _, b := range bindings {
		names := append([]string(nil), b.names...)
		for i := range names {
			names[i] = strings.ToLower(strings.TrimSpace(names[i]))
		}
		sort.Strings(names)
		certHash := hashFileContent(b.tls.Cert)
		keyHash := hashFileContent(b.tls.Key)
		entries = append(entries, fmt.Sprintf("names=%s;cert=%s;key=%s", strings.Join(names, ","), certHash, keyHash))
	}
	sort.Strings(entries)
	return "file:" + strings.Join(entries, "|")
}

// mtlsModeRank ranks client-auth modes the way clientAuthMode + authStrength do,
// so the fingerprint picks the strongest mode across blocks sharing an address.
func mtlsModeRank(mode string) int {
	switch strings.TrimSpace(mode) {
	case "require":
		return 2
	case "request":
		return 1
	default:
		return 0
	}
}

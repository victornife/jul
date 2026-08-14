// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package backendtls resolves the public backend_tls configuration into one
// immutable policy that every outbound data-plane consumer shares: the HTTP
// reverse proxy, native gRPC passthrough, gRPC-JSON transcoding and active
// health checks.
//
// The separation is deliberate (ADR 0016). Transports never parse public
// configuration — they accept only a *Policy — which is what keeps a future
// named-profile feature a change to resolution rather than a transport rewrite,
// and what stops each protocol from inventing its own trust model.
//
// The package imports only the standard library, so it can be used from config
// validation and from every transport without an import cycle.
package backendtls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
)

// CA trust-root modes. The mode is always explicit and is never inferred from
// the presence of ca_file: inference is unrevertable, because changing
// augment-versus-replace semantics later would silently change which backends
// verify, with no error anywhere.
const (
	CAModeSystem        = "system"
	CAModeSystemAndFile = "system_and_file"
	CAModeFileOnly      = "file_only"
)

// Supported minimum TLS versions. 1.2 is the default, matching Go's client
// default and the transcoder's current behaviour; defaulting to 1.3 would
// silently break reachable backends.
const (
	MinVersion12 = "1.2"
	MinVersion13 = "1.3"
)

// PeerIdentityErrorFragment is the fixed sentence a peer-identity failure
// carries. Consumers match on it to classify a handshake failure into a bounded
// category without parsing anything operator- or peer-controlled.
const PeerIdentityErrorFragment = "matches none of the"

// Peer-identity prefixes. Entries are prefixed from the first release so future
// identity types are purely additive rather than ambiguous forever.
const (
	IdentityDNS = "dns:"
	IdentityURI = "uri:"
)

// CAModes and MinVersions return the accepted values, for validation messages
// and generated metadata.
func CAModes() []string { return []string{CAModeSystem, CAModeSystemAndFile, CAModeFileOnly} }

func MinVersions() []string { return []string{MinVersion12, MinVersion13} }

// Options is the public backend_tls block in the shape this package consumes.
// config.BackendTLSConfig converts to it, so this package never imports config.
type Options struct {
	CAFile             string
	CAMode             string
	ClientCert         string
	ClientKey          string
	ServerName         string
	MinVersion         string
	PeerIdentities     []string
	InsecureSkipVerify bool
}

// IsZero reports whether the block carries no setting at all.
func (o Options) IsZero() bool {
	return o.CAFile == "" && o.CAMode == "" && o.ClientCert == "" && o.ClientKey == "" &&
		o.ServerName == "" && o.MinVersion == "" && len(o.PeerIdentities) == 0 && !o.InsecureSkipVerify
}

// Identity is one parsed peer identity. Identities are matched after standard
// certificate verification, never instead of it, and never by regex or
// substring.
type Identity struct {
	Kind  string // "dns" or "uri"
	Value string
}

func (i Identity) String() string { return i.Kind + ":" + i.Value }

// Policy is an immutable resolved backend TLS policy. It is safe to share
// between consumers and across goroutines: every accessor returns a copy or a
// freshly built value, so no consumer can mutate what another one reads.
type Policy struct {
	rootCAs      *x509.CertPool
	clientCert   *tls.Certificate
	serverName   string
	minVersion   uint16
	peers        []Identity
	insecure     bool
	fingerprint  string
	certSubject  string
	certNotAfter string
	caMode       string
}

// Resolve builds a policy from opts for a backend whose configured logical
// identity is logicalHost (the upstream name, or the host of a literal target).
//
// It performs no network I/O and holds no secret longer than it must: the
// client key is parsed into a tls.Certificate and the file contents are
// dropped. It is called during reload preparation, so unreadable or malformed
// material aborts the reload before anything is published.
//
// A zero Options yields a nil policy, meaning "language defaults" — which is
// what a plaintext backend, or an HTTPS backend with no explicit policy, uses.
func Resolve(opts Options, logicalHost string) (*Policy, error) {
	if opts.IsZero() {
		return nil, nil
	}
	if errs := Validate(opts); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	p := &Policy{
		insecure:   opts.InsecureSkipVerify,
		serverName: strings.TrimSpace(opts.ServerName),
		caMode:     caMode(opts),
	}
	if p.serverName == "" {
		// A discovery-returned or load-balanced address is a dial destination
		// only; the configured logical name stays the verified identity, so a
		// compromised registry cannot rewrite both the address and the name it
		// is checked against.
		p.serverName = defaultServerName(logicalHost)
	}
	p.minVersion = tls.VersionTLS12
	if strings.TrimSpace(opts.MinVersion) == MinVersion13 {
		p.minVersion = tls.VersionTLS13
	}

	material := map[string]string{}
	switch p.caMode {
	case CAModeSystem:
		// nil RootCAs means the platform pool, which Go loads lazily.
	case CAModeSystemAndFile, CAModeFileOnly:
		pool, digest, err := loadCAPool(opts.CAFile, p.caMode == CAModeSystemAndFile)
		if err != nil {
			return nil, err
		}
		p.rootCAs = pool
		material["ca"] = digest
	}

	if opts.ClientCert != "" {
		cert, digest, err := loadClientCertificate(opts.ClientCert, opts.ClientKey)
		if err != nil {
			return nil, err
		}
		p.clientCert = cert
		material["client"] = digest
		p.certSubject, p.certNotAfter = certificateMetadata(cert)
	}

	for _, raw := range opts.PeerIdentities {
		id, err := ParseIdentity(raw)
		if err != nil {
			return nil, err
		}
		p.peers = append(p.peers, id)
	}
	p.peers = dedupeIdentities(p.peers)
	p.fingerprint = computeFingerprint(p, material)
	return p, nil
}

// ClientConfig returns a fresh *tls.Config for one consumer. Every call builds
// a new value, so a transport that mutates its copy — setting NextProtos for
// HTTP/2, for example — cannot affect any other consumer of the same policy.
//
// Peer-identity checking is installed through VerifyConnection, which runs
// *after* Go's standard chain and hostname verification rather than replacing
// it. With insecure_skip_verify the standard checks are disabled, and a peer
// identity cannot be configured alongside it (validation rejects the
// combination), so there is no path on which an identity check silently
// substitutes for verification.
func (p *Policy) ClientConfig() *tls.Config {
	if p == nil {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg := &tls.Config{
		MinVersion:         p.minVersion,
		ServerName:         p.serverName,
		RootCAs:            p.rootCAs,
		InsecureSkipVerify: p.insecure, //nolint:gosec // opt-in, lint-error-gated, and rejected with peer identities or a non-system ca_mode
	}
	if p.clientCert != nil {
		cert := *p.clientCert
		cfg.Certificates = []tls.Certificate{cert}
	}
	if len(p.peers) > 0 {
		peers := append([]Identity(nil), p.peers...)
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			return verifyPeerIdentity(cs, peers)
		}
	}
	return cfg
}

// ServerName returns the identity the peer is verified against.
func (p *Policy) ServerName() string {
	if p == nil {
		return ""
	}
	return p.serverName
}

// InsecureSkipVerify reports whether verification is bypassed. Consumers use it
// to warn; nothing may use it to widen behaviour.
func (p *Policy) InsecureSkipVerify() bool { return p != nil && p.insecure }

// Fingerprint is a stable, secret-free identity for the resolved policy,
// including a digest of the CA and client-certificate *contents*. Two policies
// with the same fingerprint are interchangeable, so a transport generation can
// be keyed by it — and rotating a file in place changes it even when the
// configured paths do not.
func (p *Policy) Fingerprint() string {
	if p == nil {
		return ""
	}
	return p.fingerprint
}

// Metadata is the status-safe projection of a policy. It carries no key
// material, no CA contents and no file path.
type Metadata struct {
	CAMode             string   `json:"ca_mode"`
	ServerName         string   `json:"server_name,omitempty"`
	MinVersion         string   `json:"min_version"`
	PeerIdentities     []string `json:"peer_identities,omitempty"`
	ClientCertSubject  string   `json:"client_cert_subject,omitempty"`
	ClientCertNotAfter string   `json:"client_cert_not_after,omitempty"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify"`
	Fingerprint        string   `json:"fingerprint"`
}

// Metadata returns the projection. The private key is never represented, not
// even as a digest that could confirm a guess.
func (p *Policy) Metadata() Metadata {
	if p == nil {
		return Metadata{CAMode: CAModeSystem, MinVersion: MinVersion12}
	}
	out := Metadata{
		CAMode:             p.caMode,
		ServerName:         p.serverName,
		MinVersion:         MinVersion12,
		ClientCertSubject:  p.certSubject,
		ClientCertNotAfter: p.certNotAfter,
		InsecureSkipVerify: p.insecure,
		Fingerprint:        p.fingerprint,
	}
	if p.minVersion == tls.VersionTLS13 {
		out.MinVersion = MinVersion13
	}
	for _, id := range p.peers {
		out.PeerIdentities = append(out.PeerIdentities, id.String())
	}
	return out
}

// caMode returns the effective mode.
func caMode(opts Options) string {
	if m := strings.TrimSpace(opts.CAMode); m != "" {
		return m
	}
	return CAModeSystem
}

// defaultServerName derives the verified identity from the configured logical
// target when server_name is absent. A host:port target contributes its host.
//
// An IP literal is returned as-is: Go suppresses SNI for one (a literal is not
// a legal SNI host) and verifies it against the certificate's IP SANs instead,
// which is the intended behaviour for a directly addressed backend.
func defaultServerName(logicalHost string) string {
	host := strings.TrimSpace(logicalHost)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// loadCAPool reads a PEM bundle and returns the pool plus a content digest.
func loadCAPool(path string, withSystem bool) (*x509.CertPool, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("backend_tls: ca_file %q is not readable: %w", path, err)
	}
	var pool *x509.CertPool
	if withSystem {
		pool, err = x509.SystemCertPool()
		if err != nil {
			// Truthful rather than silent: a platform whose roots cannot be
			// loaded must not quietly degrade to the configured file alone.
			return nil, "", fmt.Errorf("backend_tls: ca_mode %q needs the system roots, which could not be loaded: %w", CAModeSystemAndFile, err)
		}
	} else {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, "", fmt.Errorf("backend_tls: ca_file %q contains no usable PEM certificate", path)
	}
	return pool, digest(data), nil
}

// loadClientCertificate parses the certificate/key pair. A mismatched pair is
// rejected here rather than at the first handshake.
func loadClientCertificate(certPath, keyPath string) (*tls.Certificate, string, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, "", fmt.Errorf("backend_tls: client_cert %q is not readable: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("backend_tls: client_key %q is not readable: %w", keyPath, err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, "", fmt.Errorf("backend_tls: client_cert and client_key do not form a usable pair: %w", err)
	}
	// The key digest covers the key bytes so an in-place rotation is detected,
	// but the bytes themselves are dropped with this function's frame.
	return &cert, digest(append(append([]byte(nil), certPEM...), keyPEM...)), nil
}

// certificateMetadata extracts the safe leaf fields for status projection.
func certificateMetadata(cert *tls.Certificate) (subject, notAfter string) {
	if cert == nil || len(cert.Certificate) == 0 {
		return "", ""
	}
	leaf := cert.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return "", ""
		}
		leaf = parsed
	}
	return leaf.Subject.String(), leaf.NotAfter.UTC().Format("2006-01-02T15:04:05Z")
}

// ParseIdentity parses one peer_identities entry.
func ParseIdentity(raw string) (Identity, error) {
	entry := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(entry, IdentityDNS):
		value := strings.TrimSpace(entry[len(IdentityDNS):])
		if value == "" {
			return Identity{}, fmt.Errorf("peer identity %q has an empty DNS name", raw)
		}
		if strings.ContainsAny(value, " \t/:") {
			return Identity{}, fmt.Errorf("peer identity %q is not a DNS name", raw)
		}
		return Identity{Kind: "dns", Value: strings.ToLower(strings.TrimSuffix(value, "."))}, nil
	case strings.HasPrefix(entry, IdentityURI):
		value := strings.TrimSpace(entry[len(IdentityURI):])
		if value == "" {
			return Identity{}, fmt.Errorf("peer identity %q has an empty URI", raw)
		}
		if _, err := url.Parse(value); err != nil {
			return Identity{}, fmt.Errorf("peer identity %q is not a URI: %w", raw, err)
		}
		return Identity{Kind: "uri", Value: value}, nil
	case entry == "":
		return Identity{}, errors.New("peer identity is empty")
	default:
		return Identity{}, fmt.Errorf("peer identity %q must start with %q or %q", raw, IdentityDNS, IdentityURI)
	}
}

// verifyPeerIdentity checks the verified leaf against the configured identities.
// Identities are ORed. DNS matching uses the certificate's own DNS names with
// the standard wildcard rule; no custom wildcard grammar is invented, and no
// substring or regex matching happens anywhere.
func verifyPeerIdentity(cs tls.ConnectionState, peers []Identity) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("backend_tls: peer presented no certificate")
	}
	leaf := cs.PeerCertificates[0]
	for _, want := range peers {
		switch want.Kind {
		case "dns":
			if leaf.VerifyHostname(want.Value) == nil {
				return nil
			}
		case "uri":
			for _, u := range leaf.URIs {
				if u.String() == want.Value {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("backend_tls: peer certificate %s %d configured peer identities", PeerIdentityErrorFragment, len(peers))
}

// dedupeIdentities removes duplicates after normalization, keeping order.
func dedupeIdentities(in []Identity) []Identity {
	if len(in) < 2 {
		return in
	}
	seen := make(map[Identity]struct{}, len(in))
	out := in[:0]
	for _, id := range in {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// computeFingerprint hashes the resolved policy plus the digests of the trust
// material it loaded.
func computeFingerprint(p *Policy, material map[string]string) string {
	keys := make([]string, 0, len(material))
	for k := range material {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	fmt.Fprintf(&b, "ca_mode=%s;server_name=%s;min_version=%d;insecure=%t;", p.caMode, p.serverName, p.minVersion, p.insecure)
	for _, id := range p.peers {
		b.WriteString("peer=" + id.String() + ";")
	}
	for _, k := range keys {
		b.WriteString(k + "=" + material[k] + ";")
	}
	return digest([]byte(b.String()))
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

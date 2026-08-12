// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"
)

// extractors maps every registry path to a function returning its effective
// value in a comparison-stable form. The map is built from the registry itself,
// so a new entry gets a schema-derived extractor automatically and a path only
// has to declare a special extractor when it needs different comparison
// semantics.
var extractors map[string]func(*config.Config) any

func init() {
	extractors = make(map[string]func(*config.Config) any, len(Registry))
	for _, e := range Registry {
		path := e.Path
		fn, ok := specialExtractor(path)
		if !ok {
			fn = func(cfg *config.Config) any { return extractGeneric(cfg, path) }
		}
		if e.Secret {
			inner := fn
			fn = func(cfg *config.Config) any { return digestValues(inner(cfg)) }
		}
		extractors[path] = fn
	}
}

// EffectiveValue returns the comparison-stable effective value of a registry
// path from an already-resolved configuration. Secret-bearing paths yield
// digests, never the configured value, so the result is safe to compare and to
// store in a fingerprint.
//
// The second result is false when the path has no disposition.
func EffectiveValue(cfg *config.Config, path string) (any, bool) {
	fn, ok := extractors[path]
	if !ok {
		return nil, false
	}
	return fn(cfg), true
}

// extractGeneric navigates the resolved configuration along a dotted registry
// path using the TOML-tagged schema. A wildcard segment expands the matching
// slice or map into a map keyed by the element's operational identity, so two
// configurations that list the same elements in a different order compare equal
// wherever order carries no runtime meaning.
func extractGeneric(cfg *config.Config, path string) any {
	return walkPath(reflect.ValueOf(cfg), strings.Split(path, "."))
}

func walkPath(v reflect.Value, parts []string) any {
	if len(parts) == 0 {
		return normalizeValue(v)
	}

	part := parts[0]
	rest := parts[1:]

	if part == "*" {
		v = deref(v)
		if !v.IsValid() {
			return nil
		}
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			out := make(map[string]any, v.Len())
			for i := 0; i < v.Len(); i++ {
				elem := v.Index(i)
				out[collectionKey(elem, i)] = walkPath(elem, rest)
			}
			return out
		case reflect.Map:
			out := make(map[string]any, v.Len())
			for _, k := range v.MapKeys() {
				out[fmt.Sprint(k.Interface())] = walkPath(v.MapIndex(k), rest)
			}
			return out
		}
		return nil
	}

	v = deref(v)
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := fieldByTOML(v, part)
	if !f.IsValid() {
		return nil
	}
	return walkPath(f, rest)
}

// normalizeValue converts a reflect.Value into a deep-equal-stable form:
// strings, ints, floats, bools, []any and map[string]any.
func normalizeValue(v reflect.Value) any {
	v = deref(v)
	if !v.IsValid() {
		return nil
	}
	if v.Type() == timeType {
		return v.Interface().(time.Time).UTC().Format(time.RFC3339Nano)
	}

	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(v.Uint())
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Bool:
		return v.Bool()
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = normalizeValue(v.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for _, k := range v.MapKeys() {
			out[fmt.Sprint(k.Interface())] = normalizeValue(v.MapIndex(k))
		}
		return out
	case reflect.Struct:
		out := make(map[string]any)
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("toml")
			if tag == "" || tag == "-" {
				continue
			}
			out[strings.Split(tag, ",")[0]] = normalizeValue(v.Field(i))
		}
		return out
	default:
		return fmt.Sprint(v.Interface())
	}
}

var timeType = reflect.TypeOf(time.Time{})

// deref follows pointers, returning an invalid value when a nil pointer is hit
// so an omitted optional block stays distinguishable from an empty one.
func deref(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func fieldByTOML(v reflect.Value, name string) reflect.Value {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		if strings.Split(tag, ",")[0] == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}

// collectionKey returns the identity of a collection element. Collections whose
// order carries no runtime meaning are keyed by the identity the operational
// code addresses them with, so reordering the document produces no diff.
// Collections where order is part of the behavior — rewrite rules are evaluated
// top to bottom — are keyed by index so a reorder is reported.
func collectionKey(elem reflect.Value, index int) string {
	elem = deref(elem)
	if !elem.IsValid() {
		return strconv.Itoa(index)
	}

	switch elem.Type() {
	case reflect.TypeOf(config.ServerConfig{}):
		return serverKey(elem.Addr().Interface().(*config.ServerConfig))
	case reflect.TypeOf(config.LocationConfig{}):
		return locationKey(elem.Addr().Interface().(*config.LocationConfig))
	case reflect.TypeOf(config.UpstreamConfig{}), reflect.TypeOf(config.AdminPrincipal{}), reflect.TypeOf(config.AdminRole{}):
		return elem.FieldByName("Name").String()
	case reflect.TypeOf(config.UpstreamServer{}):
		return elem.FieldByName("Address").String()
	case reflect.TypeOf(config.StreamServer{}):
		return streamKey(elem.Addr().Interface().(*config.StreamServer))
	default:
		return strconv.Itoa(index)
	}
}

// serverKey is the operational identity of a server block: its listen address
// plus its order-insensitive host-name set. Two documents that list the same
// blocks in a different order therefore compare equal.
func serverKey(s *config.ServerConfig) string {
	return sniKey(s.ServerNames) + "@" + s.Listen
}

// streamKey is the operational identity of an L4 listener, matching the
// "proto|addr" key the stream server binds with.
func streamKey(s *config.StreamServer) string {
	return normalizeStreamProtocol(s.Protocol) + "|" + strings.TrimSpace(s.Listen)
}

// locationKey is the operational identity of a location: the match type and
// path the router dispatches on.
func locationKey(l *config.LocationConfig) string {
	t := l.Match.Type
	if t == "" {
		t = "prefix"
	}
	return t + " " + l.Match.Path
}

// sniKey is the order-insensitive rendering of a server block's host names,
// used both as the TLS certificate-selection identity and as part of serverKey.
func sniKey(names []string) string {
	if len(names) == 0 {
		return "_default_"
	}
	return strings.Join(sortedCopy(names), ",")
}

func sortedCopy(xs []string) []string {
	cp := append([]string(nil), xs...)
	sort.Strings(cp)
	return cp
}

// digestValues replaces every string in a normalized value with its digest so a
// secret-bearing path can be compared without the value leaving the process.
func digestValues(v any) any {
	switch t := v.(type) {
	case string:
		return digestString(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = digestValues(t[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = digestValues(vv)
		}
		return out
	default:
		return v
	}
}

// perListener groups a per-server-block value by listen address and then by the
// block's host-name set, mirroring how the runtime binds one TLS/HTTP-3 listener
// per address and selects certificates by SNI. Address keying lets
// DiffAddressAware ignore addresses that were added or removed rather than
// edited.
func perListener(cfg *config.Config, fn func(*config.ServerConfig) any) any {
	byAddr := map[string]any{}
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		hosts, ok := byAddr[s.Listen].(map[string]any)
		if !ok {
			hosts = map[string]any{}
			byAddr[s.Listen] = hosts
		}
		hosts[sniKey(s.ServerNames)] = fn(s)
	}
	return byAddr
}

// locationBackendTLS groups a location's backend_tls field by listen address,
// then by virtual host, then by the location's match path. The address keying
// is what lets DiffAddressAware ignore listeners the reload added or removed
// rather than reporting them as a restart for an untouched address.
func locationBackendTLS(fn func(*config.BackendTLSConfig) any) func(*config.Config) any {
	return func(cfg *config.Config) any {
		return perListener(cfg, func(s *config.ServerConfig) any {
			out := make(map[string]any, len(s.Locations))
			for i := range s.Locations {
				loc := &s.Locations[i]
				if loc.BackendTLS == nil {
					continue
				}
				out[loc.Match.Type+" "+loc.Match.Path] = fn(loc.BackendTLS)
			}
			return out
		})
	}
}

// perServer groups a per-block value by the block's operational identity.
func perServer(cfg *config.Config, fn func(*config.ServerConfig) any) any {
	out := make(map[string]any, len(cfg.Servers))
	for i := range cfg.Servers {
		s := &cfg.Servers[i]
		out[serverKey(s)] = fn(s)
	}
	return out
}

func tlsValue(fn func(*config.TLSConfig) any) func(*config.Config) any {
	return func(cfg *config.Config) any {
		return perListener(cfg, func(s *config.ServerConfig) any {
			if s.TLS == nil {
				return nil
			}
			return fn(s.TLS)
		})
	}
}

func clientAuthValue(fn func(*config.ClientAuthConfig) any) func(*config.Config) any {
	return tlsValue(func(t *config.TLSConfig) any {
		if t.ClientAuth == nil {
			return nil
		}
		return fn(t.ClientAuth)
	})
}

func acmeValue(fn func(*config.ACMEConfig) any) func(*config.Config) any {
	return tlsValue(func(t *config.TLSConfig) any {
		if t.ACME == nil {
			return nil
		}
		return fn(t.ACME)
	})
}

// specialExtractor returns the extractor for paths whose comparison shape is not
// the plain schema walk: listener-scoped values, file-content digests, pointer
// defaults resolved to their effective value, and lists whose order carries no
// runtime meaning.
func specialExtractor(path string) (func(*config.Config) any, bool) {
	switch path {

	// ── Listener-bound TLS material ────────────────────────────────────────
	case "servers.*.tls.enabled":
		return tlsValue(func(t *config.TLSConfig) any { return t.Enabled }), true
	case "servers.*.tls.min_version":
		return tlsValue(func(t *config.TLSConfig) any { return t.MinVersion }), true
	case "servers.*.tls.cert":
		return tlsValue(func(t *config.TLSConfig) any { return digestTLSMaterial(t.Cert) }), true
	case "servers.*.tls.key":
		return tlsValue(func(t *config.TLSConfig) any { return digestTLSMaterial(t.Key) }), true
	case "servers.*.tls.client_auth.mode":
		return clientAuthValue(func(c *config.ClientAuthConfig) any { return c.Mode }), true
	case "servers.*.tls.client_auth.ca_file":
		return clientAuthValue(func(c *config.ClientAuthConfig) any { return digestTLSMaterial(c.CAFile) }), true
	case "servers.*.tls.client_auth.crl_file":
		return clientAuthValue(func(c *config.ClientAuthConfig) any { return digestTLSMaterial(c.CRLFile) }), true
	case "servers.*.tls.client_auth.verify_san":
		return clientAuthValue(func(c *config.ClientAuthConfig) any { return sortedCopy(c.VerifySAN) }), true

	// ── ACME ───────────────────────────────────────────────────────────────
	case "servers.*.tls.acme.enabled":
		return acmeValue(func(a *config.ACMEConfig) any { return a.Enabled }), true
	case "servers.*.tls.acme.email":
		return acmeValue(func(a *config.ACMEConfig) any { return a.Email }), true
	case "servers.*.tls.acme.ca":
		return acmeValue(func(a *config.ACMEConfig) any { return a.CA }), true
	case "servers.*.tls.acme.domains":
		return acmeValue(func(a *config.ACMEConfig) any { return sortedCopy(a.Domains) }), true
	case "servers.*.tls.acme.challenge":
		return acmeValue(func(a *config.ACMEConfig) any { return a.Challenge }), true
	case "servers.*.tls.acme.cache_dir":
		return acmeValue(func(a *config.ACMEConfig) any { return a.CacheDir }), true
	case "servers.*.tls.acme.ocsp_stapling":
		return acmeValue(func(a *config.ACMEConfig) any { return a.OCSPStaplingEnabled() }), true
	case "servers.*.tls.acme.dns_provider":
		// Validation rejects a non-empty value, so nothing can ever be consumed.
		// The extractor reports only whether any block configures one, which is
		// what preview needs to explain the rejection — and which keeps adding or
		// removing an unrelated listener from looking like a rejected change.
		return func(cfg *config.Config) any {
			for i := range cfg.Servers {
				s := &cfg.Servers[i]
				if s.TLS != nil && s.TLS.ACME != nil && strings.TrimSpace(s.TLS.ACME.DNSProvider) != "" {
					return true
				}
			}
			return false
		}, true
	case "servers.*.locations.*.rate_limit.max_conns":
		// Validation rejects a location-scoped connection cap. Presence is the
		// only signal preview needs, and it stays stable when locations are added.
		return func(cfg *config.Config) any {
			for i := range cfg.Servers {
				for j := range cfg.Servers[i].Locations {
					if rl := cfg.Servers[i].Locations[j].RateLimit; rl != nil && rl.MaxConns != 0 {
						return true
					}
				}
			}
			return false
		}, true

	// ── Other listener-bound settings ──────────────────────────────────────
	case "servers.*.h2c":
		return func(cfg *config.Config) any {
			return perListener(cfg, func(s *config.ServerConfig) any { return s.H2C })
		}, true
	case "stream.*.protocol":
		// An omitted protocol means "tcp"; comparing the raw value would report
		// a spurious change when a document spells the default out.
		return func(cfg *config.Config) any {
			out := make(map[string]any, len(cfg.Streams))
			for i := range cfg.Streams {
				s := &cfg.Streams[i]
				out[streamKey(s)] = normalizeStreamProtocol(s.Protocol)
			}
			return out
		}, true
	case "servers.*.http3.enabled":
		return func(cfg *config.Config) any {
			return perListener(cfg, func(s *config.ServerConfig) any { return s.HTTP3 != nil && s.HTTP3.Enabled })
		}, true
	case "servers.*.http3.alt_svc_max_age":
		return func(cfg *config.Config) any {
			return perListener(cfg, func(s *config.ServerConfig) any {
				if s.HTTP3 == nil {
					return 0
				}
				return s.HTTP3.AltSvcMaxAge
			})
		}, true

	// ── Effective values behind optional pointers ──────────────────────────
	case "global.worker_threads":
		return func(cfg *config.Config) any { return effectiveWorkerThreads(cfg.Global.WorkerThreads) }, true
	case "observability.access_log.enabled":
		return func(cfg *config.Config) any { return cfg.Observability.AccessLog.IsEnabled() }, true
	case "compression.enabled":
		return func(cfg *config.Config) any { return cfg.Compression.IsEnabled() }, true
	case "admin.console":
		return func(cfg *config.Config) any { return cfg.Admin.ConsoleEnabled() }, true

	// ── Order-insensitive lists ────────────────────────────────────────────
	case "observability.access_log.sinks":
		return func(cfg *config.Config) any { return sortedCopy(cfg.Observability.AccessLog.Sinks) }, true
	case "egress.allow":
		return func(cfg *config.Config) any { return sortedCopy(cfg.Egress.Allow) }, true
	case "compression.types":
		return func(cfg *config.Config) any { return sortedCopy(cfg.Compression.Types) }, true
	case "servers.*.server_names":
		return func(cfg *config.Config) any {
			return perServer(cfg, func(s *config.ServerConfig) any { return sortedCopy(s.ServerNames) })
		}, true
	case "servers.*.plugins":
		return func(cfg *config.Config) any {
			return perServer(cfg, func(s *config.ServerConfig) any { return sortedCopy(s.Plugins) })
		}, true
	case "servers.*.locations.*.backend_tls.ca_file":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return digestTLSMaterial(b.CAFile) }), true
	case "servers.*.locations.*.backend_tls.ca_mode":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return b.CAMode }), true
	case "servers.*.locations.*.backend_tls.client_cert":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return digestTLSMaterial(b.ClientCert) }), true
	case "servers.*.locations.*.backend_tls.client_key":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return digestTLSMaterial(b.ClientKey) }), true
	case "servers.*.locations.*.backend_tls.insecure_skip_verify":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return b.InsecureSkipVerify }), true
	case "servers.*.locations.*.backend_tls.min_version":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return b.MinVersion }), true
	case "servers.*.locations.*.backend_tls.peer_identities":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return sortedCopy(b.PeerIdentities) }), true
	case "servers.*.locations.*.backend_tls.server_name":
		return locationBackendTLS(func(b *config.BackendTLSConfig) any { return b.ServerName }), true
	case "servers.*.locations.*.plugins":
		return func(cfg *config.Config) any {
			return perServer(cfg, func(s *config.ServerConfig) any {
				locs := make(map[string]any, len(s.Locations))
				for j := range s.Locations {
					l := &s.Locations[j]
					locs[locationKey(l)] = sortedCopy(l.Plugins)
				}
				return locs
			})
		}, true
	}
	return nil, false
}

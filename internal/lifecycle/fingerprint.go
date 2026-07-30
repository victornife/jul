// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"jul/internal/config"
)

var initialGOMAXPROCS = runtime.GOMAXPROCS(0)

// InitialGOMAXPROCS returns the GOMAXPROCS value in effect before the server
// applied any worker_threads cap. It is used to resolve "auto" back to the
// original container-aware default.
func InitialGOMAXPROCS() int { return initialGOMAXPROCS }

// Fingerprint captures the effective values of startup-consumed configuration
// fields. It is computed from the expanded configuration (secret references
// resolved) and stored on the live server after initial startup. Any subsequent
// reload whose candidate fingerprint differs must be rejected as restart-required.
type Fingerprint struct {
	// Values maps a registered startup-consumed path to its effective value or
	// digest. Paths use the same dot notation as the lifecycle registry.
	Values map[string]any `json:"values"`
}

// EmptyFingerprint returns a fingerprint with no values.
func EmptyFingerprint() Fingerprint {
	return Fingerprint{Values: map[string]any{}}
}

// ComputeFingerprint builds a fingerprint from the expanded effective config.
// It uses file-content digests for file-backed values so rotating the contents
// of an unchanged secret reference is detected.
func ComputeFingerprint(cfg *config.Config) Fingerprint {
	fp := EmptyFingerprint()
	for _, e := range StartupFields() {
		v := extractValue(cfg, e.Path)
		fp.Values[e.Path] = v
	}
	return fp
}

// Diff returns the list of paths that differ between two fingerprints.
func Diff(a, b Fingerprint) []string {
	var out []string
	for _, e := range StartupFields() {
		av, ok1 := a.Values[e.Path]
		bv, ok2 := b.Values[e.Path]
		if !ok1 || !ok2 {
			out = append(out, e.Path)
			continue
		}
		if !deepEqualValues(av, bv) {
			out = append(out, e.Path)
		}
	}
	return out
}

// extractValue returns the effective value for a registry path. For file-backed
// values it returns a digest so content rotation is detected.
func extractValue(cfg *config.Config, path string) any {
	switch path {
	case "global.worker_threads":
		return effectiveWorkerThreads(cfg.Global.WorkerThreads)
	case "global.access_log":
		return cfg.Global.AccessLog
	case "global.error_log":
		return cfg.Global.ErrorLog
	case "global.log_format":
		return cfg.Global.LogFormat
	case "global.redact_min_secret_length":
		return cfg.Global.RedactMinSecretLength
	case "admin.enabled":
		return cfg.Admin.Enabled
	case "admin.listen":
		return cfg.Admin.Listen
	case "admin.token":
		return digestString(cfg.Admin.Token)
	case "admin.console":
		if cfg.Admin.Console == nil {
			return nil
		}
		return *cfg.Admin.Console
	case "admin.history_dir":
		return cfg.Admin.HistoryDir
	case "admin.history_keep":
		return cfg.Admin.HistoryKeep
	case "admin.rate_limit_read_per_min":
		return cfg.Admin.RateLimitReadPerMin
	case "admin.rate_limit_write_per_min":
		return cfg.Admin.RateLimitWritePerMin
	case "admin.rate_limit_apply_per_min":
		return cfg.Admin.RateLimitApplyPerMin
	case "admin.max_event_conns":
		return cfg.Admin.MaxEventConns
	case "admin.audit_log_file":
		return cfg.Admin.AuditLogFile
	case "admin.audit_log_rotate_max_mb":
		return cfg.Admin.AuditLogRotateMaxMB
	case "admin.audit_log_rotate_keep":
		return cfg.Admin.AuditLogRotateKeep
	case "admin.plugin_upload_dir":
		return cfg.Admin.PluginUploadDir
	case "admin.plugin_upload_max_size":
		return cfg.Admin.PluginUploadMaxSize
	case "admin.plugin_upload_enabled":
		if cfg.Admin.PluginUploadEnabled == nil {
			return nil
		}
		return *cfg.Admin.PluginUploadEnabled
	case "admin.rbac.enabled":
		// Enabling or disabling RBAC changes the auth middleware wiring and
		// requires a restart. The policy contents (roles, principals) are
		// hot-swappable and are not fingerprinted here.
		return cfg.Admin.RBAC.Enabled
	case "cache.enabled":
		return cfg.Cache.Enabled
	case "cache.memory_max_size":
		return cfg.Cache.MemoryMaxSize
	case "cache.disk_path":
		// Disk cache path is compared as a path; directory creation must not
		// create false restart signals (R6-03).
		return cfg.Cache.DiskPath
	case "cache.disk_max_size":
		return cfg.Cache.DiskMaxSize
	case "cache.default_ttl":
		return cfg.Cache.DefaultTTL
	case "cache.stale_while_revalidate":
		return cfg.Cache.StaleWhileRevalidate
	case "cache.stale_if_error":
		return cfg.Cache.StaleIfError
	case "observability.metrics.host_label":
		return cfg.Observability.Metrics.HostLabel
	case "observability.tracing.enabled":
		return cfg.Observability.Tracing.Enabled
	case "observability.tracing.endpoint":
		return cfg.Observability.Tracing.Endpoint
	case "observability.tracing.sample_ratio":
		return cfg.Observability.Tracing.SampleRatio
	case "observability.tracing.exporter":
		return cfg.Observability.Tracing.Exporter
	case "observability.tracing.service_name":
		return cfg.Observability.Tracing.ServiceName
	case "observability.tracing.insecure":
		return cfg.Observability.Tracing.Insecure
	case "observability.access_log.sinks":
		return cfg.Observability.AccessLog.Sinks
	case "observability.access_log.file":
		// Access-log file path is compared as a path; log growth must not
		// create false restart signals (R6-03).
		return cfg.Observability.AccessLog.File
	case "observability.access_log.format":
		return cfg.Observability.AccessLog.Format
	case "observability.access_log.rotate_max_mb":
		return cfg.Observability.AccessLog.RotateMaxMB
	case "observability.access_log.rotate_keep":
		return cfg.Observability.AccessLog.RotateKeep
	case "egress.enabled":
		return cfg.Egress.Enabled
	case "egress.allow":
		return cfg.Egress.Allow
	case "servers.*.tls":
		return tlsFingerprint(cfg)
	case "servers.*.http3":
		return http3Fingerprint(cfg)
	case "servers.*.h2c":
		return h2cFingerprint(cfg)
	case "stream.*.protocol":
		return streamProtocolFingerprint(cfg)
	default:
		return nil
	}
}

// effectiveWorkerThreads resolves "auto" and empty to the GOMAXPROCS value
// that was in effect before the server applied any cap (InitialGOMAXPROCS).
// Numeric strings are parsed. This guarantees that switching from a numeric
// cap back to "auto" is detected as a change, and that OnReloaded can restore
// the original container-aware default.
func effectiveWorkerThreads(raw string) any {
	n := parseWorkerThreads(raw)
	if n > 0 {
		return n
	}
	return initialGOMAXPROCS
}

func parseWorkerThreads(raw string) int {
	if raw == "" || strings.EqualFold(raw, "auto") {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// digestString hashes an inline value. It is used for fields whose effective
// value is the secret or configuration text itself (e.g. admin.token, an
// inline PEM block). It never reads the filesystem, so path-like strings are
// compared as paths and growing files cannot create false restart signals
// (R6-03).
func digestString(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// digestFileContent reads path and returns a digest of its bytes. It is used
// for TLS certificate/key/CA/CRL files where the bind-time content matters.
// If the path cannot be read, an error marker is returned; that still signals
// a change from a previously readable file.
func digestFileContent(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("error:%v", err)
	}
	h := sha256.Sum256(data)
	return "file-sha256:" + hex.EncodeToString(h[:])
}

// digestTLSFile canonicalizes a TLS certificate/key/CA/CRL value. After secret
// resolution the value may be inline PEM content, a ${file:...} reference
// resolved to PEM content, or a plain file path. Inline PEM is digested as a
// string; a readable file path is digested by content; an unreadable path is
// digested as a string so misconfiguration is stable.
//
// The path check accepts Unix absolute paths, relative paths (with or without
// a leading "./"), and Windows absolute/UNC paths (C:\..., \\server\share)
// so rotating a certificate on any platform or via a bare filename is detected.
func digestTLSFile(s string) string {
	if s == "" {
		return ""
	}
	// Inline PEM content (or any already-resolved secret value) is digested
	// directly. PEM has a distinctive marker.
	if strings.HasPrefix(s, "-----BEGIN") {
		return digestString(s)
	}
	// Treat every other non-empty value as a candidate file path. This covers
	// relative paths such as "certs/cert.pem" and Windows paths such as
	// "C:\\certs\\cert.pem" without maintaining a fragile path-prefix allow-list.
	info, err := os.Stat(s)
	if err == nil && !info.IsDir() {
		return digestFileContent(s)
	}
	return digestString(s)
}

func sniKey(names []string) string {
	if len(names) == 0 {
		return "_default_"
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func tlsFingerprint(cfg *config.Config) any {
	byAddr := map[string]map[string]any{}
	for _, s := range cfg.Servers {
		hosts, ok := byAddr[s.Listen]
		if !ok {
			hosts = map[string]any{}
			byAddr[s.Listen] = hosts
		}
		m := map[string]any{
			"enabled":          false,
			"client_auth_mode": "",
			"cert_file":        "",
			"key_file":         "",
			"ca_file":          "",
			"crl_file":         "",
			"verify_san":       []string(nil),
		}
		if s.TLS != nil {
			m["enabled"] = s.TLS.Enabled
			if s.TLS.ClientAuth != nil {
				m["client_auth_mode"] = s.TLS.ClientAuth.Mode
				m["ca_file"] = digestTLSFile(s.TLS.ClientAuth.CAFile)
				m["crl_file"] = digestTLSFile(s.TLS.ClientAuth.CRLFile)
				m["verify_san"] = s.TLS.ClientAuth.VerifySAN
			}
			m["cert_file"] = digestTLSFile(s.TLS.Cert)
			m["key_file"] = digestTLSFile(s.TLS.Key)
			if s.TLS.ACME != nil {
				m["acme"] = acmeFingerprint(s.TLS.ACME)
			}
		}
		hosts[sniKey(s.ServerNames)] = m
	}
	entries := map[string]any{}
	for addr, hosts := range byAddr {
		entries[addr] = hosts
	}
	return entries
}

func acmeFingerprint(a *config.ACMEConfig) map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{
		"enabled":       a.Enabled,
		"email":         a.Email,
		"ca":            a.CA,
		"domains":       a.Domains,
		"challenge":     a.Challenge,
		"cache_dir":     a.CacheDir,
		"ocsp_stapling": a.OCSPStaplingEnabled(),
	}
}

func http3Fingerprint(cfg *config.Config) any {
	byAddr := map[string]map[string]any{}
	for _, s := range cfg.Servers {
		hosts, ok := byAddr[s.Listen]
		if !ok {
			hosts = map[string]any{}
			byAddr[s.Listen] = hosts
		}
		m := map[string]any{
			"enabled":         s.HTTP3 != nil && s.HTTP3.Enabled,
			"alt_svc_max_age": 0,
		}
		if s.HTTP3 != nil {
			m["alt_svc_max_age"] = s.HTTP3.AltSvcMaxAge
		}
		hosts[sniKey(s.ServerNames)] = m
	}
	entries := map[string]any{}
	for addr, hosts := range byAddr {
		entries[addr] = hosts
	}
	return entries
}

func h2cFingerprint(cfg *config.Config) any {
	byAddr := map[string]map[string]any{}
	for _, s := range cfg.Servers {
		hosts, ok := byAddr[s.Listen]
		if !ok {
			hosts = map[string]any{}
			byAddr[s.Listen] = hosts
		}
		hosts[sniKey(s.ServerNames)] = map[string]any{
			"h2c": s.H2C,
		}
	}
	entries := map[string]any{}
	for addr, hosts := range byAddr {
		entries[addr] = hosts
	}
	return entries
}

func streamProtocolFingerprint(cfg *config.Config) any {
	entries := map[string]any{}
	for _, s := range cfg.Streams {
		entries[s.Listen] = map[string]any{
			"listen":   s.Listen,
			"protocol": normalizeStreamProtocol(s.Protocol),
		}
	}
	return entries
}

// effectiveValue uses reflection to navigate the config struct from a dotted
// path. It is a fallback for fields not explicitly handled above; it returns
// values in a serialization-stable form (string, int, bool, []any, map[string]any).
func effectiveValue(cfg *config.Config, path string) any {
	parts := strings.Split(path, ".")
	v := reflect.ValueOf(cfg)
	for _, part := range parts {
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return nil
		}
		f := v.FieldByNameFunc(func(name string) bool {
			return strings.EqualFold(name, part)
		})
		if !f.IsValid() {
			return nil
		}
		v = f
	}
	return normalizeReflectValue(v)
}

func normalizeReflectValue(v reflect.Value) any {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(v.Uint())
	case reflect.Bool:
		return v.Bool()
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = normalizeReflectValue(v.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for _, k := range v.MapKeys() {
			out[fmt.Sprint(k.Interface())] = normalizeReflectValue(v.MapIndex(k))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return normalizeReflectValue(v.Elem())
	case reflect.Struct:
		return fmt.Sprint(v.Interface())
	default:
		return fmt.Sprint(v.Interface())
	}
}

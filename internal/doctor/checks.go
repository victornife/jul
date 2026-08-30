// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package doctor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"jul/internal/app"
	"jul/internal/config"
	"jul/internal/diagnostics"
)

const diagnosticsDocs = "docs/diagnostics.md"

type session struct {
	options Options
	cfg     *config.Config
	loadErr error
}

func (s *session) registry() []diagnostics.Check {
	return []diagnostics.Check{
		s.check("CONFIG_FILE", "configuration", s.configFileCheck),
		s.check("CONFIG_PARSE", "configuration", s.configParseCheck),
		s.check("CONFIG_VALIDATE", "configuration", s.configValidateCheck),
		s.check("CONFIG_LINT", "configuration", s.configLintCheck),
		s.check("CONFIGURED_PATHS", "deployment", s.configuredPathsCheck),
		s.check("TLS_CERTIFICATES", "security", s.tlsCertificatesCheck),
		s.check("ADMIN_SECURITY", "security", s.adminSecurityCheck),
		s.check("CONFIG_TOPOLOGY", "runtime", s.topologyCheck),
		s.check("SYSTEM_RUNTIME", "runtime", s.systemRuntimeCheck),
		s.check("RUNTIME_PREFLIGHT", "network", s.runtimePreflightCheck),
		s.check("LISTENER_BIND", "network", s.listenerBindCheck),
	}
}

func (s *session) check(code, phase string, fn func(context.Context) diagnostics.Result) diagnostics.Check {
	return diagnostics.CheckFunc{
		Metadata: diagnostics.Spec{Code: code, Phase: phase, Docs: diagnosticsDocs},
		Fn:       fn,
	}
}

func (s *session) configFileCheck(context.Context) diagnostics.Result {
	info, err := os.Lstat(s.options.ConfigPath)
	if err != nil {
		return errorResult("configuration file is not accessible", err, "verify the path and the account running Jul")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return diagnostics.Result{
			Status:      diagnostics.StatusWarning,
			Severity:    diagnostics.SeverityWarning,
			Message:     "configuration path is a symbolic link",
			Evidence:    map[string]any{"mode": info.Mode().String(), "size_bytes": info.Size()},
			Remediation: "prefer a regular, operator-owned configuration file or verify the symlink target and ownership policy",
		}
	}
	if !info.Mode().IsRegular() {
		return diagnostics.Result{
			Status:      diagnostics.StatusError,
			Severity:    diagnostics.SeverityError,
			Message:     "configuration path is not a regular file",
			Evidence:    map[string]any{"mode": info.Mode().String()},
			Remediation: "point jul doctor at a regular TOML configuration file",
		}
	}
	status := diagnostics.StatusPass
	severity := diagnostics.SeverityInfo
	message := "configuration file is a readable regular file"
	remediation := ""
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		status = diagnostics.StatusWarning
		severity = diagnostics.SeverityWarning
		message = "configuration file is readable by group or other users"
		remediation = "review the file contents and tighten permissions when it contains secret references or sensitive topology"
	}
	file, err := os.Open(s.options.ConfigPath)
	if err != nil {
		return errorResult("configuration file cannot be opened for reading", err, "grant read access to the Jul service account")
	}
	_ = file.Close()
	return diagnostics.Result{
		Status:      status,
		Severity:    severity,
		Message:     message,
		Evidence:    map[string]any{"mode": info.Mode().Perm().String(), "size_bytes": info.Size()},
		Remediation: remediation,
	}
}

func (s *session) configParseCheck(context.Context) diagnostics.Result {
	s.cfg, s.loadErr = config.NewTOMLSource(s.options.ConfigPath).Load()
	if s.loadErr != nil {
		return errorResult("strict TOML decoding failed", s.loadErr, "correct the reported syntax, unknown field or value before starting Jul")
	}
	return diagnostics.Result{
		Status:   diagnostics.StatusPass,
		Severity: diagnostics.SeverityInfo,
		Message:  "strict TOML decoding succeeded",
	}
}

func (s *session) configValidateCheck(context.Context) diagnostics.Result {
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	errorsFound := flattenErrors(config.Validate(s.cfg))
	if len(errorsFound) == 0 {
		return diagnostics.Result{Status: diagnostics.StatusPass, Severity: diagnostics.SeverityInfo, Message: "semantic configuration validation succeeded"}
	}
	messages := make([]string, len(errorsFound))
	for i, err := range errorsFound {
		messages[i] = err.Error()
	}
	return diagnostics.Result{
		Status:      diagnostics.StatusError,
		Severity:    diagnostics.SeverityError,
		Message:     fmt.Sprintf("semantic configuration validation found %d error(s)", len(messages)),
		Evidence:    map[string]any{"errors": messages},
		Remediation: "correct every validation error; doctor reports independent later checks as skipped where their prerequisites are unavailable",
	}
}

func (s *session) configLintCheck(context.Context) diagnostics.Result {
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	findings := config.Lint(s.cfg)
	authority, _ := app.ResolveConfigAuthority(s.cfg.Global.ConfigAuthority, s.options.ConfigPath != "")
	findings = append(findings, app.CheckManagedFilesystem(s.options.ConfigPath, authority)...)
	findings = append(findings, app.CheckFileOwnedArtifacts(s.options.ConfigPath, authority)...)
	if len(findings) == 0 {
		return diagnostics.Result{Status: diagnostics.StatusPass, Severity: diagnostics.SeverityInfo, Message: "no configuration lint findings"}
	}
	rows := make([]map[string]any, 0, len(findings))
	status := diagnostics.StatusPass
	severity := diagnostics.SeverityInfo
	for _, finding := range findings {
		rows = append(rows, map[string]any{
			"severity": finding.Severity.String(),
			"field":    finding.Field,
			"message":  finding.Message,
			"hint":     finding.Hint,
		})
		switch finding.Severity {
		case config.SeverityError:
			status = diagnostics.StatusError
			severity = diagnostics.SeverityError
		case config.SeverityWarning:
			if status != diagnostics.StatusError {
				status = diagnostics.StatusWarning
				severity = diagnostics.SeverityWarning
			}
		}
	}
	return diagnostics.Result{
		Status:      status,
		Severity:    severity,
		Message:     fmt.Sprintf("configuration lint produced %d finding(s)", len(rows)),
		Evidence:    map[string]any{"findings": rows},
		Remediation: "review each finding and apply the linked hint before production use",
	}
}

func (s *session) configuredPathsCheck(context.Context) diagnostics.Result {
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	paths := collectConfiguredPaths(s.cfg)
	if len(paths) == 0 {
		return diagnostics.Result{Status: diagnostics.StatusPass, Severity: diagnostics.SeverityInfo, Message: "configuration declares no external files or directories that require a local path check"}
	}
	var errorsCount, warningsCount, checked int
	states := map[string]int{}
	for _, item := range paths {
		state, status := inspectConfiguredPath(item)
		states[item.Kind+":"+state]++
		checked++
		switch status {
		case diagnostics.StatusError:
			errorsCount++
		case diagnostics.StatusWarning:
			warningsCount++
		}
	}
	status := diagnostics.StatusPass
	severity := diagnostics.SeverityInfo
	message := fmt.Sprintf("checked %d configured path(s) without exposing their values", checked)
	if errorsCount > 0 {
		status = diagnostics.StatusError
		severity = diagnostics.SeverityError
		message = fmt.Sprintf("configured path checks found %d error(s) and %d warning(s)", errorsCount, warningsCount)
	} else if warningsCount > 0 {
		status = diagnostics.StatusWarning
		severity = diagnostics.SeverityWarning
		message = fmt.Sprintf("configured path checks found %d warning(s)", warningsCount)
	}
	return diagnostics.Result{
		Status:      status,
		Severity:    severity,
		Message:     message,
		Evidence:    map[string]any{"checked": checked, "errors": errorsCount, "warnings": warningsCount, "states": states},
		Remediation: "verify missing inputs, file types, symlink targets and private-key permissions; path values are intentionally omitted from this report",
	}
}

func (s *session) tlsCertificatesCheck(context.Context) diagnostics.Result {
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	pairs := collectCertificatePairs(s.cfg)
	if len(pairs) == 0 {
		return diagnostics.Result{Status: diagnostics.StatusSkipped, Severity: diagnostics.SeverityInfo, Message: "no operator-supplied certificate/key pairs are configured"}
	}
	now := time.Now()
	var valid, expired, notYetValid, expiringSoon, invalid, hostnameMismatches, hostnamesChecked int
	for _, pair := range pairs {
		certificate, err := tls.LoadX509KeyPair(pair.Cert, pair.Key)
		if err != nil || len(certificate.Certificate) == 0 {
			invalid++
			continue
		}
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			invalid++
			continue
		}
		hostnameMismatch := false
		for _, configuredName := range pair.ServerNames {
			name := strings.TrimSuffix(strings.TrimSpace(configuredName), ".")
			if name == "" || name == "_" || strings.Contains(name, "*") {
				continue
			}
			hostnamesChecked++
			if err := leaf.VerifyHostname(name); err != nil {
				hostnameMismatches++
				hostnameMismatch = true
			}
		}
		switch {
		case now.Before(leaf.NotBefore):
			notYetValid++
		case now.After(leaf.NotAfter):
			expired++
		case leaf.NotAfter.Sub(now) <= 30*24*time.Hour:
			expiringSoon++
		default:
			if !hostnameMismatch {
				valid++
			}
		}
	}
	status := diagnostics.StatusPass
	severity := diagnostics.SeverityInfo
	message := fmt.Sprintf("validated %d certificate/key pair(s)", len(pairs))
	if invalid > 0 || expired > 0 || notYetValid > 0 || hostnameMismatches > 0 {
		status = diagnostics.StatusError
		severity = diagnostics.SeverityError
		message = "one or more configured certificate/key pairs are invalid, outside their validity period, or do not cover a configured server name"
	} else if expiringSoon > 0 {
		status = diagnostics.StatusWarning
		severity = diagnostics.SeverityWarning
		message = "one or more configured certificates expire within 30 days"
	}
	return diagnostics.Result{
		Status:   status,
		Severity: severity,
		Message:  message,
		Evidence: map[string]any{
			"pairs":                   len(pairs),
			"valid":                   valid,
			"expiring_within_30_days": expiringSoon,
			"expired":                 expired,
			"not_yet_valid":           notYetValid,
			"invalid":                 invalid,
			"hostnames_checked":       hostnamesChecked,
			"hostname_mismatches":     hostnameMismatches,
		},
		Remediation: "replace invalid, mismatched, not-yet-valid, expired, or expiring certificate material and rerun doctor before starting or reloading Jul",
	}
}

func (s *session) adminSecurityCheck(context.Context) diagnostics.Result {
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	metadata := SafeConfigMetadata(s.cfg, nil)
	if !s.cfg.Admin.Enabled {
		return diagnostics.Result{
			Status:   diagnostics.StatusPass,
			Severity: diagnostics.SeverityInfo,
			Message:  "admin listener is disabled",
			Evidence: map[string]any{"authority": metadata.Authority, "enabled": false},
		}
	}
	loopback := isLoopbackListen(s.cfg.Admin.Listen)
	status := diagnostics.StatusPass
	severity := diagnostics.SeverityInfo
	message := "admin listener is loopback-bound and has an authentication mechanism"
	remediation := ""
	if !metadata.AdminAuthenticated && !loopback {
		status = diagnostics.StatusError
		severity = diagnostics.SeverityError
		message = "admin listener is non-loopback and has no authentication mechanism"
		remediation = "bind the admin listener to loopback or configure a token/RBAC credential before exposing it"
	} else if !metadata.AdminAuthenticated {
		status = diagnostics.StatusWarning
		severity = diagnostics.SeverityWarning
		message = "admin listener has no authentication mechanism"
		remediation = "configure a token or RBAC principal even on loopback when local users are not equally trusted"
	} else if !loopback {
		status = diagnostics.StatusWarning
		severity = diagnostics.SeverityWarning
		message = "admin listener is authenticated but bound beyond loopback"
		remediation = "restrict network reachability and use the supported secure transport boundary before remote administration"
	}
	return diagnostics.Result{
		Status:      status,
		Severity:    severity,
		Message:     message,
		Evidence:    map[string]any{"authority": metadata.Authority, "enabled": true, "loopback": loopback, "authenticated": metadata.AdminAuthenticated, "rbac": metadata.AdminRBACEnabled},
		Remediation: remediation,
	}
}

func (s *session) topologyCheck(context.Context) diagnostics.Result {
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	metadata := SafeConfigMetadata(s.cfg, s.options.Capabilities)
	return diagnostics.Result{
		Status:   diagnostics.StatusPass,
		Severity: diagnostics.SeverityInfo,
		Message:  "captured bounded configuration topology metadata",
		Evidence: map[string]any{"metadata": metadata},
	}
}

func (s *session) systemRuntimeCheck(context.Context) diagnostics.Result {
	return diagnostics.Result{
		Status:   diagnostics.StatusPass,
		Severity: diagnostics.SeverityInfo,
		Message:  "captured safe process and build runtime metadata",
		Evidence: map[string]any{
			"product":       s.options.Product,
			"version":       s.options.Version,
			"commit":        s.options.Commit,
			"build_profile": s.options.BuildProfile,
			"go_version":    runtime.Version(),
			"goos":          runtime.GOOS,
			"goarch":        runtime.GOARCH,
			"num_cpu":       runtime.NumCPU(),
			"gomaxprocs":    runtime.GOMAXPROCS(0),
			"capabilities":  cloneCapabilities(s.options.Capabilities),
		},
	}
}

func (s *session) runtimePreflightCheck(ctx context.Context) diagnostics.Result {
	if !s.options.CheckNetwork {
		return diagnostics.Result{
			Status:      diagnostics.StatusSkipped,
			Severity:    diagnostics.SeverityInfo,
			Message:     "full runtime preflight is disabled in the default network-free mode",
			Remediation: "rerun with -check-network to resolve secret references and initialize network-capable runtime components under a timeout",
		}
	}
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	if err := app.ValidateRuntimeConfig(ctx, s.cfg); err != nil {
		return errorResult("authoritative runtime preflight failed", err, "fix the reported build capability, secret, authentication, WAF or compression prerequisite")
	}
	return diagnostics.Result{Status: diagnostics.StatusPass, Severity: diagnostics.SeverityInfo, Message: "authoritative runtime preflight succeeded"}
}

func (s *session) listenerBindCheck(ctx context.Context) diagnostics.Result {
	if !s.options.CheckNetwork {
		return diagnostics.Result{
			Status:      diagnostics.StatusSkipped,
			Severity:    diagnostics.SeverityInfo,
			Message:     "listener bind probes are disabled in the default network-free mode",
			Remediation: "rerun with -check-network to perform immediate-close local TCP/UDP bind probes",
		}
	}
	if s.cfg == nil {
		return prerequisiteSkipped("configuration was not decoded", "resolve CONFIG_PARSE first")
	}
	listeners := configuredListeners(s.cfg)
	if len(listeners) == 0 {
		return diagnostics.Result{Status: diagnostics.StatusSkipped, Severity: diagnostics.SeverityInfo, Message: "configuration declares no listeners"}
	}
	var available, conflicts int
	byNetwork := map[string]int{}
	for _, listener := range listeners {
		if err := probeListener(ctx, listener.Network, listener.Address); err != nil {
			conflicts++
			continue
		}
		available++
		byNetwork[listener.Network]++
	}
	if conflicts > 0 {
		return diagnostics.Result{
			Status:      diagnostics.StatusError,
			Severity:    diagnostics.SeverityError,
			Message:     fmt.Sprintf("%d configured listener bind probe(s) failed", conflicts),
			Evidence:    map[string]any{"checked": len(listeners), "available": available, "failed": conflicts, "available_by_network": byNetwork},
			Remediation: "identify port conflicts, address-family mismatches or permission restrictions before starting Jul",
		}
	}
	return diagnostics.Result{
		Status:   diagnostics.StatusPass,
		Severity: diagnostics.SeverityInfo,
		Message:  fmt.Sprintf("all %d configured listener bind probe(s) succeeded and were closed immediately", len(listeners)),
		Evidence: map[string]any{"checked": len(listeners), "available_by_network": byNetwork},
	}
}

func errorResult(message string, err error, remediation string) diagnostics.Result {
	evidence := map[string]any{}
	if err != nil {
		evidence["error"] = err.Error()
	}
	return diagnostics.Result{
		Status:      diagnostics.StatusError,
		Severity:    diagnostics.SeverityError,
		Message:     message,
		Evidence:    evidence,
		Remediation: remediation,
	}
}

func prerequisiteSkipped(message, remediation string) diagnostics.Result {
	return diagnostics.Result{
		Status:      diagnostics.StatusSkipped,
		Severity:    diagnostics.SeverityInfo,
		Message:     message,
		Remediation: remediation,
	}
}

func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, child := range joined.Unwrap() {
			out = append(out, flattenErrors(child)...)
		}
		return out
	}
	return []error{err}
}

type configuredPath struct {
	Kind    string
	Path    string
	WantDir bool
	Input   bool
	Private bool
}

func collectConfiguredPaths(cfg *config.Config) []configuredPath {
	var paths []configuredPath
	add := func(item configuredPath) {
		if item.Path != "" {
			paths = append(paths, item)
		}
	}
	add(configuredPath{Kind: "cache_directory", Path: cfg.Cache.DiskPath, WantDir: true})
	add(configuredPath{Kind: "admin_history_directory", Path: cfg.Admin.HistoryDir, WantDir: true})
	add(configuredPath{Kind: "admin_plugin_directory", Path: cfg.Admin.PluginUploadDir, WantDir: true})
	add(configuredPath{Kind: "audit_log", Path: cfg.Admin.AuditLogFile})
	add(configuredPath{Kind: "access_log", Path: cfg.Observability.AccessLog.File})
	for _, plugin := range cfg.Plugins {
		add(configuredPath{Kind: "plugin_module", Path: plugin.Path, Input: true})
	}
	for _, file := range cfg.WAF.DirectivesFiles {
		add(configuredPath{Kind: "waf_directives", Path: file, Input: true})
	}
	for _, server := range cfg.Servers {
		if server.TLS != nil {
			add(configuredPath{Kind: "tls_certificate", Path: server.TLS.Cert, Input: true})
			add(configuredPath{Kind: "tls_private_key", Path: server.TLS.Key, Input: true, Private: true})
			if server.TLS.ClientAuth != nil {
				add(configuredPath{Kind: "client_ca", Path: server.TLS.ClientAuth.CAFile, Input: true})
				add(configuredPath{Kind: "client_crl", Path: server.TLS.ClientAuth.CRLFile, Input: true})
			}
			if server.TLS.ACME != nil {
				add(configuredPath{Kind: "acme_cache_directory", Path: server.TLS.ACME.CacheDir, WantDir: true})
			}
		}
		for _, location := range server.Locations {
			add(configuredPath{Kind: "static_root", Path: location.Root, WantDir: true, Input: true})
			appendBackendTLSPaths(&paths, location.BackendTLS)
			if location.Auth != nil && location.Auth.Basic != nil {
				add(configuredPath{Kind: "htpasswd", Path: location.Auth.Basic.File, Input: true, Private: true})
			}
			if location.GRPCTranscode != nil {
				add(configuredPath{Kind: "grpc_descriptor_set", Path: location.GRPCTranscode.DescriptorSet, Input: true})
			}
			if location.WAF != nil {
				for _, file := range location.WAF.DirectivesFiles {
					add(configuredPath{Kind: "waf_directives", Path: file, Input: true})
				}
			}
		}
	}
	for _, upstream := range cfg.Upstreams {
		appendBackendTLSPaths(&paths, upstream.BackendTLS)
		if upstream.Discovery != nil {
			if upstream.Discovery.Consul != nil {
				appendBackendTLSPaths(&paths, upstream.Discovery.Consul.TLS)
			}
			if upstream.Discovery.Kubernetes != nil {
				add(configuredPath{Kind: "kubernetes_ca", Path: upstream.Discovery.Kubernetes.CAFile, Input: true})
			}
		}
	}
	return dedupeConfiguredPaths(paths)
}

func appendBackendTLSPaths(paths *[]configuredPath, tlsConfig *config.BackendTLSConfig) {
	if tlsConfig == nil {
		return
	}
	*paths = append(*paths,
		configuredPath{Kind: "backend_ca", Path: tlsConfig.CAFile, Input: true},
		configuredPath{Kind: "backend_client_certificate", Path: tlsConfig.ClientCert, Input: true},
		configuredPath{Kind: "backend_client_private_key", Path: tlsConfig.ClientKey, Input: true, Private: true},
	)
}

func dedupeConfiguredPaths(input []configuredPath) []configuredPath {
	seen := map[string]struct{}{}
	out := make([]configuredPath, 0, len(input))
	for _, item := range input {
		if item.Path == "" {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%t\x00%t", item.Kind, filepath.Clean(item.Path), item.WantDir, item.Input)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func inspectConfiguredPath(item configuredPath) (string, diagnostics.Status) {
	info, err := os.Lstat(item.Path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "inaccessible", diagnostics.StatusError
		}
		if item.Input {
			return "missing", diagnostics.StatusError
		}
		parent := item.Path
		if !item.WantDir {
			parent = filepath.Dir(item.Path)
		}
		for {
			parentInfo, parentErr := os.Stat(parent)
			if parentErr == nil {
				if parentInfo.IsDir() {
					return "not_created_parent_exists", diagnostics.StatusPass
				}
				return "parent_not_directory", diagnostics.StatusError
			}
			next := filepath.Dir(parent)
			if next == parent {
				return "parent_missing", diagnostics.StatusWarning
			}
			parent = next
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink", diagnostics.StatusWarning
	}
	if item.WantDir && !info.IsDir() {
		return "wrong_type", diagnostics.StatusError
	}
	if !item.WantDir && !info.Mode().IsRegular() {
		return "wrong_type", diagnostics.StatusError
	}
	if runtime.GOOS != "windows" && item.Private && info.Mode().Perm()&0o077 != 0 {
		return "private_mode_too_open", diagnostics.StatusWarning
	}
	if item.Input {
		file, openErr := os.Open(item.Path)
		if openErr != nil {
			return "unreadable", diagnostics.StatusError
		}
		_ = file.Close()
	}
	return "ok", diagnostics.StatusPass
}

type certificatePair struct {
	Cert        string
	Key         string
	ServerNames []string
}

func collectCertificatePairs(cfg *config.Config) []certificatePair {
	var pairs []certificatePair
	byIdentity := map[string]int{}
	add := func(cert, key string, serverNames []string) {
		if cert == "" || key == "" {
			return
		}
		identity := filepath.Clean(cert) + "\x00" + filepath.Clean(key)
		if index, ok := byIdentity[identity]; ok {
			pairs[index].ServerNames = mergeServerNames(pairs[index].ServerNames, serverNames)
			return
		}
		byIdentity[identity] = len(pairs)
		pairs = append(pairs, certificatePair{Cert: cert, Key: key, ServerNames: mergeServerNames(nil, serverNames)})
	}
	for _, server := range cfg.Servers {
		if server.TLS != nil && (server.TLS.ACME == nil || !server.TLS.ACME.Enabled) {
			add(server.TLS.Cert, server.TLS.Key, server.ServerNames)
		}
		for _, location := range server.Locations {
			if location.BackendTLS != nil {
				add(location.BackendTLS.ClientCert, location.BackendTLS.ClientKey, nil)
			}
		}
	}
	for _, upstream := range cfg.Upstreams {
		if upstream.BackendTLS != nil {
			add(upstream.BackendTLS.ClientCert, upstream.BackendTLS.ClientKey, nil)
		}
		if upstream.Discovery != nil && upstream.Discovery.Consul != nil && upstream.Discovery.Consul.TLS != nil {
			add(upstream.Discovery.Consul.TLS.ClientCert, upstream.Discovery.Consul.TLS.ClientKey, nil)
		}
	}
	return pairs
}

func mergeServerNames(existing, additional []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additional))
	for _, name := range existing {
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, name := range additional {
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func countListeners(cfg *config.Config) int {
	return len(configuredListeners(cfg))
}

type listenerSpec struct {
	Network string
	Address string
}

func configuredListeners(cfg *config.Config) []listenerSpec {
	var listeners []listenerSpec
	for _, server := range cfg.Servers {
		listeners = append(listeners, listenerSpec{Network: "tcp", Address: server.Listen})
		if server.HTTP3 != nil && server.HTTP3.Enabled {
			listeners = append(listeners, listenerSpec{Network: "udp", Address: server.Listen})
		}
	}
	for _, stream := range cfg.Streams {
		network := strings.ToLower(stream.Protocol)
		if network == "" {
			network = "tcp"
		}
		listeners = append(listeners, listenerSpec{Network: network, Address: stream.Listen})
	}
	if cfg.Admin.Enabled && cfg.Admin.Listen != "" {
		listeners = append(listeners, listenerSpec{Network: "tcp", Address: cfg.Admin.Listen})
	}
	seen := map[string]struct{}{}
	out := make([]listenerSpec, 0, len(listeners))
	for _, listener := range listeners {
		key := listener.Network + "\x00" + listener.Address
		if listener.Address == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, listener)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Network == out[j].Network {
			return out[i].Address < out[j].Address
		}
		return out[i].Network < out[j].Network
	})
	return out
}

func probeListener(ctx context.Context, network, address string) error {
	var listenConfig net.ListenConfig
	if network == "udp" {
		packet, err := listenConfig.ListenPacket(ctx, network, address)
		if err != nil {
			return err
		}
		return packet.Close()
	}
	listener, err := listenConfig.Listen(ctx, network, address)
	if err != nil {
		return err
	}
	return listener.Close()
}

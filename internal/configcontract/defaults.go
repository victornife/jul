// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

// DefaultOverrides is the small, explicit, reviewable table of documented
// defaults, joined against canonical schema paths. It is deliberately NOT
// derived by parsing zero_semantics prose (e.g. "omitted/zero defaults to
// 14") — each entry is a human-authored fact read directly from the Go doc
// comment that states it, kept separate from zero/empty semantics rather
// than collapsing the two.
//
// Coverage is representative, not exhaustive: a field is included only when
// its Go doc comment states a concrete, unconditional (or simply-described
// conditional, e.g. admin.console) default. A field whose omission simply
// means "zero/disabled" already has that fact in ZeroSemantics and gets no
// separate entry here, so the two concepts are never duplicated. A field
// whose default depends on another field in a way that cannot be stated as
// one short fact (e.g. "positive when upload is enabled; otherwise
// non-negative") is left out rather than flattened into a misleading string.
var DefaultOverrides = map[string]string{
	"global.worker_threads":           "auto",
	"global.log_format":               "text",
	"global.log_level":                "info",
	"global.redact_min_secret_length": "4",
	"global.reload_timeout":           "10s",
	"global.shutdown_timeout":         "30s",
	"global.config_authority":         "file_owned",

	"servers.*.client_address.forwarded_headers": "[forwarded, x-forwarded-for]",
	"servers.*.client_address.max_hops":          "16",
	"servers.*.max_header_bytes":                 "1m",

	"servers.*.locations.*.cors.allowed_methods":                      "[GET, HEAD, POST]",
	"servers.*.locations.*.grpc_transcode.stream_mode":                "ndjson",
	"servers.*.locations.*.grpc_transcode.max_message_size":           "4m",
	"servers.*.locations.*.grpc_transcode.preserve_proto_field_names": "false",
	"servers.*.locations.*.waf.mode":                                  "block",
	"servers.*.locations.*.waf.request_body_limit":                    "128k",
	"servers.*.locations.*.backend_tls.ca_mode":                       "system",
	"servers.*.locations.*.backend_tls.min_version":                   "1.2",
	"servers.*.locations.*.auth.basic.realm":                          "Restricted",
	"servers.*.locations.*.auth.jwt.timeout":                          "10s",
	"servers.*.locations.*.auth.forward_auth.timeout":                 "10s",

	"waf.mode":               "block",
	"waf.request_body_limit": "128k",

	"stream.*.connect_timeout":  "10s",
	"stream.*.idle_timeout":     "5m",
	"stream.*.max_udp_sessions": "10000",
	"stream.*.protocol":         "tcp",

	"plugins.*.type":               "middleware",
	"plugins.*.memory_limit":       "16m",
	"plugins.*.timeout":            "100ms",
	"plugins.*.max_request_body":   "1m",
	"plugins.*.max_response_body":  "8m",
	"plugins.*.fetch_timeout":      "5s",
	"plugins.*.max_fetch_response": "1m",
	"plugins.*.kv_max_entries":     "1024",
	"plugins.*.kv_max_bytes":       "1m",

	"upstreams.*.strategy":                            "round_robin",
	"upstreams.*.resilience.circuit_half_open_probes": "1",
	"upstreams.*.resilience.max_fails":                "3",
	"upstreams.*.resilience.fail_timeout":             "10s",
	"upstreams.*.max_fails":                           "3",
	"upstreams.*.fail_timeout":                        "10s",
	"upstreams.*.health_check.type":                   "http",
	"upstreams.*.health_check.interval":               "5s",
	"upstreams.*.health_check.timeout":                "2s",
	"upstreams.*.health_check.healthy_threshold":      "2",
	"upstreams.*.health_check.unhealthy_threshold":    "3",
	"upstreams.*.health_check.expect_status":          "[200]",
	"upstreams.*.discovery.refresh":                   "30s",
	"upstreams.*.discovery.consul.address":            "http://127.0.0.1:8500",
	"upstreams.*.discovery.consul.passing_only":       "true",
	"upstreams.*.backend_tls.ca_mode":                 "system",
	"upstreams.*.backend_tls.min_version":             "1.2",
	"upstreams.*.discovery.consul.tls.ca_mode":        "system",
	"upstreams.*.discovery.consul.tls.min_version":    "1.2",

	"servers.*.tls.acme.ca":                         "letsencrypt-staging",
	"servers.*.tls.acme.challenge":                  "http-01",
	"servers.*.tls.acme.cache_dir":                  "./jul-data/certs",
	"servers.*.tls.acme.ocsp_stapling":              "true",
	"servers.*.tls.client_auth.mode":                "none",
	"servers.*.tls.client_auth.forward_certificate": "none",

	"compression.encoders": "[gzip]",
	"compression.min_size": "1k",

	"rate_limit.key": "ip",

	// admin.console's default depends on admin.enabled, but the dependency is
	// short enough to state as one fact rather than being left out.
	"admin.console":                  "true (when admin.enabled)",
	"admin.history_dir":              "./jul-data/config-history",
	"admin.history_keep":             "50",
	"admin.rate_limit_read_per_min":  "240",
	"admin.rate_limit_write_per_min": "60",
	"admin.rate_limit_apply_per_min": "30",
	"admin.max_event_conns":          "4",
	"admin.audit_log_rotate_max_mb":  "100",
	"admin.audit_log_rotate_keep":    "14",
	"admin.plugin_upload_max_size":   "32",
	"admin.plugin_upload_enabled":    "false",
	"admin.rbac.enabled":             "false",
	"admin.rbac.default_role":        "admin",

	"observability.access_log.enabled":   "true",
	"observability.access_log.format":    "text",
	"observability.tracing.exporter":     "otlp-grpc",
	"observability.tracing.sample_ratio": "1.0",
	"observability.tracing.service_name": "jul",
}

// DefaultFor returns the documented default for path, if one is recorded.
func DefaultFor(path string) (string, bool) {
	d, ok := DefaultOverrides[path]
	return d, ok
}

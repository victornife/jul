// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

type capabilityKey struct {
	context AssessmentContext
	name    string
}

type capability struct {
	code        string
	class       AssessmentClass
	severity    AssessmentSeverity
	risk        AssessmentRisk
	message     string
	targetPaths []string
}

var capabilityRegistry = map[capabilityKey]capability{
	{ContextMain, "http"}:                 supported("NGX_MAIN_HTTP", RiskOperational, "HTTP configuration is translated", nil),
	{ContextMain, "events"}:               informational("NGX_MAIN_EVENTS", RiskOperational, "events block is process-level context and is not emitted into Jul configuration"),
	{ContextMain, "worker_processes"}:     ignored("NGX_MAIN_WORKERS", RiskPerformance, "NGINX worker-process settings do not apply to Jul's Go runtime"),
	{ContextMain, "worker_rlimit_nofile"}: ignored("NGX_MAIN_RLIMIT", RiskOperational, "process resource limits remain deployment-manager responsibilities"),
	{ContextMain, "pid"}:                  ignored("NGX_MAIN_PID", RiskOperational, "PID-file management remains a process-supervisor responsibility"),
	{ContextMain, "user"}:                 ignored("NGX_MAIN_USER", RiskSecurity, "process user selection remains a deployment-manager responsibility"),
	{ContextMain, "daemon"}:               ignored("NGX_MAIN_DAEMON", RiskOperational, "Jul runs in the foreground and does not daemonize itself"),
	{ContextMain, "master_process"}:       ignored("NGX_MAIN_MASTER", RiskOperational, "NGINX master-process settings do not apply to Jul"),
	{ContextMain, "load_module"}:          blocking("NGX_MAIN_MODULE", RiskSecurity, "dynamic NGINX modules are not imported"),
	{ContextMain, "pcre_jit"}:             ignored("NGX_MAIN_PCRE_JIT", RiskPerformance, "regular-expression engine tuning is runtime-owned"),
	{ContextMain, "error_log"}:            ignored("NGX_MAIN_ERROR_LOG", RiskObservability, "process logging must be configured through Jul observability and the service manager"),
	{ContextMain, "stream"}:               supported("NGX_MAIN_STREAM", RiskOperational, "bounded stream configuration is translated", nil),
	{ContextMain, "mail"}:                 blocking("NGX_MAIN_MAIL", RiskRouting, "the NGINX mail module is not translated"),
	{ContextMain, "include"}:              blocking("NGX_MAIN_INCLUDE", RiskOperational, "included files are not traversed by this importer"),

	{ContextEvents, "worker_connections"}: ignored("NGX_EVENTS_CONNECTIONS", RiskPerformance, "NGINX worker connection limits do not map to Jul"),
	{ContextEvents, "multi_accept"}:       ignored("NGX_EVENTS_MULTI_ACCEPT", RiskPerformance, "NGINX accept-loop tuning does not map to Jul"),
	{ContextEvents, "use"}:                ignored("NGX_EVENTS_USE", RiskPerformance, "NGINX event-loop selection does not map to Jul"),

	{ContextHTTP, "server"}:               supported("NGX_HTTP_SERVER", RiskRouting, "server block is translated", []string{"servers[]"}),
	{ContextHTTP, "upstream"}:             supported("NGX_HTTP_UPSTREAM", RiskRouting, "upstream block is translated", []string{"upstreams[]"}),
	{ContextHTTP, "gzip"}:                 supported("NGX_HTTP_GZIP", RiskPerformance, "gzip enablement is translated", []string{"compression.enabled"}),
	{ContextHTTP, "include"}:              blocking("NGX_HTTP_INCLUDE", RiskOperational, "included files are not traversed by this importer"),
	{ContextHTTP, "map"}:                  blocking("NGX_HTTP_MAP", RiskRouting, "variable maps are not representable in the bounded Jul routing model"),
	{ContextHTTP, "geo"}:                  blocking("NGX_HTTP_GEO", RiskRouting, "geo maps are not translated"),
	{ContextHTTP, "split_clients"}:        blocking("NGX_HTTP_SPLIT_CLIENTS", RiskRouting, "traffic splitting is not translated"),
	{ContextHTTP, "log_format"}:           blocking("NGX_HTTP_LOG_FORMAT", RiskObservability, "custom NGINX log formats are not translated"),
	{ContextHTTP, "access_log"}:           blocking("NGX_HTTP_ACCESS_LOG", RiskObservability, "NGINX access-log destinations and formats require manual Jul configuration"),
	{ContextHTTP, "client_max_body_size"}: blocking("NGX_HTTP_BODY_LIMIT", RiskSecurity, "request body limits are not translated"),

	{ContextServer, "listen"}:                  supported("NGX_SERVER_LISTEN", RiskAvailability, "listener address is translated", []string{"servers[].listen", "servers[].tls.enabled"}),
	{ContextServer, "server_name"}:             supported("NGX_SERVER_NAME", RiskRouting, "server names are translated", []string{"servers[].server_names"}),
	{ContextServer, "root"}:                    supported("NGX_SERVER_ROOT", RiskRouting, "server root is translated through a static location", []string{"servers[].locations[].root"}),
	{ContextServer, "index"}:                   supported("NGX_SERVER_INDEX", RiskRouting, "index files are translated", []string{"servers[].locations[].index"}),
	{ContextServer, "location"}:                supported("NGX_SERVER_LOCATION", RiskRouting, "location block is translated", []string{"servers[].locations[]"}),
	{ContextServer, "ssl_certificate"}:         supported("NGX_SERVER_TLS_CERT", RiskSecurity, "TLS certificate reference is translated", []string{"servers[].tls.cert"}),
	{ContextServer, "ssl_certificate_key"}:     supported("NGX_SERVER_TLS_KEY", RiskSecurity, "TLS key reference is translated", []string{"servers[].tls.key"}),
	{ContextServer, "ssl_protocols"}:           supported("NGX_SERVER_TLS_PROTOCOLS", RiskSecurity, "supported TLS protocol floor is translated", []string{"servers[].tls.min_version"}),
	{ContextServer, "ssl_verify_client"}:       supported("NGX_SERVER_CLIENT_AUTH_MODE", RiskSecurity, "client-certificate verification mode is translated", []string{"servers[].tls.client_auth.mode"}),
	{ContextServer, "ssl_client_certificate"}:  supported("NGX_SERVER_CLIENT_AUTH_CA", RiskSecurity, "trusted client-certificate CA bundle is translated", []string{"servers[].tls.client_auth.ca_file"}),
	{ContextServer, "ssl_crl"}:                 supported("NGX_SERVER_CLIENT_AUTH_CRL", RiskSecurity, "client-certificate revocation list is translated", []string{"servers[].tls.client_auth.crl_file"}),
	{ContextServer, "ssl_verify_depth"}:        ignored("NGX_SERVER_TLS_VERIFY_DEPTH", RiskSecurity, "Jul's client-certificate chain verification depth is not independently configurable"),
	{ContextServer, "ssl_trusted_certificate"}: ignored("NGX_SERVER_TLS_TRUSTED_CERT", RiskSecurity, "Jul's OCSP stapling is ACME-managed and does not take a manually supplied trust chain"),
	{ContextServer, "return"}:                  approximated("NGX_SERVER_RETURN", RiskRouting, "server-level return is synthesized as a catch-all location; precedence differs"),
	{ContextServer, "if"}:                      blocking("NGX_SERVER_IF", RiskRouting, "server-level conditional logic is not translated"),
	{ContextServer, "rewrite"}:                 blocking("NGX_SERVER_REWRITE", RiskRouting, "server-level rewrite is not translated"),
	{ContextServer, "include"}:                 blocking("NGX_SERVER_INCLUDE", RiskOperational, "included files are not traversed by this importer"),
	{ContextServer, "access_log"}:              blocking("NGX_SERVER_ACCESS_LOG", RiskObservability, "per-server NGINX access logs require manual Jul configuration"),
	{ContextServer, "auth_basic"}:              blocking("NGX_SERVER_AUTH", RiskSecurity, "NGINX basic authentication is not translated"),
	{ContextServer, "auth_basic_user_file"}:    blocking("NGX_SERVER_AUTH_FILE", RiskSecurity, "NGINX credential-file authentication is not translated"),
	{ContextServer, "allow"}:                   blocking("NGX_SERVER_ALLOW", RiskSecurity, "source-address access controls are not translated"),
	{ContextServer, "deny"}:                    blocking("NGX_SERVER_DENY", RiskSecurity, "source-address access controls are not translated"),

	{ContextLocation, "proxy_pass"}:                supported("NGX_LOCATION_PROXY_PASS", RiskRouting, "proxy target is translated", []string{"servers[].locations[].proxy_pass"}),
	{ContextLocation, "fastcgi_pass"}:              supported("NGX_LOCATION_FASTCGI", RiskRouting, "FastCGI target is translated", []string{"servers[].locations[].fastcgi_pass"}),
	{ContextLocation, "fastcgi_param"}:             supported("NGX_LOCATION_FASTCGI_PARAM", RiskRouting, "literal FastCGI parameter is translated", []string{"servers[].locations[].fastcgi_params"}),
	{ContextLocation, "uwsgi_pass"}:                supported("NGX_LOCATION_UWSGI", RiskRouting, "uWSGI target is translated", []string{"servers[].locations[].uwsgi_pass"}),
	{ContextLocation, "uwsgi_param"}:               blocking("NGX_LOCATION_UWSGI_PARAM", RiskRouting, "Jul has no per-parameter uWSGI configuration equivalent"),
	{ContextLocation, "grpc_pass"}:                 supported("NGX_LOCATION_GRPC_PASS", RiskRouting, "native gRPC passthrough target is translated", []string{"servers[].locations[].proxy_pass", "servers[].locations[].grpc"}),
	{ContextLocation, "root"}:                      supported("NGX_LOCATION_ROOT", RiskRouting, "location root is translated", []string{"servers[].locations[].root"}),
	{ContextLocation, "alias"}:                     approximated("NGX_LOCATION_ALIAS", RiskRouting, "alias is mapped to root; prefix-stripping semantics differ"),
	{ContextLocation, "index"}:                     supported("NGX_LOCATION_INDEX", RiskRouting, "location index files are translated", []string{"servers[].locations[].index"}),
	{ContextLocation, "try_files"}:                 supported("NGX_LOCATION_TRY_FILES", RiskRouting, "try_files is translated", []string{"servers[].locations[].try_files"}),
	{ContextLocation, "return"}:                    supported("NGX_LOCATION_RETURN", RiskRouting, "return action is translated", []string{"servers[].locations[].return", "servers[].locations[].redirect"}),
	{ContextLocation, "rewrite"}:                   supported("NGX_LOCATION_REWRITE", RiskRouting, "rewrite action is translated", []string{"servers[].locations[].rewrites[]"}),
	{ContextLocation, "add_header"}:                supported("NGX_LOCATION_ADD_HEADER", RiskSecurity, "static always-applied response header is translated", []string{"servers[].locations[].response_headers[]", "servers[].locations[].cors"}),
	{ContextLocation, "limit_except"}:              approximated("NGX_LOCATION_LIMIT_EXCEPT", RiskSecurity, "bare method denial maps to a route predicate; excluded requests may resolve differently"),
	{ContextLocation, "if"}:                        blocking("NGX_LOCATION_IF", RiskRouting, "location-level conditional logic is not translated"),
	{ContextLocation, "include"}:                   blocking("NGX_LOCATION_INCLUDE", RiskOperational, "included files are not traversed by this importer"),
	{ContextLocation, "proxy_set_header"}:          blocking("NGX_LOCATION_PROXY_HEADER", RiskSecurity, "backend request-header policy is not translated"),
	{ContextLocation, "client_max_body_size"}:      blocking("NGX_LOCATION_BODY_LIMIT", RiskSecurity, "request body limits are not translated"),
	{ContextLocation, "auth_basic"}:                blocking("NGX_LOCATION_AUTH", RiskSecurity, "NGINX basic authentication is not translated"),
	{ContextLocation, "auth_basic_user_file"}:      blocking("NGX_LOCATION_AUTH_FILE", RiskSecurity, "NGINX credential-file authentication is not translated"),
	{ContextLocation, "auth_request"}:              blocking("NGX_LOCATION_AUTH_REQUEST", RiskSecurity, "NGINX subrequest authentication is not translated"),
	{ContextLocation, "allow"}:                     blocking("NGX_LOCATION_ALLOW", RiskSecurity, "source-address access controls are not translated"),
	{ContextLocation, "deny"}:                      blocking("NGX_LOCATION_DENY", RiskSecurity, "source-address access controls are not translated"),
	{ContextLocation, "limit_req"}:                 blocking("NGX_LOCATION_RATE_LIMIT", RiskAvailability, "NGINX request-rate limiting is not translated"),
	{ContextLocation, "limit_conn"}:                blocking("NGX_LOCATION_CONN_LIMIT", RiskAvailability, "NGINX connection limiting is not translated"),
	{ContextLocation, "proxy_connect_timeout"}:     supported("NGX_LOCATION_PROXY_CONNECT_TIMEOUT", RiskAvailability, "connection establishment timeout is translated", []string{"servers[].locations[].proxy_connect_timeout"}),
	{ContextLocation, "proxy_read_timeout"}:        supported("NGX_LOCATION_PROXY_READ_TIMEOUT", RiskAvailability, "upstream read-inactivity timeout is translated", []string{"servers[].locations[].proxy_read_timeout"}),
	{ContextLocation, "proxy_send_timeout"}:        supported("NGX_LOCATION_PROXY_SEND_TIMEOUT", RiskAvailability, "upstream write-inactivity timeout is translated", []string{"servers[].locations[].proxy_send_timeout"}),
	{ContextLocation, "proxy_next_upstream_tries"}: approximated("NGX_LOCATION_RETRY_ATTEMPTS", RiskAvailability, "an explicit bound of 2 or more tries is translated to retry_attempts (tries-1); 0 (unlimited) or 1 (no retry) cannot be distinguished from Jul's own \"inherit the pool default\" sentinel and stay blocking"),

	// Bounded proxy_cache subset: a single proxy_cache_path zone declared at
	// http level and referenced consistently by every proxy_cache in the
	// file translates onto Jul's one process-wide [cache]. Anything wider
	// (multiple zones, an undeclared zone, or a dynamic/expression-driven
	// cache-control directive) stays blocking - see cache_translate.go.
	{ContextHTTP, "proxy_cache_path"}:                  supported("NGX_HTTP_CACHE_PATH", RiskPerformance, "cache zone declaration is translated", []string{"cache.enabled", "cache.disk_path", "cache.disk_max_size"}),
	{ContextLocation, "proxy_cache"}:                   supported("NGX_LOCATION_CACHE", RiskSecurity, "cache enablement is translated", []string{"servers[].locations[].cache"}),
	{ContextLocation, "proxy_cache_valid"}:             approximated("NGX_LOCATION_CACHE_VALID", RiskPerformance, "proxy_cache_valid maps to [cache].default_ttl, which Jul applies only as a fallback when the upstream gives no explicit Cache-Control/Expires freshness"),
	{ContextLocation, "proxy_cache_bypass"}:            blocking("NGX_LOCATION_CACHE_BYPASS", RiskSecurity, "expression-driven cache bypass conditions are not translated"),
	{ContextLocation, "proxy_no_cache"}:                blocking("NGX_LOCATION_CACHE_NO_CACHE", RiskSecurity, "expression-driven cache-skip conditions are not translated"),
	{ContextLocation, "proxy_cache_key"}:               blocking("NGX_LOCATION_CACHE_KEY", RiskSecurity, "a custom cache key expression is not translated; Jul's key is always method + host + URI"),
	{ContextLocation, "proxy_ignore_headers"}:          blocking("NGX_LOCATION_CACHE_IGNORE_HEADERS", RiskSecurity, "Jul always honors upstream Cache-Control/Expires; a policy to ignore them is not translated"),
	{ContextLocation, "proxy_cache_min_uses"}:          ignored("NGX_LOCATION_CACHE_MIN_USES", RiskPerformance, "Jul has no minimum-uses gate before caching a response"),
	{ContextLocation, "proxy_cache_lock"}:              ignored("NGX_LOCATION_CACHE_LOCK", RiskPerformance, "Jul has no equivalent same-key fetch-serialization toggle"),
	{ContextLocation, "proxy_cache_lock_age"}:          ignored("NGX_LOCATION_CACHE_LOCK", RiskPerformance, "Jul has no equivalent same-key fetch-serialization toggle"),
	{ContextLocation, "proxy_cache_lock_timeout"}:      ignored("NGX_LOCATION_CACHE_LOCK", RiskPerformance, "Jul has no equivalent same-key fetch-serialization toggle"),
	{ContextLocation, "proxy_cache_background_update"}: ignored("NGX_LOCATION_CACHE_BACKGROUND_UPDATE", RiskPerformance, "Jul's stale-while-revalidate background refresh is unconditional and cannot be independently toggled"),
	{ContextLocation, "proxy_cache_revalidate"}:        approximated("NGX_LOCATION_CACHE_REVALIDATE", RiskPerformance, "Jul's background revalidation always reissues the request; it does not send conditional If-Modified-Since/If-None-Match"),
	{ContextLocation, "proxy_cache_use_stale"}:         approximated("NGX_LOCATION_CACHE_USE_STALE", RiskAvailability, "Jul's stale-while-revalidate/stale-if-error serving is unconditional and cannot be selectively disabled per error condition"),

	{ContextUpstream, "server"}:             supported("NGX_UPSTREAM_SERVER", RiskAvailability, "upstream backend is translated", []string{"upstreams[].servers[]"}),
	{ContextUpstream, "least_conn"}:         supported("NGX_UPSTREAM_LEAST_CONN", RiskAvailability, "least-connections balancing is translated", []string{"upstreams[].strategy"}),
	{ContextUpstream, "ip_hash"}:            approximatedWithTargets("NGX_UPSTREAM_IP_HASH", RiskAvailability, "ip_hash maps to consistent_hash on client_ip; NGINX hashes the first three octets of an IPv4 address with its own function while Jul hashes the full canonical client address by rendezvous hashing, so clients are re-placed once at cutover and one /24 may span backends", []string{"upstreams[].strategy", "upstreams[].hash.key"}),
	{ContextUpstream, "hash"}:               approximated("NGX_UPSTREAM_HASH", RiskAvailability, "hash maps to consistent_hash when its key is a supported source"),
	{ContextUpstream, "random"}:             approximated("NGX_UPSTREAM_RANDOM", RiskAvailability, "random balancing falls back to round-robin"),
	{ContextUpstream, "keepalive"}:          ignored("NGX_UPSTREAM_KEEPALIVE", RiskPerformance, "NGINX upstream connection-pool tuning is runtime-owned"),
	{ContextUpstream, "keepalive_timeout"}:  ignored("NGX_UPSTREAM_KEEPALIVE_TIMEOUT", RiskPerformance, "NGINX upstream connection-pool tuning is runtime-owned"),
	{ContextUpstream, "keepalive_requests"}: ignored("NGX_UPSTREAM_KEEPALIVE_REQUESTS", RiskPerformance, "NGINX upstream connection-pool tuning is runtime-owned"),
	{ContextUpstream, "zone"}:               ignored("NGX_UPSTREAM_ZONE", RiskOperational, "NGINX shared-memory zones do not apply to a single Jul process"),

	{ContextLimitExcept, "deny"}:   supported("NGX_LIMIT_EXCEPT_DENY", RiskSecurity, "bare deny-all body is consumed by the method-predicate translation", nil),
	{ContextLimitExcept, "return"}: supported("NGX_LIMIT_EXCEPT_RETURN", RiskSecurity, "bare return-403 body is consumed by the method-predicate translation", nil),

	// Bounded NGINX stream (L4) subset (#426). Anything not listed here falls
	// through to the ContextStream catch-all in classifyDirective, which stays
	// blocking by default - mail, Lua, third-party stream modules, arbitrary
	// map/variable programs, and unknown directives never need an entry to
	// remain safely blocking.
	{ContextStream, "server"}:                supported("NGX_STREAM_SERVER", RiskRouting, "stream server block is translated", []string{"stream[]"}),
	{ContextStream, "upstream"}:              supported("NGX_STREAM_UPSTREAM", RiskRouting, "named upstream is reused from the HTTP upstream translation", []string{"upstreams[]"}),
	{ContextStream, "listen"}:                supported("NGX_STREAM_LISTEN", RiskAvailability, "stream listener address is translated", []string{"stream[].listen", "stream[].protocol"}),
	{ContextStream, "server_name"}:           supported("NGX_STREAM_SERVER_NAME", RiskRouting, "stream server_name is translated into bounded SNI routing", []string{"stream[].sni_routes"}),
	{ContextStream, "ssl_preread"}:           supported("NGX_STREAM_SSL_PREREAD", RiskRouting, "ssl_preread is translated to Jul's bounded ClientHello SNI passthrough", []string{"stream[].sni_routes", "stream[].tls_passthrough"}),
	{ContextStream, "proxy_pass"}:            supported("NGX_STREAM_PROXY_PASS", RiskRouting, "stream proxy target is translated", []string{"stream[].proxy_pass"}),
	{ContextStream, "proxy_timeout"}:         supported("NGX_STREAM_PROXY_TIMEOUT", RiskAvailability, "proxy_timeout is translated to the stream idle timeout", []string{"stream[].idle_timeout"}),
	{ContextStream, "proxy_connect_timeout"}: supported("NGX_STREAM_PROXY_CONNECT_TIMEOUT", RiskAvailability, "proxy_connect_timeout is translated to the stream connect timeout", []string{"stream[].connect_timeout"}),
	{ContextStream, "proxy_protocol"}:        supported("NGX_STREAM_PROXY_PROTOCOL_OUT", RiskSecurity, "outbound PROXY-protocol propagation to the backend is translated", []string{"stream[].proxy_protocol"}),
	{ContextStream, "ssl"}:                   blocking("NGX_STREAM_TLS_TERMINATION", RiskSecurity, "stream TLS termination is not representable; Jul's stream listener only supports SNI-preread passthrough"),
	{ContextStream, "map"}:                   blocking("NGX_STREAM_MAP", RiskRouting, "variable maps (including ssl_preread-driven routing) are not representable in the bounded Jul stream model"),
	{ContextStream, "return"}:                blocking("NGX_STREAM_RETURN", RiskRouting, "stream return is not translated"),
}

func supported(code string, risk AssessmentRisk, message string, targets []string) capability {
	return capability{code: code, class: AssessmentSupported, severity: AssessmentInfo, risk: risk, message: message, targetPaths: targets}
}

func approximated(code string, risk AssessmentRisk, message string) capability {
	return capability{code: code, class: AssessmentApproximated, severity: AssessmentWarning, risk: risk, message: message}
}

func ignored(code string, risk AssessmentRisk, message string) capability {
	return capability{code: code, class: AssessmentIgnored, severity: AssessmentInfo, risk: risk, message: message}
}

func informational(code string, risk AssessmentRisk, message string) capability {
	return capability{code: code, class: AssessmentInformational, severity: AssessmentInfo, risk: risk, message: message}
}

func blocking(code string, risk AssessmentRisk, message string) capability {
	return capability{code: code, class: AssessmentBlocking, severity: AssessmentError, risk: risk, message: message}
}

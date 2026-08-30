// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"errors"
	"strings"
	"testing"

	"jul/internal/config"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

func assertCapabilityClass(t *testing.T, got capability, want AssessmentClass) {
	t.Helper()
	if got.class != want {
		t.Fatalf("class = %q, want %q: %+v", got.class, want, got)
	}
}

func TestClassifyRealIP(t *testing.T) {
	tests := []struct {
		name      string
		directive string
		params    []string
		want      AssessmentClass
	}{
		{"missing source", "set_real_ip_from", nil, AssessmentBlocking},
		{"unix source", "set_real_ip_from", []string{"unix:"}, AssessmentBlocking},
		{"invalid source", "set_real_ip_from", []string{"not-an-address"}, AssessmentBlocking},
		{"CIDR source", "set_real_ip_from", []string{"10.0.0.0/8"}, AssessmentSupported},
		{"missing header", "real_ip_header", nil, AssessmentBlocking},
		{"XFF", "real_ip_header", []string{"X-Forwarded-For"}, AssessmentSupported},
		{"Forwarded", "real_ip_header", []string{"Forwarded"}, AssessmentSupported},
		{"X-Real-IP", "real_ip_header", []string{"X-Real-IP"}, AssessmentBlocking},
		{"recursive off", "real_ip_recursive", []string{"off"}, AssessmentBlocking},
		{"recursive on", "real_ip_recursive", []string{"on"}, AssessmentSupported},
		{"unknown", "real_ip_unknown", nil, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyRealIP(tt.directive, tt.params), tt.want)
		})
	}
}

func TestClassifyListenAndTLS(t *testing.T) {
	listenTests := []struct {
		name   string
		params []string
		extra  bool
		want   AssessmentClass
	}{
		{"extra", []string{"8081"}, true, AssessmentApproximated},
		{"missing", nil, false, AssessmentBlocking},
		{"plain", []string{"8080"}, false, AssessmentSupported},
		{"TLS", []string{"443", "ssl"}, false, AssessmentSupported},
		{"HTTP2", []string{"443", "http2"}, false, AssessmentApproximated},
		{"default server", []string{"443", "default_server"}, false, AssessmentApproximated},
		{"unsupported option", []string{"443", "reuseport"}, false, AssessmentBlocking},
	}
	for _, tt := range listenTests {
		t.Run("listen/"+tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyListen(tt.params, tt.extra), tt.want)
		})
	}

	tlsTests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"empty", nil, AssessmentBlocking},
		{"TLS 1.2", []string{"TLSv1.2"}, AssessmentSupported},
		{"TLS 1.3", []string{"TLSv1.3"}, AssessmentSupported},
		{"legacy", []string{"TLSv1", "TLSv1.2"}, AssessmentApproximated},
		{"unknown", []string{"TLSv9"}, AssessmentBlocking},
	}
	for _, tt := range tlsTests {
		t.Run("TLS/"+tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyTLSProtocols(tt.params), tt.want)
		})
	}
}

func TestClassifyProxyReturnRewriteAndHeader(t *testing.T) {
	proxyTests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"blank", []string{"   "}, AssessmentBlocking},
		{"dynamic", []string{"http://$backend"}, AssessmentBlocking},
		{"URI", []string{"http://backend/api/"}, AssessmentApproximated},
		{"URL host", []string{"http://backend"}, AssessmentSupported},
		{"named upstream", []string{"backend"}, AssessmentSupported},
	}
	for _, tt := range proxyTests {
		t.Run("proxy/"+tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyProxyPass(tt.params), tt.want)
		})
	}
	if proxyPassHasURI("http://backend") || !proxyPassHasURI("https://backend/path") {
		t.Fatal("proxyPassHasURI classified a URL incorrectly")
	}

	returnTests := []struct {
		name        string
		params      []string
		serverLevel bool
		want        AssessmentClass
	}{
		{"malformed", nil, false, AssessmentBlocking},
		{"server", []string{"301", "https://example.test"}, true, AssessmentApproximated},
		{"body", []string{"200", "hello"}, false, AssessmentApproximated},
		{"redirect", []string{"302", "https://example.test"}, false, AssessmentSupported},
		{"implicit redirect", []string{"https://example.test"}, false, AssessmentSupported},
	}
	for _, tt := range returnTests {
		t.Run("return/"+tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyReturn(tt.params, tt.serverLevel), tt.want)
		})
	}

	rewriteTests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing replacement", []string{"^/a"}, AssessmentBlocking},
		{"plain", []string{"^/a", "/b"}, AssessmentSupported},
		{"known flag", []string{"^/a", "/b", "last"}, AssessmentSupported},
		{"unknown flag", []string{"^/a", "/b", "mystery"}, AssessmentApproximated},
	}
	for _, tt := range rewriteTests {
		t.Run("rewrite/"+tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyRewrite(tt.params), tt.want)
		})
	}

	headerTests := []struct {
		name         string
		params       []string
		corsConflict bool
		want         AssessmentClass
	}{
		{"malformed", []string{"X-Test"}, false, AssessmentBlocking},
		{"missing always", []string{"X-Test", "value"}, false, AssessmentBlocking},
		{"dynamic", []string{"X-Test", "$value", "always"}, false, AssessmentBlocking},
		{"CORS conflict", []string{"Access-Control-Allow-Origin", "*", "always"}, true, AssessmentBlocking},
		{"invalid max age", []string{"Access-Control-Max-Age", "later", "always"}, false, AssessmentBlocking},
		{"negative max age", []string{"Access-Control-Max-Age", "-1", "always"}, false, AssessmentBlocking},
		{"static", []string{"X-Frame-Options", "DENY", "always"}, false, AssessmentSupported},
	}
	for _, tt := range headerTests {
		t.Run("header/"+tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyAddHeader(tt.params, tt.corsConflict), tt.want)
		})
	}
}

func TestClassifyUpstreamServer(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"blank", []string{""}, AssessmentBlocking},
		{"weight", []string{"127.0.0.1:8080", "weight=2"}, AssessmentSupported},
		{"bad weight", []string{"127.0.0.1:8080", "weight=0"}, AssessmentBlocking},
		{"down", []string{"127.0.0.1:8080", "down"}, AssessmentApproximated},
		{"unsupported", []string{"127.0.0.1:8080", "backup"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyUpstreamServer(tt.params), tt.want)
		})
	}
}

func TestDynamicDirectiveClassifiers(t *testing.T) {
	locations := []struct {
		name   string
		source string
		want   AssessmentClass
	}{
		{"prefix", `http { server { location / { return 200; } } }`, AssessmentSupported},
		{"exact", `http { server { location = / { return 200; } } }`, AssessmentSupported},
		{"regex", `http { server { location ~ ^/x { return 200; } } }`, AssessmentSupported},
		{"preferential prefix", `http { server { location ^~ /x { return 200; } } }`, AssessmentApproximated},
		{"case-insensitive regex", `http { server { location ~* ^/x { return 200; } } }`, AssessmentApproximated},
		{"named", `http { server { location @fallback { return 200; } } }`, AssessmentBlocking},
	}
	for _, tt := range locations {
		t.Run("location/"+tt.name, func(t *testing.T) {
			d := firstDirectiveNamed(t, tt.source, "location")
			assertCapabilityClass(t, classifyLocation(d), tt.want)
		})
	}

	limit := firstDirectiveNamed(t, `http { server { location / { limit_except GET { deny all; } } } }`, "limit_except")
	assertCapabilityClass(t, classifyLimitExcept(limit, nil), AssessmentBlocking)
	assertCapabilityClass(t, classifyLimitExcept(limit, paramValues(limit)), AssessmentApproximated)
	unsupported := firstDirectiveNamed(t, `http { server { location / { limit_except GET { allow 10.0.0.0/8; deny all; } } } }`, "limit_except")
	assertCapabilityClass(t, classifyLimitExcept(unsupported, paramValues(unsupported)), AssessmentBlocking)

	unknown := firstDirectiveNamed(t, `mystery value;`, "mystery")
	for context, want := range map[AssessmentContext]AssessmentClass{
		ContextEvents:   AssessmentIgnored,
		ContextStream:   AssessmentBlocking,
		ContextMail:     AssessmentBlocking,
		ContextVariable: AssessmentBlocking,
		ContextServer:   AssessmentBlocking,
	} {
		assertCapabilityClass(t, classifyDirective(context, unknown, walkFacts{}), want)
	}
}

func TestAssessmentTraversalHelpers(t *testing.T) {
	contexts := []struct {
		parent AssessmentContext
		name   string
		want   AssessmentContext
		ok     bool
	}{
		{ContextMain, "http", ContextHTTP, true},
		{ContextMain, "events", ContextEvents, true},
		{ContextHTTP, "server", ContextServer, true},
		{ContextStream, "server", ContextStream, true},
		{ContextMail, "server", ContextMail, true},
		{ContextServer, "location", ContextLocation, true},
		{ContextHTTP, "upstream", ContextUpstream, true},
		{ContextLocation, "limit_except", ContextLimitExcept, true},
		{ContextMain, "stream", ContextStream, true},
		{ContextMain, "mail", ContextMail, true},
		{ContextHTTP, "map", ContextVariable, true},
		{ContextLocation, "if", ContextLocation, true},
		{ContextServer, "ordinary", ContextServer, false},
	}
	for _, tt := range contexts {
		got, ok := nestedContext(tt.parent, tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("nestedContext(%q, %q) = (%q, %v), want (%q, %v)", tt.parent, tt.name, got, ok, tt.want, tt.ok)
		}
	}
	if !dHasBlockName("types") || dHasBlockName("proxy_pass") {
		t.Fatal("block-name classification is incorrect")
	}

	risks := map[string]AssessmentRisk{
		"auth_basic":  RiskSecurity,
		"access_log":  RiskObservability,
		"proxy_cache": RiskPerformance,
		"proxy_pass":  RiskRouting,
	}
	for name, want := range risks {
		if got := defaultRisk(name); got != want {
			t.Errorf("defaultRisk(%q) = %q, want %q", name, got, want)
		}
	}

	tree, err := parseString(`
http {
  gzip on;
  upstream app {
    keepalive 10;
    server 127.0.0.1:8080;
  }
  server {
    listen 8080;
    location / {
      add_header Access-Control-Allow-Credentials true always;
      add_header Access-Control-Allow-Origin * always;
    }
  }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	top := orderedDirectives(topLevelDirectives(tree))
	if len(top) != 1 || len(orderedChildren(top[0])) < 3 {
		t.Fatalf("typed HTTP children are incomplete: %+v", top)
	}
	upstream := firstDirectiveNamedFrom(t, top, "upstream")
	if len(orderedChildren(upstream)) != 2 {
		t.Fatalf("typed upstream children are incomplete: %+v", orderedChildren(upstream))
	}
	location := firstDirectiveNamedFrom(t, top, "location")
	if !hasStaticCORSConflict(orderedChildren(location)) {
		t.Fatal("static CORS conflict was not detected")
	}
}

func TestAssessmentUtilityBranches(t *testing.T) {
	var nilAssessment *Assessment
	if nilAssessment.Human() != "" || nilAssessment.HasWarnings() || nilAssessment.HasBlocking() {
		t.Fatal("nil assessment helpers are not safe")
	}
	if _, err := nilAssessment.JSON(); err == nil {
		t.Fatal("nil assessment JSON should fail")
	}
	nilAssessment.SetValidation(nil, nil)

	a := &Assessment{
		SchemaVersion: AssessmentSchemaVersion,
		Source:        "fixture.conf",
		Validation:    AssessmentValidation{Status: "not_run"},
		Results: []AssessmentResult{
			{Class: AssessmentSupported, Severity: AssessmentInfo, Risk: RiskRouting, Context: ContextHTTP, Directive: "server", Code: "TEST_SUPPORTED", Message: "supported"},
			{Class: AssessmentApproximated, Severity: AssessmentWarning, Risk: RiskRouting, Context: ContextLocation, Directive: "alias", Code: "TEST_APPROX", Message: "approx"},
			{Class: AssessmentIgnored, Severity: AssessmentInfo, Risk: RiskPerformance, Context: ContextEvents, Directive: "use", Code: "TEST_IGNORED", Message: "ignored"},
			{Class: AssessmentInformational, Severity: AssessmentInfo, Risk: RiskOperational, Context: ContextMain, Directive: "events", Code: "TEST_INFO", Message: "info"},
		},
	}
	a.finalize()
	if a.Status != "ready_for_review" || !a.Summary.Ready || !a.HasWarnings() {
		t.Fatalf("unexpected summary: status=%s summary=%+v", a.Status, a.Summary)
	}
	a.SetValidation(nil, []config.Diagnostic{{Severity: config.SeverityWarning, Field: "field\nname", Message: "warning\rmessage"}})
	if a.Validation.Status != "valid" || len(a.Validation.Warnings) != 1 {
		t.Fatalf("warnings were not projected: %+v", a.Validation)
	}
	if strings.Contains(a.Validation.Warnings[0].Field, "\n") || strings.Contains(a.Validation.Warnings[0].Message, "\r") {
		t.Fatalf("diagnostic controls were not collapsed: %+v", a.Validation.Warnings[0])
	}

	for _, class := range []AssessmentClass{
		AssessmentParseError,
		AssessmentValidationError,
		AssessmentBlocking,
		AssessmentApproximated,
		AssessmentIgnored,
		AssessmentSupported,
		AssessmentInformational,
	} {
		_ = resultPriority(class)
	}
	if assessmentText(" a\r\nb ") != "a  b" {
		t.Fatalf("unexpected assessmentText result %q", assessmentText(" a\r\nb "))
	}

	synthetic := syntheticResult("SYNTHETIC", AssessmentBlocking, AssessmentError, RiskSecurity, ContextServer, "listen", 7, "message")
	if !synthetic.Synthetic || synthetic.Line != 7 || synthetic.Code != "SYNTHETIC" {
		t.Fatalf("unexpected synthetic result: %+v", synthetic)
	}

	empty := BuildAssessment(nil, "empty.conf", nil)
	if empty == nil || empty.Status != "ready_for_review" || !empty.Summary.Ready {
		t.Fatalf("empty assessment is not stable: %+v", empty)
	}

	invalid := FailureAssessment("fixture.conf", AssessmentInformational, "TEST_INFO", "info")
	invalid.SetValidation([]error{errors.New("bad\nvalue"), errors.New("second")}, nil)
	if invalid.Validation.Status != "invalid" || len(invalid.Validation.Errors) != 2 || invalid.Summary.ValidationErrors != 2 {
		t.Fatalf("unexpected validation projection: %+v %+v", invalid.Validation, invalid.Summary)
	}
	if strings.Contains(invalid.Validation.Errors[0].Message, "\n") {
		t.Fatalf("validation message contains newline: %q", invalid.Validation.Errors[0].Message)
	}
}

func firstDirectiveNamed(t *testing.T, source, name string) ngx.IDirective {
	t.Helper()
	tree, err := parseString(source)
	if err != nil {
		t.Fatalf("parseString: %v", err)
	}
	return firstDirectiveNamedFrom(t, orderedDirectives(topLevelDirectives(tree)), name)
}

func firstDirectiveNamedFrom(t *testing.T, directives []ngx.IDirective, name string) ngx.IDirective {
	t.Helper()
	var visit func([]ngx.IDirective) ngx.IDirective
	visit = func(items []ngx.IDirective) ngx.IDirective {
		for _, directive := range items {
			if directive.GetName() == name {
				return directive
			}
			if found := visit(orderedChildren(directive)); found != nil {
				return found
			}
		}
		return nil
	}
	found := visit(directives)
	if found == nil {
		t.Fatalf("directive %q not found", name)
	}
	return found
}

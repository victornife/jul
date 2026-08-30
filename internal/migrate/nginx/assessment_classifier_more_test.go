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

func TestClassifyRealIPTable(t *testing.T) {
	tests := []struct {
		name      string
		directive string
		params    []string
		class     AssessmentClass
	}{
		{"missing trust source", "set_real_ip_from", nil, AssessmentBlocking},
		{"unix trust source", "set_real_ip_from", []string{"unix:"}, AssessmentBlocking},
		{"invalid trust source", "set_real_ip_from", []string{"not-an-address"}, AssessmentBlocking},
		{"CIDR trust source", "set_real_ip_from", []string{"10.0.0.0/8"}, AssessmentSupported},
		{"missing real IP header", "real_ip_header", nil, AssessmentBlocking},
		{"XFF header", "real_ip_header", []string{"X-Forwarded-For"}, AssessmentSupported},
		{"Forwarded header", "real_ip_header", []string{"Forwarded"}, AssessmentSupported},
		{"X-Real-IP header", "real_ip_header", []string{"X-Real-IP"}, AssessmentBlocking},
		{"recursive off", "real_ip_recursive", []string{"off"}, AssessmentBlocking},
		{"recursive on", "real_ip_recursive", []string{"on"}, AssessmentSupported},
		{"unknown real IP directive", "real_ip_unknown", nil, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyRealIP(tt.directive, tt.params)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestClassifyListenTable(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		extra  bool
		class  AssessmentClass
	}{
		{"extra listen", []string{"8081"}, true, AssessmentApproximated},
		{"missing listen", nil, false, AssessmentBlocking},
		{"plain listen", []string{"8080"}, false, AssessmentSupported},
		{"TLS listen", []string{"443", "ssl"}, false, AssessmentSupported},
		{"implicit HTTP2 option", []string{"443", "http2"}, false, AssessmentApproximated},
		{"default server option", []string{"443", "default_server"}, false, AssessmentApproximated},
		{"unsupported listen option", []string{"443", "reuseport"}, false, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyListen(tt.params, tt.extra)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestClassifyTLSProtocolsTable(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		class  AssessmentClass
	}{
		{"empty", nil, AssessmentBlocking},
		{"TLS 1.2", []string{"TLSv1.2"}, AssessmentSupported},
		{"TLS 1.3", []string{"TLSv1.3"}, AssessmentSupported},
		{"legacy floor", []string{"TLSv1", "TLSv1.2"}, AssessmentApproximated},
		{"unknown", []string{"TLSv9"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyTLSProtocols(tt.params)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestClassifyProxyPassTable(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		class  AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"blank", []string{"   "}, AssessmentBlocking},
		{"dynamic", []string{"http://$backend"}, AssessmentBlocking},
		{"URI rewrite", []string{"http://backend/api/"}, AssessmentApproximated},
		{"URL host", []string{"http://backend"}, AssessmentSupported},
		{"named upstream", []string{"backend"}, AssessmentSupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyProxyPass(tt.params)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
	if proxyPassHasURI("http://backend") {
		t.Fatal("host-only URL reported a URI")
	}
	if !proxyPassHasURI("https://backend/path") {
		t.Fatal("URL path was not detected")
	}
}

func TestClassifyReturnAndRewriteTable(t *testing.T) {
	returnTests := []struct {
		name        string
		params      []string
		serverLevel bool
		class       AssessmentClass
	}{
		{"malformed return", nil, false, AssessmentBlocking},
		{"server return", []string{"301", "https://example.test"}, true, AssessmentApproximated},
		{"response body", []string{"200", "hello"}, false, AssessmentApproximated},
		{"redirect", []string{"302", "https://example.test"}, false, AssessmentSupported},
		{"implicit redirect", []string{"https://example.test"}, false, AssessmentSupported},
	}
	for _, tt := range returnTests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyReturn(tt.params, tt.serverLevel)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}

	rewriteTests := []struct {
		name   string
		params []string
		class  AssessmentClass
	}{
		{"missing replacement", []string{"^/a"}, AssessmentBlocking},
		{"plain rewrite", []string{"^/a", "/b"}, AssessmentSupported},
		{"known flag", []string{"^/a", "/b", "last"}, AssessmentSupported},
		{"unknown flag", []string{"^/a", "/b", "mystery"}, AssessmentApproximated},
	}
	for _, tt := range rewriteTests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyRewrite(tt.params)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestClassifyAddHeaderTable(t *testing.T) {
	tests := []struct {
		name         string
		params       []string
		corsConflict bool
		class        AssessmentClass
	}{
		{"malformed", []string{"X-Test"}, false, AssessmentBlocking},
		{"missing always", []string{"X-Test", "value"}, false, AssessmentBlocking},
		{"dynamic value", []string{"X-Test", "$value", "always"}, false, AssessmentBlocking},
		{"CORS conflict", []string{"Access-Control-Allow-Origin", "*", "always"}, true, AssessmentBlocking},
		{"invalid max age", []string{"Access-Control-Max-Age", "later", "always"}, false, AssessmentBlocking},
		{"negative max age", []string{"Access-Control-Max-Age", "-1", "always"}, false, AssessmentBlocking},
		{"static header", []string{"X-Frame-Options", "DENY", "always"}, false, AssessmentSupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyAddHeader(tt.params, tt.corsConflict)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestClassifyUpstreamServerTable(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		class  AssessmentClass
	}{
		{"missing address", nil, AssessmentBlocking},
		{"blank address", []string{""}, AssessmentBlocking},
		{"valid weight", []string{"127.0.0.1:8080", "weight=2"}, AssessmentSupported},
		{"invalid weight", []string{"127.0.0.1:8080", "weight=0"}, AssessmentBlocking},
		{"down backend", []string{"127.0.0.1:8080", "down"}, AssessmentApproximated},
		{"unsupported flag", []string{"127.0.0.1:8080", "backup"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUpstreamServer(tt.params)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestLocationAndLimitExceptDynamicClassifiers(t *testing.T) {
	locationTests := []struct {
		name   string
		source string
		class  AssessmentClass
	}{
		{"prefix", `http { server { location / { return 200; } } }`, AssessmentSupported},
		{"exact", `http { server { location = / { return 200; } } }`, AssessmentSupported},
		{"regex", `http { server { location ~ ^/x { return 200; } } }`, AssessmentSupported},
		{"preferential prefix", `http { server { location ^~ /x { return 200; } } }`, AssessmentApproximated},
		{"case-insensitive regex", `http { server { location ~* ^/x { return 200; } } }`, AssessmentApproximated},
		{"named location", `http { server { location @fallback { return 200; } } }`, AssessmentBlocking},
	}
	for _, tt := range locationTests {
		t.Run(tt.name, func(t *testing.T) {
			d := firstDirectiveNamed(t, tt.source, "location")
			got := classifyLocation(d)
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}

	limitTests := []struct {
		name   string
		source string
		class  AssessmentClass
	}{
		{"missing methods", `http { server { location / { limit_except { deny all; } } } }`, AssessmentBlocking},
		{"bare denial", `http { server { location / { limit_except GET { deny all; } } } }`, AssessmentApproximated},
		{"unsupported body", `http { server { location / { limit_except GET { allow 10.0.0.0/8; deny all; } } } }`, AssessmentBlocking},
	}
	for _, tt := range limitTests {
		t.Run(tt.name, func(t *testing.T) {
			d := firstDirectiveNamed(t, tt.source, "limit_except")
			got := classifyLimitExcept(d, paramValues(d))
			if got.class != tt.class {
				t.Fatalf("class = %q, want %q: %+v", got.class, tt.class, got)
			}
		})
	}
}

func TestClassifyDirectiveFallbackContexts(t *testing.T) {
	d := firstDirectiveNamed(t, `mystery value;`, "mystery")
	tests := []struct {
		context AssessmentContext
		class   AssessmentClass
	}{
		{ContextEvents, AssessmentIgnored},
		{ContextStream, AssessmentBlocking},
		{ContextMail, AssessmentBlocking},
		{ContextVariable, AssessmentBlocking},
		{ContextServer, AssessmentBlocking},
	}
	for _, tt := range tests {
		got := classifyDirective(tt.context, d, walkFacts{})
		if got.class != tt.class {
			t.Errorf("context %q class = %q, want %q", tt.context, got.class, tt.class)
		}
	}
}

func TestNestedContextAndRiskHelpers(t *testing.T) {
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
			t.Errorf("nestedContext(%q,%q) = (%q,%v), want (%q,%v)", tt.parent, tt.name, got, ok, tt.want, tt.ok)
		}
	}
	if !dHasBlockName("types") || dHasBlockName("proxy_pass") {
		t.Fatal("block-name classification is incorrect")
	}

	risks := map[string]AssessmentRisk{
		"auth_basic": RiskSecurity,
		"access_log": RiskObservability,
		"proxy_cache": RiskPerformance,
		"proxy_pass": RiskRouting,
	}
	for name, want := range risks {
		if got := defaultRisk(name); got != want {
			t.Errorf("defaultRisk(%q) = %q, want %q", name, got, want)
		}
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
	if a.Status != "ready_for_review" || a.Summary.Ready || !a.HasWarnings() {
		// Approximations are review warnings, but not blocking readiness.
		if a.Status != "ready_for_review" || !a.Summary.Ready || !a.HasWarnings() {
			t.Fatalf("unexpected summary: status=%s summary=%+v", a.Status, a.Summary)
		}
	}
	a.SetValidation(nil, []config.Diagnostic{{Severity: config.SeverityWarning, Field: "field\nname", Message: "warning\rmessage"}})
	if a.Validation.Status != "valid" || len(a.Validation.Warnings) != 1 {
		t.Fatalf("warnings were not projected: %+v", a.Validation)
	}
	if strings.Contains(a.Validation.Warnings[0].Field, "\n") || strings.Contains(a.Validation.Warnings[0].Message, "\r") {
		t.Fatalf("diagnostic controls were not collapsed: %+v", a.Validation.Warnings[0])
	}

	classes := []AssessmentClass{AssessmentParseError, AssessmentValidationError, AssessmentBlocking, AssessmentApproximated, AssessmentIgnored, AssessmentSupported, AssessmentInformational}
	for _, class := range classes {
		_ = resultPriority(class)
	}
	if assessmentText(" a\r\nb ") != "a  b" {
		t.Fatalf("unexpected assessmentText result %q", assessmentText(" a\r\nb "))
	}

	s := syntheticResult("SYNTHETIC", AssessmentBlocking, AssessmentError, RiskSecurity, ContextServer, "listen", 7, "message")
	if !s.Synthetic || s.Line != 7 || s.Code != "SYNTHETIC" {
		t.Fatalf("unexpected synthetic result: %+v", s)
	}

	empty := BuildAssessment(nil, "empty.conf", nil)
	if empty == nil || empty.Status != "ready_for_review" || !empty.Summary.Ready {
		t.Fatalf("empty assessment is not stable: %+v", empty)
	}
}

func TestOrderedChildrenAndCORSConflict(t *testing.T) {
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
	if len(top) != 1 {
		t.Fatalf("top-level directives = %d", len(top))
	}
	httpKids := orderedChildren(top[0])
	if len(httpKids) < 3 {
		t.Fatalf("typed HTTP children missing: %d", len(httpKids))
	}
	up := firstDirectiveNamedFrom(t, top, "upstream")
	if len(orderedChildren(up)) != 2 {
		t.Fatalf("typed upstream children missing: %+v", orderedChildren(up))
	}
	loc := firstDirectiveNamedFrom(t, top, "location")
	if !hasStaticCORSConflict(orderedChildren(loc)) {
		t.Fatal("static wildcard/credentials CORS conflict was not detected")
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
		for _, d := range items {
			if d.GetName() == name {
				return d
			}
			if found := visit(orderedChildren(d)); found != nil {
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

func TestAssessmentValidationErrorProjection(t *testing.T) {
	a := FailureAssessment("fixture.conf", AssessmentInformational, "TEST_INFO", "info")
	a.SetValidation([]error{errors.New("bad\nvalue"), errors.New("second")}, nil)
	if a.Validation.Status != "invalid" || len(a.Validation.Errors) != 2 || a.Summary.ValidationErrors != 2 {
		t.Fatalf("unexpected validation projection: %+v %+v", a.Validation, a.Summary)
	}
	if strings.Contains(a.Validation.Errors[0].Message, "\n") {
		t.Fatalf("validation message contains newline: %q", a.Validation.Errors[0].Message)
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"errors"
	"strings"
	"testing"
)

func assessString(t *testing.T, source string) (*Assessment, *Report) {
	t.Helper()
	tree, err := parseString(source)
	if err != nil {
		t.Fatalf("parseString: %v", err)
	}
	_, rep := Translate(tree, "fixture.conf")
	a := BuildAssessment(tree, "fixture.conf", rep)
	if a == nil {
		t.Fatal("assessment is nil")
	}
	return a, rep
}

func TestAssessmentVisitsEveryDirectiveOnce(t *testing.T) {
	a, _ := assessString(t, `
worker_processes 1;
events { worker_connections 128; }
http {
  gzip on;
  upstream app {
    server 127.0.0.1:8080 weight=2;
    keepalive 32;
  }
  server {
    listen 8080;
    server_name example.test;
    location / {
      proxy_pass http://app;
      add_header X-Frame-Options DENY always;
    }
  }
}
`)
	if a.Summary.Total != 14 {
		t.Fatalf("assessment results = %d, want 14: %+v", a.Summary.Total, a.Results)
	}
	seen := map[string]bool{}
	for _, result := range a.Results {
		if seen[result.ID] {
			t.Fatalf("duplicate result ID %q", result.ID)
		}
		seen[result.ID] = true
	}
}

func TestAssessmentIncludesEveryBoundedClass(t *testing.T) {
	a, _ := assessString(t, `
events { worker_connections 128; }
http {
  upstream app {
    server 127.0.0.1:8080;
    keepalive 32;
  }
  server {
    listen 8080;
    location /static {
      alias /srv/static;
      if ($request_method = POST) { return 403; }
    }
  }
}
`)
	classes := map[AssessmentClass]bool{}
	for _, result := range a.Results {
		classes[result.Class] = true
	}
	for _, want := range []AssessmentClass{
		AssessmentSupported,
		AssessmentApproximated,
		AssessmentIgnored,
		AssessmentBlocking,
		AssessmentInformational,
	} {
		if !classes[want] {
			t.Errorf("missing class %q in %+v", want, a.Results)
		}
	}
}

func TestAssessmentJSONIsDeterministic(t *testing.T) {
	source := `http { server { listen 8080; location / { return 200; } } }`
	a1, _ := assessString(t, source)
	a2, _ := assessString(t, source)
	j1, err := a1.JSON()
	if err != nil {
		t.Fatal(err)
	}
	j2, err := a2.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(j1) != string(j2) {
		t.Fatalf("JSON is not deterministic:\n%s\n---\n%s", j1, j2)
	}
}

func TestAssessmentDoesNotEchoDirectiveSecrets(t *testing.T) {
	const secret = "VERY-SECRET-BEARER-TOKEN-123"
	a, _ := assessString(t, `
http {
  server {
    listen 8080;
    location / {
      proxy_set_header Authorization "Bearer VERY-SECRET-BEARER-TOKEN-123";
      add_header X-Private "VERY-SECRET-BEARER-TOKEN-123" always;
    }
  }
}
`)
	jsonOut, err := a.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonOut), secret) {
		t.Fatalf("JSON leaked fixture secret: %s", jsonOut)
	}
	if strings.Contains(a.Human(), secret) {
		t.Fatalf("human report leaked fixture secret: %s", a.Human())
	}
}

func TestAssessmentRealIPMissingHeaderIsBlocking(t *testing.T) {
	a, _ := assessString(t, `
http {
  server {
    listen 8080;
    set_real_ip_from 10.0.0.0/8;
    location / { return 200; }
  }
}
`)
	if !hasAssessmentCode(a, "NGX_REALIP_HEADER_REQUIRED") {
		t.Fatalf("missing explicit-header finding: %+v", a.Results)
	}
	if !a.HasBlocking() {
		t.Fatal("missing real_ip_header must block readiness")
	}
}

func TestAssessmentSupportedRealIPIsReady(t *testing.T) {
	a, _ := assessString(t, `
http {
  server {
    listen 8080;
    set_real_ip_from 10.0.0.0/8;
    real_ip_header X-Forwarded-For;
    real_ip_recursive on;
    location / { return 200; }
  }
}
`)
	for _, result := range a.Results {
		if isRealIPDirective(result.Directive) && result.Class == AssessmentBlocking {
			t.Fatalf("supported real-IP form was blocked: %+v", result)
		}
	}
}

func TestAssessmentCacheZoneConflictIsSynthesized(t *testing.T) {
	a, _ := assessString(t, `
http {
  server {
    listen 8080;
    location / {
      proxy_cache undeclared_zone;
      return 200;
    }
  }
}
`)
	if !hasAssessmentCode(a, "NGX_CACHE_ZONE_CONFLICT") {
		t.Fatalf("expected a synthetic cache-zone-conflict finding: %+v", a.Results)
	}
	if !a.HasBlocking() {
		t.Fatal("an undeclared cache zone must block readiness")
	}
}

func TestAssessmentUpstreamFailoverInconsistencyIsApproximated(t *testing.T) {
	a, _ := assessString(t, `
http {
  upstream pool {
    server a:80 max_fails=2 fail_timeout=5s;
    server b:80 max_fails=5 fail_timeout=9s;
  }
}
`)
	// classifyUpstreamServer reports one result per "server" directive (not
	// one per parameter, matching its existing weight=/down precedent), so
	// with both max_fails and fail_timeout disagreeing across backends,
	// exactly one approximated finding per server surfaces - whichever
	// parameter gonginx's parser yields first (order is not source order;
	// it is not guaranteed to be max_fails before fail_timeout).
	found := 0
	for _, r := range a.Results {
		if r.Code == "NGX_UPSTREAM_SERVER_MAX_FAILS" || r.Code == "NGX_UPSTREAM_SERVER_FAIL_TIMEOUT" {
			if r.Class != AssessmentApproximated {
				t.Errorf("%s: class = %q, want approximated", r.Code, r.Class)
			}
			found++
		}
	}
	if found != 2 {
		t.Fatalf("expected 2 approximated findings (one per server), got %d: %+v", found, a.Results)
	}
}

func TestAssessmentClientAuthUsableThroughFullWalker(t *testing.T) {
	a, _ := assessString(t, `
http {
  server {
    listen 8443 ssl;
    ssl_certificate /fixture/server.pem;
    ssl_certificate_key /fixture/server.key;
    ssl_verify_client on;
    ssl_client_certificate /fixture/ca.pem;
    location / { return 200; }
  }
}
`)
	for _, code := range []string{"NGX_SERVER_CLIENT_AUTH_MODE", "NGX_SERVER_CLIENT_AUTH_CA"} {
		if !hasAssessmentCode(a, code) || a.HasBlocking() {
			t.Fatalf("expected %s supported and no blocking findings, got %+v", code, a.Results)
		}
	}
}

func TestAssessmentGRPCPassAndFastCGIParamThroughFullWalker(t *testing.T) {
	a, _ := assessString(t, `
http {
  server {
    listen 8080;
    location /grpc { grpc_pass grpc://backend:9090; }
    location /fcgi {
      fastcgi_pass 127.0.0.1:9000;
      fastcgi_param SCRIPT_NAME /index.php;
    }
  }
}
`)
	for _, code := range []string{"NGX_LOCATION_GRPC_PASS", "NGX_LOCATION_FASTCGI", "NGX_LOCATION_FASTCGI_PARAM"} {
		if !hasAssessmentCode(a, code) || a.HasBlocking() {
			t.Fatalf("expected %s supported and no blocking findings, got %+v", code, a.Results)
		}
	}
}

func TestAssessmentValidationFailure(t *testing.T) {
	a := FailureAssessment("fixture.conf", AssessmentInformational, "TEST", "test")
	a.SetValidation([]error{errors.New("invalid candidate")}, nil)
	if a.Validation.Status != "invalid" {
		t.Fatalf("validation status = %q", a.Validation.Status)
	}
	if a.Summary.ValidationErrors != 1 || a.Status != "invalid_candidate" {
		t.Fatalf("unexpected validation summary: %+v status=%s", a.Summary, a.Status)
	}
}

func TestFailureAssessmentParseError(t *testing.T) {
	a := FailureAssessment("broken.conf", AssessmentParseError, "NGX_PARSE_ERROR", "NGINX configuration could not be parsed")
	if a.Status != "parse_error" || a.Summary.ParseErrors != 1 || a.Summary.Ready {
		t.Fatalf("unexpected parse assessment: %+v", a)
	}
}

func TestAssessmentHumanListsBlockingFirst(t *testing.T) {
	a, _ := assessString(t, `http { server { listen 8080; location / { alias /srv; if ($x) { return 403; } } } }`)
	human := a.Human()
	blocking := strings.Index(human, "BLOCKING:")
	approximated := strings.Index(human, "APPROXIMATED:")
	if blocking < 0 || approximated < 0 || blocking > approximated {
		t.Fatalf("blocking findings are not first:\n%s", human)
	}
}

func hasAssessmentCode(a *Assessment, code string) bool {
	for _, result := range a.Results {
		if result.Code == code {
			return true
		}
	}
	return false
}

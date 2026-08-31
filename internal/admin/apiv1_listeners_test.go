// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// listenerConfig declares addresses in an order that is deliberately not their
// sorted order, and binds one address from two server blocks.
const listenerConfig = `
[global]
log_level = "info"

[[servers]]
listen = "127.0.0.1:9443"
server_names = ["zeta.example.com"]
  [servers.tls]
  enabled = true
  cert = "/tmp/c.pem"
  key = "/tmp/k.pem"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204

[[servers]]
listen = "127.0.0.1:8080"
server_names = ["alpha.example.com"]
  [servers.client_address]
  trusted_proxies = ["10.0.0.0/8"]
  max_hops = 2
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204

[[servers]]
listen = "127.0.0.1:8080"
server_names = ["beta.example.com"]
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204

[[stream]]
listen = "127.0.0.1:7002"
protocol = "udp"
proxy_pass = "127.0.0.1:7100"
idle_timeout = "45s"

[[stream]]
listen = "127.0.0.1:7001"
tls_passthrough = true
connect_timeout = "3s"
  [stream.sni_routes]
  "zzz.example.com" = "127.0.0.1:7200"
  "aaa.example.com" = "127.0.0.1:7300"
`

func listenerServer(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Parse([]byte(listenerConfig))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:     func() (*config.Config, error) { return cfg, nil },
		StreamCompiled: true,
	})
}

// TestV1ListenersAreInDeclarationOrderNotSorted is the specific defect this
// slice fixes: the internal listener route sorts by address, and the published
// contract must not inherit that sort.
func TestV1ListenersAreInDeclarationOrderNotSorted(t *testing.T) {
	s := listenerServer(t)
	got := decodeInto[adminapi.ListenersResponse](t, getV1(t, s, "/api/v1/listeners", ""))

	if len(got.Listeners) != 2 {
		t.Fatalf("listeners = %d, want 2 distinct addresses: %+v", len(got.Listeners), got.Listeners)
	}
	if got.Listeners[0].Listen != "127.0.0.1:9443" {
		t.Fatalf("first listener = %q, want the first declared 127.0.0.1:9443 — the collection is sorted",
			got.Listeners[0].Listen)
	}
	if got.Listeners[1].Listen != "127.0.0.1:8080" {
		t.Fatalf("second listener = %q", got.Listeners[1].Listen)
	}
	if got.BaseVersion == "" {
		t.Error("the collection carries no base_version")
	}
}

// TestV1ListenerIsTheAddressNotTheServerBlock. Two server blocks share
// 127.0.0.1:8080; the listener is the address, and the names it serves are the
// union in declaration order.
func TestV1ListenerIsTheAddressNotTheServerBlock(t *testing.T) {
	s := listenerServer(t)
	got := decodeInto[adminapi.ListenersResponse](t, getV1(t, s, "/api/v1/listeners", ""))

	shared := got.Listeners[1]
	if shared.ServerBlocks != 2 {
		t.Fatalf("server_blocks = %d, want 2", shared.ServerBlocks)
	}
	want := []string{"alpha.example.com", "beta.example.com"}
	if len(shared.ServerNames) != 2 || shared.ServerNames[0] != want[0] || shared.ServerNames[1] != want[1] {
		t.Fatalf("server_names = %v, want %v in declaration order", shared.ServerNames, want)
	}
	if !shared.ClientAddressConfigured {
		t.Error("a listener with a written trusted-proxy policy does not report it")
	}

	tlsListener := got.Listeners[0]
	if !tlsListener.TLS || tlsListener.ServerBlocks != 1 {
		t.Errorf("tls listener = %+v", tlsListener)
	}
	if tlsListener.ClientAddressConfigured {
		t.Error("a listener with no policy claims one is configured")
	}
}

// TestV1ListenerDoesNotInlineTheTrustedProxyPolicy. The policy has its own
// permission and its own address; inlining it would publish one thing in two
// shapes, free to drift, and would leak a config:read body into a listing.
func TestV1ListenerDoesNotInlineTheTrustedProxyPolicy(t *testing.T) {
	s := listenerServer(t)
	body := getV1(t, s, "/api/v1/listeners", "").Body.String()
	for _, leaked := range []string{"10.0.0.0/8", "trusted_proxies", "max_hops"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the listeners collection inlines the trusted-proxy policy (%q); it is a sub-resource", leaked)
		}
	}
}

func TestV1ListenerClientAddress(t *testing.T) {
	s := listenerServer(t)

	got := decodeInto[adminapi.ClientAddressResponse](t, getV1(t, s, "/api/v1/listeners/127.0.0.1:8080/client_address", ""))
	if got.ClientAddress.Listen != "127.0.0.1:8080" {
		t.Fatalf("listen = %q", got.ClientAddress.Listen)
	}
	if !got.ClientAddress.Configured || got.ClientAddress.MaxHops != 2 {
		t.Fatalf("policy = %+v", got.ClientAddress)
	}
	if len(got.ClientAddress.TrustedProxies) != 1 || got.ClientAddress.TrustedProxies[0] != "10.0.0.0/8" {
		t.Fatalf("trusted_proxies = %v", got.ClientAddress.TrustedProxies)
	}
	if got.BaseVersion == "" {
		t.Error("no base_version")
	}

	// An unbound address is not_found, not an empty default policy — reporting
	// defaults for an address nothing listens on would tell an operator their
	// listener was configured permissively when it does not exist.
	rr := getV1(t, s, "/api/v1/listeners/127.0.0.1:1/client_address", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unbound address = %d, want 404", rr.Code)
	}
	if env := decodeEnvelope(t, rr); env.Error.Details.Kind != "listener" {
		t.Fatalf("details = %+v", env.Error.Details)
	}
}

// TestV1ListenerWithoutAPolicyReportsDefaults. A listener with no written
// policy still has an effective one, and `configured` is what distinguishes
// them — the two are otherwise identical on the wire.
func TestV1ListenerWithoutAPolicyReportsDefaults(t *testing.T) {
	s := listenerServer(t)
	got := decodeInto[adminapi.ClientAddressResponse](t, getV1(t, s, "/api/v1/listeners/127.0.0.1:9443/client_address", ""))

	if got.ClientAddress.Configured {
		t.Error("a listener with no written policy reports configured = true")
	}
	if len(got.ClientAddress.ForwardedHeaders) == 0 {
		t.Error("the effective default headers are not reported")
	}
	if got.ClientAddress.TrustsEveryClient {
		t.Error("a default policy claims it trusts every client")
	}
}

// TestV1StreamsUseMillisecondsNotDurationStrings is ADR 0019 §26.4: a duration
// in a response is an integer in a _ms field, never a Go duration string. A
// client parsing "45s" has to implement Go's duration grammar to read a number.
func TestV1StreamsUseMillisecondsNotDurationStrings(t *testing.T) {
	s := listenerServer(t)
	rr := getV1(t, s, "/api/v1/streams", "")
	body := rr.Body.String()
	for _, goDuration := range []string{`"45s"`, `"3s"`, "connect_timeout\":\"", "idle_timeout\":\""} {
		if strings.Contains(body, goDuration) {
			t.Errorf("a stream publishes a Go duration string (%s): %s", goDuration, body)
		}
	}

	got := decodeInto[adminapi.StreamsResponse](t, rr)
	if got.Streams[0].IdleTimeoutMS != 45000 {
		t.Errorf("idle_timeout_ms = %d, want 45000", got.Streams[0].IdleTimeoutMS)
	}
	if got.Streams[1].ConnectTimeoutMS != 3000 {
		t.Errorf("connect_timeout_ms = %d, want 3000", got.Streams[1].ConnectTimeoutMS)
	}

	// An unwritten timeout reports its effective value, not zero. A client must
	// not have to know this server's defaults to learn when a stream gives up.
	if got.Streams[0].ConnectTimeoutMS != 10000 {
		t.Errorf("defaulted connect_timeout_ms = %d, want the effective 10000", got.Streams[0].ConnectTimeoutMS)
	}
	if got.Streams[1].IdleTimeoutMS != 300000 {
		t.Errorf("defaulted idle_timeout_ms = %d, want the effective 300000", got.Streams[1].IdleTimeoutMS)
	}
}

// TestV1StreamsAreInDeclarationOrder: 7002 is declared before 7001.
func TestV1StreamsAreInDeclarationOrder(t *testing.T) {
	s := listenerServer(t)
	got := decodeInto[adminapi.StreamsResponse](t, getV1(t, s, "/api/v1/streams", ""))

	if len(got.Streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(got.Streams))
	}
	if got.Streams[0].Listen != "127.0.0.1:7002" || got.Streams[1].Listen != "127.0.0.1:7001" {
		t.Fatalf("order = %q, %q; want declaration order", got.Streams[0].Listen, got.Streams[1].Listen)
	}
	if got.Streams[0].Protocol != "udp" {
		t.Errorf("protocol = %q", got.Streams[0].Protocol)
	}
	// The default protocol is resolved, not left empty: a client must not have
	// to know the server's default to read the collection.
	if got.Streams[1].Protocol != "tcp" {
		t.Errorf("defaulted protocol = %q, want the effective tcp", got.Streams[1].Protocol)
	}
}

// TestV1SNIRoutesAreASortedListNotAMap. The keys are operator-chosen, so an
// object with unbounded keys could not be schema-described; and a map has no
// declaration order, so Go's randomized iteration must not be published as one.
func TestV1SNIRoutesAreASortedListNotAMap(t *testing.T) {
	s := listenerServer(t)

	// Sorted, and stable across reads: a client diffing two reads must not see
	// phantom changes.
	for range 8 {
		got := decodeInto[adminapi.StreamsResponse](t, getV1(t, s, "/api/v1/streams", ""))
		routes := got.Streams[1].SNIRoutes
		if len(routes) != 2 {
			t.Fatalf("sni_routes = %d, want 2", len(routes))
		}
		if routes[0].ServerName != "aaa.example.com" || routes[1].ServerName != "zzz.example.com" {
			t.Fatalf("sni_routes not sorted by server name: %+v", routes)
		}
		if routes[0].Target != "127.0.0.1:7300" {
			t.Fatalf("target = %q", routes[0].Target)
		}
	}

	if got := decodeInto[adminapi.StreamsResponse](t, getV1(t, s, "/api/v1/streams", "")); got.Streams[0].SNIRoutes != nil {
		t.Errorf("a stream with no sni_routes publishes %+v", got.Streams[0].SNIRoutes)
	}
}

// TestV1StreamsReportWhetherTheBuildCanServeThem. A declared stream validates
// in a build without the stream proxy, but the process refuses to start — a
// client seeing only the declarations would believe a service was configured
// that this binary cannot serve.
func TestV1StreamsReportWhetherTheBuildCanServeThem(t *testing.T) {
	cfg, err := config.Parse([]byte(listenerConfig))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, compiled := range []bool{true, false} {
		s := newTestServer(t, config.AdminConfig{}, Deps{
			LoadConfig:     func() (*config.Config, error) { return cfg, nil },
			StreamCompiled: compiled,
		})
		got := decodeInto[adminapi.StreamsResponse](t, getV1(t, s, "/api/v1/streams", ""))
		if got.Compiled != compiled {
			t.Errorf("compiled = %v, want %v", got.Compiled, compiled)
		}
		if len(got.Streams) != 2 {
			t.Errorf("a lean build hides the declared streams: %d", len(got.Streams))
		}
	}
}

// TestV1ListenerAndStreamCollectionsReportStorageFailure.
func TestV1ListenerAndStreamCollectionsReportStorageFailure(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	for _, path := range []string{
		"/api/v1/listeners", "/api/v1/listeners/127.0.0.1:8080/client_address", "/api/v1/streams",
	} {
		rr := getV1(t, s, path, "")
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503", path, rr.Code)
			continue
		}
		if env := decodeEnvelope(t, rr); env.Error.Code != adminapi.CodeStorageUnavailable {
			t.Errorf("%s: code = %q", path, env.Error.Code)
		}
	}
}

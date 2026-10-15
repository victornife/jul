// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminoidc

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestRepeatedLoginLogoutReclaimsSessionsAndTransactions(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	runtime, _, client, server := fixture(t)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client.Transport = transport
	client.Timeout = 15 * time.Second
	defer transport.CloseIdleConnections()
	runtime.cfg.MaxSessions = 2
	runtime.cfg.MaxTransactions = 2
	base, _ := url.Parse(server.URL)
	call := func(method, target string, unsafe bool) (int, string) {
		t.Helper()
		if strings.HasPrefix(target, "/") {
			target = server.URL + target
		}
		req, err := http.NewRequest(method, target, nil)
		if err != nil {
			t.Fatal("create synthetic protocol request")
		}
		if unsafe {
			req.Header.Set("Origin", server.URL)
			req.Header.Set("X-Jul-Console", "1")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("synthetic protocol request failed")
		}
		_, err = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal("read synthetic protocol response")
		}
		return response.StatusCode, response.Header.Get("Location")
	}
	previous := ""
	for cycle := 0; cycle < 100; cycle++ {
		status, authorization := call("POST", "/auth/oidc/login", true)
		if status != 303 {
			t.Fatalf("cycle %d: login status %d", cycle, status)
		}
		status, callback := call("GET", authorization, false)
		if status != 303 {
			t.Fatalf("cycle %d: provider status %d", cycle, status)
		}
		status, location := call("GET", callback, false)
		if status != 303 || location != "/" {
			t.Fatalf("cycle %d: callback refused", cycle)
		}
		cookies := client.Jar.Cookies(base)
		if len(cookies) != 1 || cookies[0].Value == previous {
			t.Fatal("session was not freshly minted")
		}
		previous = cookies[0].Value
		if status, _ := call("GET", "/", false); status != 200 {
			t.Fatal("fresh session unavailable")
		}
		if status, _ := call("POST", "/auth/oidc/logout", true); status != 204 {
			t.Fatal("session revocation failed")
		}
		runtime.mu.Lock()
		transactions := len(runtime.transactions)
		runtime.mu.Unlock()
		if transactions != 0 || runtime.sessionCount.Load() != 0 || len(client.Jar.Cookies(base)) != 0 {
			t.Fatal("login/logout retained transaction or session capacity")
		}
		if status, _ := call("GET", "/", false); status != 401 {
			t.Fatal("logged-out browser remained authenticated")
		}
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/fcgi"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/middleware"
)

func TestCGIVaryRealBackendsIsolateIdentities(t *testing.T) {
	for _, protocol := range []string{"fastcgi", "uwsgi"} {
		t.Run(protocol, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			var calls atomic.Int32
			response := func(path, cert, language, preference string) (string, string) {
				calls.Add(1)
				switch path {
				case "/safe":
					return "Accept-Language", language
				case "/alias":
					return "X-Preference", preference
				default:
					return "Client-Cert", cert
				}
			}
			if protocol == "fastcgi" {
				go func() {
					_ = fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						vary, body := response(r.URL.Path, r.Header.Get("Client-Cert"), r.Header.Get("Accept-Language"), r.Header.Get("X-Preference"))
						w.Header().Set("Cache-Control", "public, max-age=60")
						w.Header().Add("Vary", "Accept")
						w.Header().Add("Vary", vary)
						_, _ = io.WriteString(w, body)
					}))
				}()
			} else {
				go func() {
					for {
						conn, err := ln.Accept()
						if err != nil {
							return
						}
						go func() {
							defer conn.Close()
							var hdr [4]byte
							if _, err := io.ReadFull(conn, hdr[:]); err != nil {
								return
							}
							vars := make([]byte, int(binary.LittleEndian.Uint16(hdr[1:3])))
							if _, err := io.ReadFull(conn, vars); err != nil {
								return
							}
							params := decodeUWSGITestVars(vars)
							_, _ = io.Copy(io.Discard, conn)
							vary, body := response(params["REQUEST_URI"], params["HTTP_CLIENT_CERT"], params["HTTP_ACCEPT_LANGUAGE"], params["HTTP_X_PREFERENCE"])
							_, _ = fmt.Fprintf(conn, "Status: 200 OK\r\nCache-Control: public, max-age=60\r\nVary: Accept\r\nVary: %s\r\n\r\n%s", vary, body)
						}()
					}
				}()
			}

			f, cleanup := minimalFactory(t)
			defer cleanup()
			cfgCache := config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20, Lock: true}
			f.Cache, err = cache.New(cfgCache, f.Log)
			if err != nil {
				t.Fatal(err)
			}
			id := "cgi-vary-fixture"
			loc := config.LocationConfig{RouteID: &id, Match: config.MatchConfig{Type: "prefix", Path: "/"}, Cache: true}
			if protocol == "fastcgi" {
				loc.FastCGIPass = ln.Addr().String()
			} else {
				loc.UWSGIPass = ln.Addr().String()
			}
			cfg := &config.Config{Cache: cfgCache, Servers: []config.ServerConfig{{
				Listen: "127.0.0.1:0", Locations: []config.LocationConfig{loc},
			}}}
			if err := config.Validate(cfg); err != nil {
				t.Fatal(err)
			}
			handlers, retire, err := f.Build(context.Background(), cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			if retire != nil {
				defer retire()
			}
			h := handlers["127.0.0.1:0"]
			request := func(path, identity, spoof, alias string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
				r.RequestURI = r.URL.RequestURI()
				r.Header.Set("Accept-Language", "en")
				if spoof != "" {
					r.Header.Set("Client-Cert", spoof)
				}
				if alias != "" {
					r.Header.Set("X_Preference", alias)
				}
				r = r.WithContext(middleware.WithPeerCertIdentity(r.Context(), &middleware.PeerCertIdentity{Verified: true, Raw: []byte(identity)}))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				return rec
			}
			for _, step := range []struct {
				path, identity, spoof, alias, state, body string
			}{
				{"/item", "fixture-a", "", "", "MISS", ":" + base64.StdEncoding.EncodeToString([]byte("fixture-a")) + ":"},
				{"/item", "fixture-b", "", "", "MISS", ":" + base64.StdEncoding.EncodeToString([]byte("fixture-b")) + ":"},
				{"/item", "fixture-a", "same-spoof", "", "MISS", ":" + base64.StdEncoding.EncodeToString([]byte("fixture-a")) + ":"},
				{"/item", "fixture-b", "same-spoof", "", "MISS", ":" + base64.StdEncoding.EncodeToString([]byte("fixture-b")) + ":"},
				{"/safe", "fixture-a", "", "", "MISS", "en"},
				{"/safe", "fixture-b", "", "", "HIT", "en"},
				{"/alias", "fixture-a", "", "", "MISS", ""},
				{"/alias", "fixture-b", "", "alias-value", "MISS", "alias-value"},
			} {
				rec := request(step.path, step.identity, step.spoof, step.alias)
				if rec.Header().Get("X-Cache") != step.state || rec.Body.String() != step.body {
					t.Fatalf("%s: state=%q body=%q; want %q %q", step.path, rec.Header().Get("X-Cache"), rec.Body.String(), step.state, step.body)
				}
			}
			if calls.Load() != 7 {
				t.Fatalf("application calls = %d, want 7", calls.Load())
			}
		})
	}
}

func decodeUWSGITestVars(data []byte) map[string]string {
	vars := make(map[string]string)
	for len(data) >= 2 {
		keyLen := int(binary.LittleEndian.Uint16(data))
		data = data[2:]
		if len(data) < keyLen+2 {
			return vars
		}
		key := string(data[:keyLen])
		data = data[keyLen:]
		valueLen := int(binary.LittleEndian.Uint16(data))
		data = data[2:]
		if len(data) < valueLen {
			return vars
		}
		vars[key] = string(data[:valueLen])
		data = data[valueLen:]
	}
	return vars
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && http3

package plugins

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
)

// On HTTP/3 a client cancel and a forced retirement each end only the
// preparing stream; the QUIC connection keeps serving its other streams.
// Jul applies no read_timeout to HTTP/3 (docs/http3.md), so that bound is
// not tested here.
func TestPreparationHTTP3StreamIsolation(t *testing.T) {
	for _, mode := range []string{"disconnect", "retire"} {
		t.Run(mode, func(t *testing.T) {
			m, hooks := bodyManager(t)
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV2, guestEcho)})
			mux := http.NewServeMux()
			mux.Handle("/p", s.Handler("g"))
			var hits atomic.Int64
			mux.Handle("/other", echoNext(&hits))

			certs := httptest.NewTLSServer(http.NotFoundHandler())
			defer certs.Close()
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			srv := &http3.Server{Handler: mux, TLSConfig: certs.TLS}
			served := make(chan struct{})
			go func() { defer close(served); _ = srv.Serve(pc) }()
			defer func() { _ = srv.Close(); <-served }()
			tr := &http3.Transport{TLSClientConfig: certs.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
			defer tr.Close()
			c := &http.Client{Transport: tr, Timeout: 10 * time.Second}
			base := "https://" + pc.LocalAddr().String()

			if r := post(c, base+"/other", "warm"); r.err != nil || r.proto != 3 {
				t.Fatalf("h3 warm-up: %+v", r)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pw, res := startUpload(ctx, c, base+"/p")
			defer pw.Close()
			_, _ = pw.Write([]byte("partial"))
			waitFor(t, "h3 upload preparing", func() bool { return preparing(s.plugins["g"]) == 1 })
			if r := post(c, base+"/other", "sibling"); r.err != nil || r.status != 200 || r.body != "sibling" {
				t.Fatalf("sibling stream during preparation: %+v", r)
			}
			want := bodyFailCanceled
			if mode == "retire" {
				want = bodyFailRetired
				_ = s.Close()
			} else {
				cancel()
			}
			// A QUIC stream reset can reach the body reader before the request
			// context ends, so a client cancel may be attributed read_error.
			if reason := hooks.awaitFail(t); reason != want && (mode == "retire" || reason != bodyFailReadError) {
				t.Fatalf("reason = %s, want %s", reason, want)
			}
			<-res
			if n, _, _, panics, _ := hooks.snapshot(); n != 0 || panics != 0 {
				t.Fatalf("guest invoked=%d panics=%d", n, panics)
			}
			if r := post(c, base+"/other", "after"); r.err != nil || r.status != 200 || r.proto != 3 {
				t.Fatalf("h3 connection after the stream ended: %+v", r)
			}
		})
	}
}

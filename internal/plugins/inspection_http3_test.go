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

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func h3Server(t *testing.T, h http.Handler) (*http.Client, string, func() bool) {
	certs := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certs.Close)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// sameConn counts the QUIC connections the server accepted, so a
	// transport that silently redialled cannot pass for connection reuse.
	var conns atomic.Int64
	srv := &http3.Server{Handler: h, TLSConfig: certs.TLS,
		ConnContext: func(ctx context.Context, _ *quic.Conn) context.Context {
			conns.Add(1)
			return ctx
		}}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(pc) }()
	tr := &http3.Transport{TLSClientConfig: certs.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	t.Cleanup(func() { _ = tr.Close(); _ = srv.Close(); <-served; _ = pc.Close() })
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}, "https://" + pc.LocalAddr().String(),
		func() bool { return conns.Load() == 1 }
}

func TestInspectionOutcomesOverH3(t *testing.T) {
	runInspectionOutcomes(t, "h3", 3, h3Server)
}

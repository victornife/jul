// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && http3

package plugins

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func h3Server(t *testing.T, h http.Handler) (*http.Client, string, func() bool) {
	certs := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certs.Close)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http3.Server{Handler: h, TLSConfig: certs.TLS}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(pc) }()
	tr := &http3.Transport{TLSClientConfig: certs.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	t.Cleanup(func() { _ = tr.Close(); _ = srv.Close(); <-served; _ = pc.Close() })
	// Every result is checked for proto 3; a stream deadline that reset the
	// QUIC connection would fail the sibling and follow-up requests.
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}, "https://" + pc.LocalAddr().String(), func() bool { return true }
}

func TestInspectionOutcomesOverH3(t *testing.T) {
	runInspectionOutcomes(t, "h3", 3, h3Server)
}

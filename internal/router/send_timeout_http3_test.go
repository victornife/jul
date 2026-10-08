// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package router

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
)

func TestSendTimeoutHTTP3(t *testing.T) {
	for _, progressing := range []bool{false, true} {
		name := "stalled"
		if progressing {
			name = "progressing"
		}
		t.Run(name, func(t *testing.T) {
			result := make(chan error, 1)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				controller := http.NewResponseController(writer)
				writer.WriteHeader(http.StatusOK)
				if err := controller.Flush(); err != nil {
					result <- err
					return
				}
				if progressing {
					ticker := time.NewTicker(100 * time.Millisecond)
					defer ticker.Stop()
					for event := 0; event < 10; event++ {
						select {
						case <-request.Context().Done():
							return
						case <-ticker.C:
						}
						if _, err := io.WriteString(writer, "event\n"); err != nil {
							result <- err
							return
						}
						if err := controller.Flush(); err != nil {
							result <- err
							return
						}
					}
					result <- nil
					return
				}
				chunk := []byte(strings.Repeat("x", 32*1024))
				for request.Context().Err() == nil {
					if _, err := writer.Write(chunk); err != nil {
						result <- err
						return
					}
					if err := controller.Flush(); err != nil {
						result <- err
						return
					}
				}
				result <- request.Context().Err()
			})
			configuration := &config.Config{Servers: []config.ServerConfig{{Listen: "test", SendTimeout: config.Duration(400 * time.Millisecond), Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "."}}}}}
			router, err := New(configuration, map[string]Builder{ActionStatic: func(config.ServerConfig, config.LocationConfig) (http.Handler, error) { return handler, nil }}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			certificateServer := httptest.NewTLSServer(http.NotFoundHandler())
			defer certificateServer.Close()
			connection, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			server := &http3.Server{Handler: router.For("test"), TLSConfig: certificateServer.TLS}
			defer server.Close()
			serveDone := make(chan struct{})
			go func() { defer close(serveDone); _ = server.Serve(connection) }()
			defer func() { _ = server.Close(); <-serveDone }()
			transport := &http3.Transport{
				TLSClientConfig: certificateServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
				QUICConfig:      &quic.Config{InitialStreamReceiveWindow: 64 * 1024, MaxStreamReceiveWindow: 64 * 1024},
			}
			defer transport.Close()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			response, err := client.Get("https://" + connection.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.ProtoMajor != 3 {
				t.Fatalf("protocol=%s, want HTTP/3", response.Proto)
			}
			if progressing {
				body, err := io.ReadAll(response.Body)
				if err != nil || strings.Count(string(body), "event\n") != 10 {
					t.Fatalf("progressing HTTP/3 response cut: body=%q err=%v", body, err)
				}
			}
			select {
			case err := <-result:
				if progressing != (err == nil) {
					t.Fatalf("progressing=%v write result=%v", progressing, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("HTTP/3 writer did not finish within the timeout guard")
			}
		})
	}
}

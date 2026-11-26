// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func TestHTTP2ExtendedConnectStatusMatchesAdvertisement(t *testing.T) {
	if os.Getenv("JUL_TEST_H2_STATUS") == "1" {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.EnableHTTP2 = true
		server.StartTLS()
		defer server.Close()
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp",
			strings.TrimPrefix(server.URL, "https://"),
			&tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
			t.Fatal(err)
		}
		framer := http2.NewFramer(conn, conn)
		if err := framer.WriteSettings(); err != nil {
			t.Fatal(err)
		}
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		settings, ok := frame.(*http2.SettingsFrame)
		if !ok {
			t.Fatalf("first frame = %T, want SETTINGS", frame)
		}
		var advertised bool
		if err := settings.ForeachSetting(func(setting http2.Setting) error {
			if setting.ID == http2.SettingEnableConnectProtocol && setting.Val == 1 {
				advertised = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		expected, err := strconv.ParseBool(os.Getenv("JUL_TEST_H2_EXPECT"))
		if err != nil {
			t.Fatal(err)
		}
		if got := HTTP2ExtendedConnectEnabled(); got != advertised || got != expected {
			t.Fatalf("Status=%t, advertised=%t, expected=%t, GODEBUG=%q", got, advertised, expected, os.Getenv("GODEBUG"))
		}
		t.Setenv("GODEBUG", "http2xconnect=0")
		if got := HTTP2ExtendedConnectEnabled(); got != advertised {
			t.Fatalf("Status changed after startup: %t, advertised=%t", got, advertised)
		}
		return
	}
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"http2xconnect=0", false},
		{"http2xconnect=1,http2xconnect=0", true},
		{"http2xconnect=0,http2xconnect=1", true},
		{"http2xconnect=10", true},
		{"http2xconnect =1", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHTTP2ExtendedConnectStatusMatchesAdvertisement$")
			cmd.Env = append(os.Environ(), "JUL_TEST_H2_STATUS=1", "JUL_TEST_H2_EXPECT="+strconv.FormatBool(tc.want), "GODEBUG="+tc.value)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("subprocess: %v\n%s", err, output)
			}
		})
	}
	startup := HTTP2ExtendedConnectEnabled()
	t.Setenv("GODEBUG", "http2xconnect=1")
	if got := HTTP2ExtendedConnectEnabled(); got != startup {
		t.Fatalf("Status changed after startup: %t, was %t", got, startup)
	}
}

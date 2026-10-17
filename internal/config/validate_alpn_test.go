// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestValidateStreamALPNRoutes(t *testing.T) {
	valid := StreamALPNRoute{ServerName: "shared.example", Protocols: []string{"h2", "x,h2", "\x00", "é"}, ProxyPass: "127.0.0.1:443"}
	if err := ValidateStreamALPNRoutes([]StreamALPNRoute{valid}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStreamALPNRoutes(nil); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*StreamALPNRoute)
	}{
		{"missing host", func(r *StreamALPNRoute) { r.ServerName = "" }},
		{"long host", func(r *StreamALPNRoute) { r.ServerName = strings.Repeat("x", 256) }},
		{"partial wildcard", func(r *StreamALPNRoute) { r.ServerName = "*.example" }},
		{"space host", func(r *StreamALPNRoute) { r.ServerName = " shared.example" }},
		{"non ASCII host", func(r *StreamALPNRoute) { r.ServerName = "é.example" }},
		{"missing offers", func(r *StreamALPNRoute) { r.Protocols = nil }},
		{"too many offers", func(r *StreamALPNRoute) { r.Protocols = make([]string, 65) }},
		{"empty offer", func(r *StreamALPNRoute) { r.Protocols = []string{""} }},
		{"duplicate offer", func(r *StreamALPNRoute) { r.Protocols = []string{"h2", "h2"} }},
		{"long byte length", func(r *StreamALPNRoute) { r.Protocols = []string{strings.Repeat("é", 128)} }},
		{"invalid UTF8", func(r *StreamALPNRoute) { r.Protocols = []string{"\xff"} }},
		{"missing target", func(r *StreamALPNRoute) { r.ProxyPass = " " }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := valid
			tt.mutate(&r)
			if err := ValidateStreamALPNRoutes([]StreamALPNRoute{r}); err == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
	max := make([]StreamALPNRoute, StreamALPNMaxRules)
	for i := range max {
		max[i] = valid
	}
	if err := ValidateStreamALPNRoutes(max); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStreamALPNRoutes(append(max, valid)); err == nil {
		t.Fatal("too many rules accepted")
	}
	valid.ServerName = "*"
	valid.Protocols = []string{strings.Repeat("x", 255)}
	if err := ValidateStreamALPNRoutes([]StreamALPNRoute{valid}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamALPNValidationAndRoundtrip(t *testing.T) {
	st := StreamServer{Listen: ":443", ALPNRoutes: []StreamALPNRoute{{ServerName: "*", Protocols: []string{"h2", "x,h2", "\x00"}, ProxyPass: "127.0.0.1:9443"}}}
	if errs := validateStreams([]StreamServer{st}, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	wire, err := toml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StreamServer
	if err = toml.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.ALPNRoutes, decoded.ALPNRoutes) {
		t.Fatalf("roundtrip changed rules: %+v", decoded)
	}
	st.Protocol = "udp"
	if errs := validateStreams([]StreamServer{st}, nil); len(errs) == 0 {
		t.Fatal("ALPN on UDP accepted")
	}
	st.Protocol = "tcp"
	st.ALPNRoutes[0].ServerName = ""
	if errs := validateStreams([]StreamServer{st}, nil); len(errs) == 0 {
		t.Fatal("invalid rules accepted")
	}
	st.ALPNRoutes[0].ServerName = "*"
	st.ALPNRoutes[0].ProxyPass = "missing"
	if errs := validateStreams([]StreamServer{st}, nil); len(errs) == 0 {
		t.Fatal("unknown upstream accepted")
	}
	if got := streamTargets(st); !reflect.DeepEqual(got, []string{"missing"}) {
		t.Fatal(got)
	}
	st.ALPNRoutes[0].ProxyPass = " "
	if got := streamTargets(st); len(got) != 0 {
		t.Fatal(got)
	}
}

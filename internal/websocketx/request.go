// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package websocketx

import (
	"context"
	"net/http"
	"strings"
)

func ExtendedProtocol(r *http.Request) (string, bool) {
	if r == nil || r.Method != http.MethodConnect {
		return "", false
	}
	switch r.ProtoMajor {
	case 2:
		values := r.Header[":protocol"]
		if len(values) == 0 {
			return "", false
		}
		return values[0], true
	case 3:
		if r.Proto == "HTTP/3.0" {
			return "", false
		}
		return r.Proto, true
	default:
		return "", false
	}
}

func ValidExtendedRequest(r *http.Request) bool {
	if protocol, ok := ExtendedProtocol(r); !ok || !strings.EqualFold(protocol, "websocket") {
		return false
	}
	if r.ProtoMajor == 2 && len(r.Header[":protocol"]) != 1 {
		return false
	}
	if r.URL == nil || !strings.HasPrefix(r.URL.Path, "/") || r.Host == "" {
		return false
	}
	for _, name := range []string{
		"Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Accept",
	} {
		if len(r.Header.Values(name)) != 0 {
			return false
		}
	}
	versions := r.Header.Values("Sec-WebSocket-Version")
	return len(versions) == 1 && strings.TrimSpace(versions[0]) == "13"
}

type extendedRequestKey struct{}

func AsApplicationRequest(r *http.Request) *http.Request {
	ctx := context.WithValue(r.Context(), extendedRequestKey{}, true)
	req := r.Clone(ctx)
	req.Method = http.MethodGet
	return req
}

func IsExtendedRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	extended, _ := r.Context().Value(extendedRequestKey{}).(bool)
	return extended
}

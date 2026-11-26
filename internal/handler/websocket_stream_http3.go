// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/quic-go/quic-go/http3"
)

func extendedFrontendStream(w http.ResponseWriter, r *http.Request, controller *http.ResponseController) (io.ReadWriteCloser, error) {
	if r.ProtoMajor == 2 {
		return &extendedWebSocketFrontend{reader: r.Body, writer: w, controller: controller}, nil
	}
	if r.ProtoMajor != 3 {
		return nil, errors.New("WebSocket Extended CONNECT requires HTTP/2 or HTTP/3")
	}
	for current := w; current != nil; {
		if stream, ok := current.(http3.HTTPStreamer); ok {
			fullDuplex, err := stream.HTTPStream()
			if err != nil {
				return nil, err
			}
			return &extendedWebSocketFrontend{
				reader: fullDuplex, writer: fullDuplex, controller: controller,
				closeFn: fullDuplex.Close,
			}, nil
		}
		unwrapper, ok := current.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		current = unwrapper.Unwrap()
	}
	return nil, errors.New("HTTP/3 response writer does not expose its stream")
}

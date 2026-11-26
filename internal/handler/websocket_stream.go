// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !http3

package handler

import (
	"errors"
	"io"
	"net/http"
)

func extendedFrontendStream(w http.ResponseWriter, r *http.Request, controller *http.ResponseController) (io.ReadWriteCloser, error) {
	if r.ProtoMajor != 2 {
		return nil, errors.New("WebSocket Extended CONNECT requires HTTP/2 or HTTP/3")
	}
	return &extendedWebSocketFrontend{
		reader: r.Body, writer: w, controller: controller,
	}, nil
}

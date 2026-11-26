// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"jul/internal/respwriter"
	"jul/internal/websocketx"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func (h *proxyHandler) serveExtendedWebSocket(w http.ResponseWriter, r *http.Request) {
	if !websocketx.IsExtendedRequest(r) && !websocketx.ValidExtendedRequest(r) {
		http.Error(w, "invalid WebSocket Extended CONNECT handshake", http.StatusBadRequest)
		return
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		http.Error(w, "WebSocket handshake could not be initialized", http.StatusInternalServerError)
		return
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	out := r.Clone(r.Context())
	out.Method = http.MethodGet
	out.Header = r.Header.Clone()
	out.Body = nil
	out.GetBody = nil
	out.ContentLength = 0
	out.RequestURI = ""
	removeWebSocketHopHeaders(out.Header)
	out.Header.Del("Sec-WebSocket-Key")
	out.Header.Del("Sec-WebSocket-Accept")
	out.Header.Set("Connection", "Upgrade")
	out.Header.Set("Upgrade", "websocket")
	out.Header.Set("Sec-WebSocket-Key", key)
	out.Header.Set("Sec-WebSocket-Version", "13")

	pr := &httputil.ProxyRequest{In: r, Out: out}
	if h.Rewrite == nil {
		http.Error(w, "WebSocket proxy route is unavailable", http.StatusBadGateway)
		return
	}
	h.Rewrite(pr)
	out = pr.Out
	response, err := h.Transport.RoundTrip(out)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			if h.ErrorHandler != nil {
				h.ErrorHandler(w, r, err)
			} else {
				http.Error(w, "upstream WebSocket handshake failed", http.StatusBadGateway)
			}
		}
		return
	}
	if response.StatusCode != http.StatusSwitchingProtocols || !validBackendWebSocketResponse(response, key) {
		_ = response.Body.Close()
		if h.ErrorHandler != nil {
			h.ErrorHandler(w, r, fmt.Errorf("upstream did not accept a valid WebSocket handshake"))
		} else {
			http.Error(w, "upstream did not accept a valid WebSocket handshake", http.StatusBadGateway)
		}
		return
	}
	if !validSelectedSubprotocol(r.Header, response.Header) {
		_ = response.Body.Close()
		http.Error(w, "upstream selected an unoffered WebSocket subprotocol", http.StatusBadGateway)
		return
	}
	if !validSelectedExtensions(r.Header, response.Header) {
		_ = response.Body.Close()
		http.Error(w, "upstream selected an unoffered WebSocket extension", http.StatusBadGateway)
		return
	}
	backend, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		_ = response.Body.Close()
		http.Error(w, "upstream WebSocket stream is unavailable", http.StatusBadGateway)
		return
	}
	backend = respwriter.AttachUpgradedBackend(r.Context(), backend, response.Header)
	copyWebSocketResponseHeaders(w.Header(), response.Header.Clone())
	controller := http.NewResponseController(w)
	if r.ProtoMajor == 2 {
		if err := controller.EnableFullDuplex(); err != nil {
			_ = backend.Close()
			http.Error(w, "HTTP/2 WebSocket full-duplex mode is unavailable", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		_ = backend.Close()
		if h.logger != nil {
			h.logger.Error("proxy WebSocket response flush failed", "path", r.URL.Path, "error", err)
		}
		return
	}
	front, err := extendedFrontendStream(w, r, controller)
	if err != nil {
		_ = backend.Close()
		if h.logger != nil {
			h.logger.Error("proxy WebSocket stream setup failed", "path", r.URL.Path, "error", err)
		}
		return
	}
	front = respwriter.AttachExtendedStream(r.Context(), front, backend)
	if err := relayExtendedWebSocket(front, backend); err != nil && h.logger != nil {
		h.logger.Error("proxy WebSocket tunnel ended with an error", "path", r.URL.Path, "error", err)
	}
}

func validBackendWebSocketResponse(response *http.Response, key string) bool {
	if response == nil || response.StatusCode != http.StatusSwitchingProtocols {
		return false
	}
	connectionUpgrade := false
	for _, value := range response.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				connectionUpgrade = true
			}
		}
	}
	upgrade := response.Header.Values("Upgrade")
	accept := response.Header.Values("Sec-WebSocket-Accept")
	if !connectionUpgrade || len(upgrade) != 1 || !strings.EqualFold(strings.TrimSpace(upgrade[0]), "websocket") || len(accept) != 1 {
		return false
	}
	sum := sha1.Sum([]byte(key + websocketGUID))
	want := base64.StdEncoding.EncodeToString(sum[:])
	return strings.TrimSpace(accept[0]) == want
}

func validSelectedSubprotocol(request, response http.Header) bool {
	selected := response.Values("Sec-WebSocket-Protocol")
	if len(selected) == 0 {
		return true
	}
	if len(selected) != 1 {
		return false
	}
	selectedToken := strings.TrimSpace(selected[0])
	if selectedToken == "" || strings.ContainsAny(selectedToken, " ,\t") {
		return false
	}
	for _, value := range request.Values("Sec-WebSocket-Protocol") {
		for _, offered := range strings.Split(value, ",") {
			if strings.TrimSpace(offered) == selectedToken {
				return true
			}
		}
	}
	return false
}

func validSelectedExtensions(request, response http.Header) bool {
	selected := response.Values("Sec-WebSocket-Extensions")
	if len(selected) == 0 {
		return true
	}
	offeredNames, ok := websocketExtensionNames(request.Values("Sec-WebSocket-Extensions"))
	if !ok {
		return false
	}
	selectedNames, ok := websocketExtensionNames(selected)
	if !ok {
		return false
	}
	for name := range selectedNames {
		if _, offered := offeredNames[name]; !offered {
			return false
		}
	}
	return true
}

func websocketExtensionNames(values []string) (map[string]struct{}, bool) {
	names := make(map[string]struct{})
	for _, value := range values {
		start, quoted, escaped := 0, false, false
		for i := 0; i <= len(value); i++ {
			if i < len(value) {
				switch value[i] {
				case '\\':
					if quoted && !escaped {
						escaped = true
						continue
					}
				case '"':
					if !escaped {
						quoted = !quoted
					}
				case ',':
					if !quoted {
						if !addWebSocketExtensionName(names, value[start:i]) {
							return nil, false
						}
						start = i + 1
					}
				}
				escaped = false
				continue
			}
			if quoted || escaped || !addWebSocketExtensionName(names, value[start:i]) {
				return nil, false
			}
		}
	}
	return names, true
}

func addWebSocketExtensionName(names map[string]struct{}, extension string) bool {
	name, _, _ := strings.Cut(strings.TrimSpace(extension), ";")
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isWebSocketTokenByte(name[i]) {
			return false
		}
	}
	names[strings.ToLower(name)] = struct{}{}
	return true
}

func isWebSocketTokenByte(char byte) bool {
	return char >= '0' && char <= '9' ||
		char >= 'A' && char <= 'Z' ||
		char >= 'a' && char <= 'z' ||
		strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char))
}

func copyWebSocketResponseHeaders(dst, src http.Header) {
	removeWebSocketHopHeaders(src)
	for name, values := range src {
		if strings.EqualFold(name, "Sec-WebSocket-Accept") ||
			strings.EqualFold(name, "Sec-WebSocket-Key") ||
			strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func removeWebSocketHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name)); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Proxy-Connection", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
	for name := range header {
		if strings.HasPrefix(name, ":") {
			delete(header, name)
		}
	}
}

type extendedWebSocketFrontend struct {
	reader     io.Reader
	writer     io.Writer
	controller *http.ResponseController
	closeFn    func() error
	writeMu    sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func (s *extendedWebSocketFrontend) Read(p []byte) (int, error) {
	return s.reader.Read(p)
}

func (s *extendedWebSocketFrontend) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return n, err
	}
	if err := s.controller.Flush(); err != nil {
		return n, err
	}
	return n, nil
}

func (s *extendedWebSocketFrontend) Close() error {
	s.closeOnce.Do(func() {
		now := time.Now()
		_ = s.controller.SetReadDeadline(now)
		_ = s.controller.SetWriteDeadline(now)
		if s.closeFn != nil {
			s.closeErr = s.closeFn()
		}
	})
	return s.closeErr
}

func (s *extendedWebSocketFrontend) SetWriteDeadline(deadline time.Time) error {
	if s.controller != nil {
		return s.controller.SetWriteDeadline(deadline)
	}
	if setter, ok := s.writer.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return setter.SetWriteDeadline(deadline)
	}
	return http.ErrNotSupported
}

func relayExtendedWebSocket(front, backend io.ReadWriteCloser) error {
	type copyResult struct {
		err error
	}
	done := make(chan copyResult, 2)
	go func() {
		_, err := io.Copy(backend, front)
		done <- copyResult{err: err}
	}()
	go func() {
		_, err := io.Copy(front, backend)
		done <- copyResult{err: err}
	}()
	first := <-done
	_ = backend.Close()
	_ = front.Close()
	select {
	case second := <-done:
		for _, err := range []error{first.err, second.err} {
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) &&
				!errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	case <-time.After(2 * time.Second):
		return errors.New("the other WebSocket tunnel direction did not stop after close")
	}
	return nil
}

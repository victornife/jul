// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package handler

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/upstream"
)

const websocketHTTP3IdleTimeout = 90 * time.Second

type websocketHTTP3Transport struct {
	mu             sync.Mutex
	closed         bool
	connections    map[string]*websocketHTTP3Connection
	tlsConfig      *tls.Config
	pool           *upstream.Pool
	connectTimeout time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration
}

type websocketHTTP3Connection struct {
	address    string
	ready      chan struct{}
	conn       *quic.Conn
	client     *http3.ClientConn
	err        error
	dialCancel context.CancelFunc
	active     int
	idleTimer  *time.Timer
}

func newHTTP3WebSocketUpstreamTransport(loc config.LocationConfig, policy *backendtls.Policy, pool *upstream.Pool) (websocketUpstreamTransport, error) {
	connectTimeout := loc.ProxyConnectTimeout.Std()
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	tlsConfig := policy.ClientConfig()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{http3.NextProtoH3}
	return &websocketHTTP3Transport{
		connections:    make(map[string]*websocketHTTP3Connection),
		tlsConfig:      tlsConfig,
		pool:           pool,
		connectTimeout: connectTimeout,
		readTimeout:    loc.ProxyReadTimeout.Std(),
		writeTimeout:   loc.ProxySendTimeout.Std(),
	}, nil
}

func (t *websocketHTTP3Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isExtendedWebSocketUpstreamRequest(req) {
		return nil, errors.New("HTTP/3 WebSocket upstream requires Extended CONNECT")
	}
	if req.URL == nil || req.URL.Scheme != "https" || req.URL.Host == "" {
		return nil, errors.New("HTTP/3 WebSocket upstream requires an https backend")
	}
	if req.Body != nil && req.Body != http.NoBody {
		return nil, errors.New("HTTP/3 WebSocket handshake must not carry a request body")
	}

	connection, release, err := t.acquire(req.Context(), req.URL.Host)
	if err != nil {
		return nil, err
	}
	stream, err := connection.client.OpenRequestStream(req.Context())
	if err != nil {
		if req.Context().Err() == nil {
			t.evict(connection)
			// GOAWAY can refuse new streams without interrupting active tunnels.
			if connection.conn.Context().Err() == nil && !errors.Is(err, net.ErrClosed) {
				err = fmt.Errorf("%w: %w", errWebSocketH3StreamUnavailable, err)
			}
		}
		release()
		return nil, err
	}
	body := &websocketHTTP3Body{
		stream:       stream,
		release:      release,
		readTimeout:  t.readTimeout,
		writeTimeout: t.writeTimeout,
		done:         make(chan struct{}),
	}
	go body.cancelOnContext(req.Context())
	if t.writeTimeout > 0 {
		if err := stream.SetWriteDeadline(time.Now().Add(t.writeTimeout)); err != nil {
			_ = body.Close()
			return nil, err
		}
	}
	if err := stream.SendRequestHeader(req); err != nil {
		_ = body.Close()
		return nil, err
	}
	if t.writeTimeout > 0 {
		if err := stream.SetWriteDeadline(time.Time{}); err != nil {
			_ = body.Close()
			return nil, err
		}
	}
	if t.readTimeout > 0 {
		if err := stream.SetReadDeadline(time.Now().Add(t.readTimeout)); err != nil {
			_ = body.Close()
			return nil, err
		}
	}
	response, err := stream.ReadResponse()
	if err != nil {
		_ = body.Close()
		return nil, err
	}
	if t.readTimeout > 0 {
		if err := stream.SetReadDeadline(time.Time{}); err != nil {
			_ = body.Close()
			return nil, err
		}
	}
	response.Request = req
	response.Body = body
	return response, nil
}

func (t *websocketHTTP3Transport) acquire(ctx context.Context, address string) (*websocketHTTP3Connection, func(), error) {
	for {
		connection, err := t.connection(ctx, address)
		if err != nil {
			return nil, nil, err
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, nil, errors.New("HTTP/3 WebSocket upstream is closed")
		}
		if t.connections[address] != connection || connection.conn.Context().Err() != nil {
			t.mu.Unlock()
			continue
		}
		if connection.idleTimer != nil {
			connection.idleTimer.Stop()
			connection.idleTimer = nil
		}
		connection.active++
		t.mu.Unlock()

		var once sync.Once
		return connection, func() {
			once.Do(func() { t.release(connection) })
		}, nil
	}
}

func (t *websocketHTTP3Transport) connection(ctx context.Context, address string) (*websocketHTTP3Connection, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("HTTP/3 WebSocket upstream is closed")
	}
	if connection := t.connections[address]; connection != nil {
		ready := connection.ready
		t.mu.Unlock()
		select {
		case <-ready:
			if connection.err != nil {
				return nil, connection.err
			}
			return connection, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}

	dialCtx, cancel := context.WithTimeout(ctx, t.connectTimeout)
	connection := &websocketHTTP3Connection{
		address:    address,
		ready:      make(chan struct{}),
		dialCancel: cancel,
	}
	t.connections[address] = connection
	t.mu.Unlock()

	t.dial(dialCtx, connection)
	cancel()
	return connection, connection.err
}

func (t *websocketHTTP3Transport) dial(ctx context.Context, connection *websocketHTTP3Connection) {
	quicConn, err := quic.DialAddr(ctx, connection.address, t.tlsConfig, &quic.Config{
		HandshakeIdleTimeout: t.connectTimeout,
		MaxIdleTimeout:       2 * time.Minute,
		KeepAlivePeriod:      20 * time.Second,
	})
	if err == nil {
		if t.pool != nil {
			release := t.pool.TrackConn()
			go func() {
				<-quicConn.Context().Done()
				release()
			}()
		}
		if quicConn.ConnectionState().TLS.NegotiatedProtocol != http3.NextProtoH3 {
			err = fmt.Errorf("backend negotiated %q instead of HTTP/3", quicConn.ConnectionState().TLS.NegotiatedProtocol)
		}
	}
	if err == nil {
		client := (&http3.Transport{}).NewClientConn(quicConn)
		select {
		case <-client.ReceivedSettings():
			if !client.Settings().EnableExtendedConnect {
				err = errExtendedConnectUnsupported
			}
		case <-quicConn.Context().Done():
			err = context.Cause(quicConn.Context())
		case <-ctx.Done():
			err = context.Cause(ctx)
		}
		if err == nil {
			t.mu.Lock()
			if t.closed || t.connections[connection.address] != connection {
				t.mu.Unlock()
				err = errors.New("HTTP/3 WebSocket upstream retired during setup")
				_ = quicConn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "WebSocket upstream retired")
			} else {
				connection.conn = quicConn
				connection.client = client
				connection.dialCancel = nil
				connection.idleTimer = time.AfterFunc(websocketHTTP3IdleTimeout, func() {
					t.closeIdle(connection)
				})
				close(connection.ready)
				t.mu.Unlock()
				go func() {
					<-quicConn.Context().Done()
					t.mu.Lock()
					if t.connections[connection.address] == connection {
						delete(t.connections, connection.address)
					}
					if connection.idleTimer != nil {
						connection.idleTimer.Stop()
						connection.idleTimer = nil
					}
					t.mu.Unlock()
				}()
				return
			}
		}
	}
	if err == nil {
		err = errors.New("HTTP/3 WebSocket upstream setup failed")
	}
	if quicConn != nil {
		_ = quicConn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "WebSocket upstream setup failed")
	}
	t.mu.Lock()
	connection.err = err
	connection.dialCancel = nil
	if t.connections[connection.address] == connection {
		delete(t.connections, connection.address)
	}
	close(connection.ready)
	t.mu.Unlock()
}

func (t *websocketHTTP3Transport) evict(connection *websocketHTTP3Connection) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.connections[connection.address] == connection {
		delete(t.connections, connection.address)
	}
	if connection.idleTimer != nil {
		connection.idleTimer.Stop()
		connection.idleTimer = nil
	}
}

func (t *websocketHTTP3Transport) release(connection *websocketHTTP3Connection) {
	t.mu.Lock()
	connection.active--
	if connection.active != 0 {
		t.mu.Unlock()
		return
	}
	if t.closed || t.connections[connection.address] != connection {
		if t.connections[connection.address] == connection {
			delete(t.connections, connection.address)
		}
		conn := connection.conn
		t.mu.Unlock()
		if conn != nil {
			_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "WebSocket upstream retired")
		}
		return
	}
	connection.idleTimer = time.AfterFunc(websocketHTTP3IdleTimeout, func() {
		t.closeIdle(connection)
	})
	t.mu.Unlock()
}

func (t *websocketHTTP3Transport) closeIdle(connection *websocketHTTP3Connection) {
	t.mu.Lock()
	if t.connections[connection.address] != connection || connection.active != 0 {
		t.mu.Unlock()
		return
	}
	delete(t.connections, connection.address)
	connection.idleTimer = nil
	conn := connection.conn
	t.mu.Unlock()
	if conn != nil {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "WebSocket upstream idle")
	}
}

func (t *websocketHTTP3Transport) Close() error {
	t.mu.Lock()
	t.closed = true
	var connections []*websocketHTTP3Connection
	for address, connection := range t.connections {
		delete(t.connections, address)
		if connection.idleTimer != nil {
			connection.idleTimer.Stop()
			connection.idleTimer = nil
		}
		if connection.dialCancel != nil {
			connection.dialCancel()
		}
		if connection.active == 0 && connection.conn != nil {
			connections = append(connections, connection)
		}
	}
	t.mu.Unlock()
	var closeErr error
	for _, connection := range connections {
		if err := connection.conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "WebSocket upstream retired"); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

type websocketHTTP3Body struct {
	stream       *http3.RequestStream
	release      func()
	readTimeout  time.Duration
	writeTimeout time.Duration
	done         chan struct{}
	closeOnce    sync.Once
}

func (b *websocketHTTP3Body) Read(p []byte) (int, error) {
	if b.readTimeout > 0 {
		if err := b.stream.SetReadDeadline(time.Now().Add(b.readTimeout)); err != nil {
			return 0, err
		}
	}
	return b.stream.Read(p)
}

func (b *websocketHTTP3Body) Write(p []byte) (int, error) {
	if b.writeTimeout > 0 {
		if err := b.stream.SetWriteDeadline(time.Now().Add(b.writeTimeout)); err != nil {
			return 0, err
		}
	}
	return b.stream.Write(p)
}

func (b *websocketHTTP3Body) Close() error {
	b.closeOnce.Do(func() {
		close(b.done)
		b.stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		b.stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		b.release()
	})
	return nil
}

func (b *websocketHTTP3Body) cancelOnContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		b.stream.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		b.stream.CancelWrite(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
	case <-b.done:
	}
}

var _ io.ReadWriteCloser = (*websocketHTTP3Body)(nil)

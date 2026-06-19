//go:build grpc

package handler

import (
	"log/slog"
	"net/http"

	"jul/internal/config"
	"jul/internal/transcode"
)

// NewGRPCTranscode builds a gRPC-JSON transcoding handler for a location. It
// loads the method routing table from the configured descriptor source, dials
// the gRPC backend, and returns a handler that maps REST/JSON requests to unary
// gRPC calls. The returned handler also implements io.Closer (via the
// Transcoder), so the caller closes it to release the backend connection when
// the configuration is replaced.
func NewGRPCTranscode(_ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, log *slog.Logger, onResult func(method, code string), onStreamMsg func(method, direction string)) (http.Handler, error) {
	return transcode.New(*loc.GRPCTranscode, upstreams, transcode.Options{
		Logger:      log,
		OnResult:    onResult,
		OnStreamMsg: onStreamMsg,
	})
}

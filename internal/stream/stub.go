//go:build !stream

package stream

import (
	"errors"

	"jul/internal/config"
)

// Compiled reports whether this binary includes L4 stream-proxy support. It is
// false in the default build, which excludes the stream listeners entirely.
const Compiled = false

// Server is the no-stream stub. It holds no listeners and never binds.
type Server struct{}

// NewServer returns a no-op stream server for builds without the "stream" tag.
func NewServer(_ Options) *Server { return &Server{} }

// Reload is the stub reload. It rejects any configured stream so a reload that
// newly adds a [[stream]] block fails clearly (the startup-time safety net is
// Check); an empty stream set is accepted as a no-op.
func (s *Server) Reload(streams []config.StreamServer, _ map[string]config.UpstreamConfig) error {
	if len(streams) > 0 {
		return errors.New("stream proxy requires a build with -tags stream")
	}
	return nil
}

// Close is a no-op for the stub.
func (s *Server) Close() error { return nil }

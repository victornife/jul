package server

import "net/http"

// h2cEnabledForAddr reports whether any server block on addr enables cleartext
// HTTP/2 (h2c). h2c only applies to a plaintext listener (TLS listeners
// negotiate HTTP/2 via ALPN already), so bind consults this only on the non-TLS
// path.
func (s *Server) h2cEnabledForAddr(addr string) bool {
	for i := range s.cfg.Servers {
		srv := &s.cfg.Servers[i]
		if srv.Listen == addr && srv.H2C {
			return true
		}
	}
	return false
}

// enableH2C configures an http.Server to also serve prior-knowledge cleartext
// HTTP/2 (h2c) alongside HTTP/1.1 on a plaintext listener, so native gRPC
// clients that connect without TLS are accepted. It uses the standard library's
// Protocols negotiation (Go 1.24+), so no extra dependency is pulled in.
func enableH2C(httpd *http.Server) {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	httpd.Protocols = &p
}

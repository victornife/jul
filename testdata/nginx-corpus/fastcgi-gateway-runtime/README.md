# fastcgi-gateway-runtime

Repository-authored, sanitized NGINX migration fixture for issue #367 ([MIG-06] gRPC/FastCGI/uWSGI/L4 migration E2E lanes). It uses only a synthetic loopback placeholder address; it contains no production configuration, credentials, or external endpoints.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. It proves `fastcgi_pass` plus a literal `fastcgi_param` (`HTTP_X_CUSTOM_TEST hello-world;`) end to end: the param accumulates into `fastcgi_params`, a static map Jul's real FastCGI client (`github.com/yookoala/gofast`) merges into the actual FastCGI PARAMS record it sends the backend, taking precedence over anything auto-derived.

The real-Jul E2E lives in `cmd/jul/corpus_fastcgi_uwsgi_runtime_test.go` (`TestNGINXCorpusFastCGIPassRealE2E`) rather than the generic manifest `scenarios` array, because it needs a real FastCGI responder to assert against. The backend is Go's own standard-library `net/http/fcgi.Serve`, a real, protocol-correct FastCGI responder (not a hand-simulated substitute), whose handler reads `r.Header.Get("X-Custom-Test")` - the CGI-standard header projection of the `HTTP_X_CUSTOM_TEST` FastCGI param - and echoes it into the response body, proving the literal param actually reached the backend over the real wire protocol, not merely that the translated config field is populated.

No pinned real-NGINX reference lane runs this fixture today (`scripts/nginx-migration-e2e.sh` has no FastCGI/PHP-FPM backend support).

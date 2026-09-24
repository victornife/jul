# uwsgi-gateway-runtime

Repository-authored, sanitized NGINX migration fixture for issue #367 ([MIG-06] gRPC/FastCGI/uWSGI/L4 migration E2E lanes). It uses only a synthetic loopback placeholder address; it contains no production configuration, credentials, or external endpoints.

The exact assessment contract, candidate disposition, categories, origin, and license are recorded in `manifest.json`. It proves the `uwsgi_pass` → `uwsgi_pass` location field translation end to end. `uwsgi_param` has no corresponding translation (Jul has no per-parameter uWSGI configuration equivalent, so it always stays an explicit blocking finding), so this fixture instead proves param propagation through the client-supplied request header path every uWSGI request already carries: Jul's uWSGI client maps an inbound `X-Test-Header` request header onto the uwsgi var `HTTP_X_TEST_HEADER`, the same CGI-standard convention FastCGI uses.

The real-Jul E2E lives in `cmd/jul/corpus_fastcgi_uwsgi_runtime_test.go` (`TestNGINXCorpusUWSGIPassRealE2E`) rather than the generic manifest `scenarios` array, because it needs a real uWSGI-protocol responder to assert against - the generic single-request/response Scenario/Dimension model has no uWSGI wire-format concept. The backend is a small real uWSGI-protocol server (the packet header, the length-prefixed var block, and the raw CGI-style response text), reproducing the same wire format `internal/handler/fastcgi_test.go`'s own `TestUWSGIHandlerRoundTrip` unit test already exercises against Jul's uWSGI client - so this is a real protocol round trip, not a simulated substitute.

No pinned real-NGINX reference lane runs this fixture today (`scripts/nginx-migration-e2e.sh` has no uWSGI backend support).

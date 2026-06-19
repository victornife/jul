module jul

go 1.26.4

require (
	github.com/andybalholm/brotli v1.2.1
	github.com/fsnotify/fsnotify v1.7.0
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/klauspost/compress v1.18.6
	github.com/pelletier/go-toml/v2 v2.2.3
	github.com/prometheus/client_golang v1.19.1
	github.com/prometheus/client_model v0.5.0
	github.com/quic-go/quic-go v0.59.1
	github.com/tetratelabs/wazero v1.12.0
	github.com/tufanbarisyildirim/gonginx v0.0.0-20260220081509-8e17ce617db3
	github.com/yookoala/gofast v0.8.0
	go.opentelemetry.io/otel v1.43.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.43.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.43.0
	go.opentelemetry.io/otel/sdk v1.43.0
	go.opentelemetry.io/otel/trace v1.43.0
	golang.org/x/crypto v0.53.0
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.46.0
	golang.org/x/time v0.5.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/prometheus/common v0.48.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/tools v0.45.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
)

// gofast@v0.8.0 imports golang.org/x/tools/godoc/vfs, which was removed in x/tools
// v0.30.0+. Transitive deps (x/text) pull x/tools v0.40.0, so pin x/tools to the last
// version that still ships godoc/vfs. Nothing in this module needs x/tools at runtime
// beyond gofast's vfs use; drop this replace once gofast is upgraded or replaced.
replace golang.org/x/tools => golang.org/x/tools v0.6.0

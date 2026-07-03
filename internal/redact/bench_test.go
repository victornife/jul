package redact

import (
	"bytes"
	"fmt"
	"testing"
)

// BenchmarkApplyNoSecrets measures the hot path when the registry is empty.
// This is the common case (most log lines contain no secret), so it must be
// allocation-free and take only a read lock.
func BenchmarkApplyNoSecrets(b *testing.B) {
	const msg = "2006-01-02T15:04:05Z INF request method=GET path=/api/v1/users status=200 duration=12ms\n"
	for i := 0; i < b.N; i++ {
		_ = Apply(msg)
	}
}

// BenchmarkApplyOneSecretMasks measures a single-secret hit: the secret
// appears in the input and is replaced by Mask.
func BenchmarkApplyOneSecretMasks(b *testing.B) {
	Replace(map[string]struct{}{"sk_live_abcdefghijklmnopqrstuvwxyz": {}})
	defer Replace(nil)
	const msg = "Authorization: Bearer sk_live_abcdefghijklmnopqrstuvwxyz\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Apply(msg)
	}
}

// BenchmarkApplyOneSecretMisses measures a single-secret registry where the
// input does not contain the secret (common for most log lines once a secret
// is registered).
func BenchmarkApplyOneSecretMisses(b *testing.B) {
	Replace(map[string]struct{}{"sk_live_abcdefghijklmnopqrstuvwxyz": {}})
	defer Replace(nil)
	const msg = "2006-01-02T15:04:05Z INF request method=GET path=/health status=200\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Apply(msg)
	}
}

// BenchmarkApplyTenSecrets measures the cost when the registry contains many
// secrets (e.g. a large config with tokens for many upstreams).
func BenchmarkApplyTenSecrets(b *testing.B) {
	secrets := make(map[string]struct{}, 10)
	for i := 0; i < 10; i++ {
		secrets[fmt.Sprintf("tok_%02d_%s", i, "aBcDeFgHiJkLmNoPqRsTuVwXyZ")] = struct{}{}
	}
	Replace(secrets)
	defer Replace(nil)
	const msg = "request tok_03_aBcDeFgHiJkLmNoPqRsTuVwXyZ used for upstream api\n"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Apply(msg)
	}
}

// BenchmarkWriterWrite measures the io.Writer wrapper for slog-like use:
// a whole log record is written in one call and masked before hitting the
// underlying sink.
func BenchmarkWriterWrite(b *testing.B) {
	Replace(map[string]struct{}{"admin-secret-token-42": {}})
	defer Replace(nil)
	var sink bytes.Buffer
	w := Writer(&sink)
	in := []byte("level=INFO msg=login user=admin token=admin-secret-token-42\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = w.Write(in)
	}
}

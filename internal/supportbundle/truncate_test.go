// Copyright 2026 Victor Niharra <vniharra@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"bytes"
	"testing"
)

func TestTruncateTextArtifactRespectsLimit(t *testing.T) {
	t.Parallel()
	input := bytes.Repeat([]byte("x"), 256)
	marker := []byte("truncated by support-bundle artifact limit")
	for _, limit := range []int64{0, 1, 16, 64, 128} {
		got := truncateTextArtifact(input, limit)
		if int64(len(got)) > limit {
			t.Fatalf("limit %d produced %d bytes", limit, len(got))
		}
		if limit >= int64(len(marker)+2) && !bytes.Contains(got, marker) {
			t.Fatalf("limit %d omitted the truncation marker: %q", limit, got)
		}
	}
}

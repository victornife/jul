// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"bytes"
	"testing"
)

func TestTruncateTextArtifactRespectsLimit(t *testing.T) {
	t.Parallel()
	input := bytes.Repeat([]byte("x"), 256)
	for _, limit := range []int64{0, 1, 16, 64, 128} {
		got := truncateTextArtifact(input, limit)
		if int64(len(got)) > limit {
			t.Fatalf("limit %d produced %d bytes", limit, len(got))
		}
	}
}

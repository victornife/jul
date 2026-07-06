// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build zstd

package middleware

import (
	"io"

	"github.com/klauspost/compress/zstd"
)

// Registers the "zstd" content coding when built with -tags zstd.
func init() {
	registerEncoder("zstd", func(level int) encoderConstructor {
		lvl := zstdLevel(level)
		return func(w io.Writer) encoder {
			enc, _ := zstd.NewWriter(w, zstd.WithEncoderLevel(lvl))
			return enc
		}
	})
}

func zstdLevel(level int) zstd.EncoderLevel {
	switch {
	case level <= 0:
		return zstd.SpeedDefault
	case level == 1:
		return zstd.SpeedFastest
	case level == 2:
		return zstd.SpeedDefault
	case level == 3:
		return zstd.SpeedBetterCompression
	default:
		return zstd.SpeedBestCompression
	}
}

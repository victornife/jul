//go:build brotli

package middleware

import (
	"io"

	"github.com/andybalholm/brotli"
)

// Registers the "br" content coding when built with -tags brotli.
func init() {
	registerEncoder("br", func(level int) encoderConstructor {
		lvl := brotliLevel(level)
		return func(w io.Writer) encoder {
			return brotli.NewWriterLevel(w, lvl)
		}
	})
}

func brotliLevel(level int) int {
	switch {
	case level <= 0:
		return brotli.DefaultCompression
	case level > brotli.BestCompression:
		return brotli.BestCompression
	default:
		return level
	}
}

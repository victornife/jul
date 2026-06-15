//go:build windows

package observability

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"jul/internal/config"
)

// TestBuildAccessSinksSyslogUnsupportedOnWindows verifies the syslog sink fails
// cleanly on Windows (where log/syslog is not implemented) instead of silently
// dropping records.
func TestBuildAccessSinksSyslogUnsupportedOnWindows(t *testing.T) {
	base := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	_, _, err := BuildAccessSinks(config.AccessLogConfig{Sinks: []string{"syslog"}}, base)
	if err == nil {
		t.Fatal("expected syslog sink to be unsupported on Windows")
	}
	if !strings.Contains(err.Error(), "syslog") {
		t.Errorf("error should mention syslog: %v", err)
	}
}

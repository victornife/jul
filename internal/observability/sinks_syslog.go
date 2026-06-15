//go:build !windows

package observability

import (
	"io"
	"log/syslog"
)

// newSyslogWriter connects to the local system log. Access lines are written at
// LOG_INFO with the "jul" tag over the LOG_LOCAL0 facility; each record is one
// syslog message. The returned writer is closed on shutdown.
func newSyslogWriter() (io.WriteCloser, error) {
	return syslog.New(syslog.LOG_INFO|syslog.LOG_LOCAL0, "jul")
}

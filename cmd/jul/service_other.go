//go:build !windows

package main

// runService is the entry hook for Windows service control. On non-Windows
// platforms there is no service manager, so it reports "not handled" and the
// caller falls back to normal foreground execution.
func runService() (handled bool, exitCode int) {
	return false, 0
}

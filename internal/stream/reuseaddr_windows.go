//go:build stream && windows

package stream

// setReuseAddr is intentionally a no-op on Windows.
//
// On Unix SO_REUSEADDR allows rapid rebinding when the previous process is in
// TIME_WAIT. On Windows the same flag has a completely different (and
// dangerous) meaning: it permits a second socket to forcibly bind to a port
// already in use by another process, producing indeterminate behaviour and
// creating a port-hijacking vector.  Windows already provides “Enhanced Socket
// Security” by default (since WS2K3), so listeners created without special
// flags are safe for production.
//
// See: https://learn.microsoft.com/en-us/windows/win32/winsock/using-so-reuseaddr-and-so-exclusiveaddruse
func setReuseAddr(fd uintptr) error {
	return nil
}

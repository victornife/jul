//go:build stream

package stream

import (
	"net"
	"runtime"
	"testing"
	"time"
)

// TestListenTCPWithReuse_BasicBind asserts that ListenTCPWithReuse can open a
// TCP listener on an ephemeral port.
func TestListenTCPWithReuse_BasicBind(t *testing.T) {
	ln, err := ListenTCPWithReuse("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenTCPWithReuse: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	if addr.Port == 0 {
		t.Fatal("expected a non-zero ephemeral port")
	}
}

// TestListenTCPWithReuse_Rebind checks the platform-specific reuse behaviour.
//
// On Unix, SO_REUSEADDR should allow immediate rebind after Close when the
// socket is in TIME_WAIT, so the second ListenTCPWithReuse succeeds.
//
// On Windows, ListenTCPWithReuse intentionally does NOT set SO_REUSEADDR
// because its semantics differ and are unsafe for public TCP servers. The
// second bind may therefore fail with an "address already in use" error if the
// OS has not yet released the socket. We tolerate that failure.
func TestListenTCPWithReuse_Rebind(t *testing.T) {
	addr := "127.0.0.1:0"

	ln1, err := ListenTCPWithReuse(addr)
	if err != nil {
		t.Fatalf("first ListenTCPWithReuse: %v", err)
	}
	realAddr := ln1.Addr().String()
	ln1.Close()

	// Give the OS a moment to transition the socket into TIME_WAIT.
	time.Sleep(50 * time.Millisecond)

	ln2, err := ListenTCPWithReuse(realAddr)
	if err == nil {
		ln2.Close()
		return
	}

	// On non-Windows platforms a failure here is unexpected.
	if runtime.GOOS == "windows" {
		t.Logf("second bind failed on Windows (expected): %v", err)
		return
	}
	t.Fatalf("second ListenTCPWithReuse failed unexpectedly: %v", err)
}

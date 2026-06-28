//go:build stream

package stream

import (
	"strconv"
	"testing"
	"time"
)

// BenchmarkUDPAdmitAtCap measures the admission decision on a full session table
// — the hot path taken for every new UDP client once a listener is at its
// max_udp_sessions cap. At the cap admitUDPLocked scans all sessions to find the
// least-recently-seen one (an O(n) sweep), so this guards the per-datagram cost
// of the cap enforcement as the table grows. All sessions are kept fresh so the
// scan finds nothing reclaimable and returns a rejection without mutating the
// map, keeping the benchmark stable across iterations.
func BenchmarkUDPAdmitAtCap(b *testing.B) {
	for _, n := range []int{256, 4096, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			now := time.Now().UnixNano()
			sessions := make(map[string]*udpSession, n)
			for i := 0; i < n; i++ {
				s := &udpSession{}
				s.lastSeen.Store(now) // fresh: never reclaimable, so admit rejects
				sessions[strconv.Itoa(i)] = s
			}
			l := &listener{udpSessions: sessions, udpPending: map[string]*udpPending{}}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, ok := l.admitUDPLocked(n, time.Minute, now); ok {
					b.Fatal("expected rejection at cap with only fresh sessions")
				}
			}
		})
	}
}

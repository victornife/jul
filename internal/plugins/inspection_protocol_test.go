// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
)

// protoServer serves h on one protocol and returns a client, the base URL,
// and whether the client is still on its first connection (H3 reports the
// protocol only).
type protoServer func(t *testing.T, h http.Handler) (c *http.Client, base string, sameConn func() bool)

func h1Server(t *testing.T, h http.Handler) (*http.Client, string, func() bool) {
	srv, c, conns := netServer(t, h, false, 0)
	return c, srv.URL, func() bool { return conns.Load() == 1 }
}

func h2Server(t *testing.T, h http.Handler) (*http.Client, string, func() bool) {
	srv, c, conns := netServer(t, h, true, 0)
	return c, srv.URL, func() bool { return conns.Load() == 1 }
}

func TestInspectionOutcomesOverH1H2(t *testing.T) {
	runInspectionOutcomes(t, "h1", 1, h1Server)
	runInspectionOutcomes(t, "h2", 2, h2Server)
}

// runInspectionOutcomes checks, on a real server and protocol, that the
// inspection body deadline and overload outcomes are delivered to the
// client, end only the affected stream, and spend no guest time.
func runInspectionOutcomes(t *testing.T, proto string, major int, start protoServer) {
	const bodyTimeout = 300 * time.Millisecond
	t.Run(proto+"/body_timeout", func(t *testing.T) {
		m, hooks, g, rec := govManager(t, limitsWith(1<<20, func(l *inspection.Limits) { l.BodyTimeout = bodyTimeout }))
		s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
		mux := http.NewServeMux()
		var hits, other atomic.Int64
		mux.Handle("/p", s.Middleware("r")(echoNext(&hits)))
		mux.Handle("/other", echoNext(&other))
		c, base, sameConn := start(t, mux)
		if r := post(c, base+"/other", "warm"); r.err != nil || r.proto != major {
			t.Fatalf("warm-up: %+v", r)
		}

		if r := post(c, base+"/p", ""); r.status != http.StatusOK {
			t.Fatalf("empty body: %+v", r)
		}
		// A slow upload that completes inside body_timeout is served.
		pr, pw := io.Pipe()
		done := make(chan netResult, 1)
		go func() {
			req, _ := http.NewRequest(http.MethodPost, base+"/p", pr)
			resp, err := c.Do(req)
			if err != nil {
				done <- netResult{err: err}
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			done <- netResult{status: resp.StatusCode, proto: resp.ProtoMajor, body: string(b)}
		}()
		for _, part := range []string{"sl", "ow", "-ok"} {
			_, _ = pw.Write([]byte(part))
			time.Sleep(bodyTimeout / 10)
		}
		_ = pw.Close()
		if r := <-done; r.status != http.StatusOK || r.body != "slow-ok" {
			t.Fatalf("valid slow upload: %+v", r)
		}

		// A stalled upload is answered 408 at the absolute deadline.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pw2, res := startUpload(ctx, c, base+"/p")
		defer pw2.Close()
		begin := time.Now()
		_, _ = pw2.Write([]byte("part"))
		waitFor(t, "preparing", func() bool { return preparing(s.plugins["r"]) == 1 })
		if r := post(c, base+"/other", "sibling"); r.status != http.StatusOK || r.body != "sibling" {
			t.Fatalf("sibling during stalled upload: %+v", r)
		}
		r := <-res
		elapsed := time.Since(begin)
		if r.err != nil || r.status != http.StatusRequestTimeout || r.proto != major {
			t.Fatalf("stalled upload: %+v", r)
		}
		if elapsed < bodyTimeout || elapsed > 5*time.Second {
			t.Fatalf("408 after %s, want about %s", elapsed, bodyTimeout)
		}
		if _, timeouts := rec.snapshot(); timeouts[proto+"/timeout"] != 1 || len(timeouts) != 1 {
			t.Fatalf("timeout metrics %v", timeouts)
		}
		if inv, _, _, panics, _ := hooks.snapshot(); inv != 2 || panics != 0 || hits.Load() != 2 {
			t.Fatalf("invocations=%d panics=%d origin=%d (only the empty and slow uploads run)", inv, panics, hits.Load())
		}
		if r := post(c, base+"/other", "after"); r.status != http.StatusOK {
			t.Fatalf("after 408: %+v", r)
		}
		if major != 1 && !sameConn() {
			t.Fatal("a stream deadline closed the shared connection")
		}
		waitFor(t, "drained", func() bool { s := g.Snapshot(); return s.ActiveLeases == 0 })
		assertDrained(t, g)
	})

	t.Run(proto+"/overload", func(t *testing.T) {
		m, hooks, g, _ := govManager(t, limitsWith(4096))
		s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
		mux := http.NewServeMux()
		var hits, other atomic.Int64
		mux.Handle("/p", s.Middleware("r")(echoNext(&hits)))
		mux.Handle("/other", echoNext(&other))
		c, base, sameConn := start(t, mux)
		_ = post(c, base+"/other", "warm")
		check := func(what string, r netResult) {
			t.Helper()
			if r.err != nil || r.status != http.StatusServiceUnavailable || r.proto != major ||
				strings.TrimSpace(r.body) != inspection.ReasonOverloaded {
				t.Fatalf("%s: %+v", what, r)
			}
		}
		// Mid-body: the base fits, the body's growth does not.
		check("mid-body", post(c, base+"/p", strings.Repeat("m", 20000)))
		// At admission: another request holds the base.
		holder, _, _, err := g.AcquireLease(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		check("admission", post(c, base+"/p", "small"))
		_ = holder.Close()
		if r := post(c, base+"/p", "fits"); r.status != http.StatusOK || r.body != "fits" {
			t.Fatalf("after release: %+v", r)
		}
		if inv, _, _, panics, _ := hooks.snapshot(); inv != 1 || panics != 0 || hits.Load() != 1 {
			t.Fatalf("invocations=%d panics=%d origin=%d", inv, panics, hits.Load())
		}
		if major != 1 && !sameConn() {
			t.Fatal("overload closed the shared connection")
		}
		waitFor(t, "drained", func() bool { return g.Snapshot().ActiveLeases == 0 })
		assertDrained(t, g)
	})

	for _, mode := range []inspection.Mode{inspection.Enforce, inspection.Observe} {
		t.Run(proto+"/guest_capacity/"+string(mode), func(t *testing.T) {
			limits := limitsWith(1<<20, func(l *inspection.Limits) {
				l.Mode = mode
				l.MaxGuestCapacityBytes = 16 << 20
			})
			m, hooks, g, rec := govManager(t, limits)
			s := buildSet(t, m, map[string]config.PluginConfig{
				"r": bodyGuestConfig(ABIJulV2, guestReadContinue, func(pc *config.PluginConfig) {
					pc.MemoryLimit = config.Size(16 << 20)
					pc.MaxInstances = 2
				}),
			})
			p := s.plugins["r"]
			held, err := p.acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			holding := true
			defer func() {
				if holding {
					p.release(held)
				}
			}()

			mux := http.NewServeMux()
			var hits, other atomic.Int64
			mux.Handle("/p", s.Middleware("r")(echoNext(&hits)))
			mux.Handle("/other", echoNext(&other))
			c, base, sameConn := start(t, mux)
			if r := post(c, base+"/other", "warm"); r.err != nil || r.proto != major {
				t.Fatalf("warm-up: %+v", r)
			}
			resp, err := c.Post(base+"/p", "text/plain", strings.NewReader("body"))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			expectedDecisions := 1
			if mode == inspection.Enforce {
				if resp.ProtoMajor != major || resp.StatusCode != http.StatusServiceUnavailable ||
					resp.Header.Get("Retry-After") != "1" || strings.TrimSpace(string(body)) != inspection.ReasonOverloaded {
					t.Fatalf("guest-capacity response = %d %v %q", resp.StatusCode, resp.Header, body)
				}
				if inv, _, _, panics, _ := hooks.snapshot(); inv != 0 || panics != 0 || hits.Load() != 0 {
					t.Fatalf("refusal ran guest or origin: invocations=%d panics=%d origin=%d", inv, panics, hits.Load())
				}
				if major >= 2 {
					req, err := http.NewRequest(http.MethodPost, base+"/p", strings.NewReader("body"))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/grpc")
					grpcResp, err := c.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					_, err = io.Copy(io.Discard, grpcResp.Body)
					_ = grpcResp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					if grpcResp.ProtoMajor != major || grpcResp.StatusCode != http.StatusOK ||
						grpcResp.Header.Get("Grpc-Status") != "14" ||
						grpcResp.Header.Get("Grpc-Message") != inspection.ReasonOverloaded {
						t.Fatalf("gRPC guest-capacity response = %d %v", grpcResp.StatusCode, grpcResp.Header)
					}
					expectedDecisions++
				}
			} else if resp.ProtoMajor != major || resp.StatusCode != http.StatusOK || string(body) != "body" {
				t.Fatalf("observe guest-capacity response = %d %q", resp.StatusCode, body)
			}
			if rejections, _ := rec.snapshot(); rejections["guest_capacity/"+string(mode)] != expectedDecisions {
				t.Fatalf("guest-capacity decisions = %v, want %d", rejections, expectedDecisions)
			}
			if major >= 2 && !sameConn() {
				t.Fatal("guest-capacity handling closed the shared connection")
			}

			p.release(held)
			holding = false
			if r := post(c, base+"/p", "after"); r.status != http.StatusOK || r.body != "after" {
				t.Fatalf("after capacity became reusable: %+v", r)
			}
			if inv, _, _, panics, _ := hooks.snapshot(); panics != 0 || int64(inv) != hits.Load() {
				t.Fatalf("invocations=%d panics=%d origin=%d", inv, panics, hits.Load())
			}
			assertDrained(t, g)
		})
	}
}

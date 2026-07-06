//go:build ignore

// Track 2 — L4 stream TCP load generator (raw TCP echo soak)
// Usage: go run scripts/burn-in-stream-load.go -duration 1h -workers 50 -target "127.0.0.1:15432"
// Requires stream-echo.go backends on the upstream ports.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	var (
		duration = flag.Duration("duration", 5*time.Minute, "How long to run")
		workers  = flag.Int("workers", 50, "Concurrent connection workers")
		target   = flag.String("target", "127.0.0.1:15432", "Stream proxy listen address")
	)
	flag.Parse()

	fmt.Println("L4 Stream TCP load test starting...")
	fmt.Printf("Duration    : %v\n", *duration)
	fmt.Printf("Workers     : %d\n", *workers)
	fmt.Printf("Target      : %s\n", *target)
	fmt.Printf("End time    : %s UTC\n", time.Now().UTC().Add(*duration).Format("15:04:05"))
	fmt.Println()

	// Pre-flight check
	conn, err := net.Dial("tcp", *target)
	if err != nil {
		fmt.Printf("Pre-flight FAILED: %v\n", err)
		os.Exit(1)
	}
	conn.Close()
	fmt.Println("Pre-flight OK: TCP connection accepted")

	end := time.Now().Add(*duration)
	var total, success, fail int64
	var rounds int64

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := []byte(fmt.Sprintf("stream-echo-%d-payload-data-chunk", id))
			readBuf := make([]byte, len(payload))

			for time.Now().Before(end) {
				atomic.AddInt64(&total, 1)
				conn, err := net.Dial("tcp", *target)
				if err != nil {
					atomic.AddInt64(&fail, 1)
					time.Sleep(100 * time.Millisecond)
					continue
				}
				conn.SetDeadline(time.Now().Add(30 * time.Second))

				// Perform many echo rounds on this single persistent connection
				connOK := true
				for r := 0; r < 1000 && time.Now().Before(end) && connOK; r++ {
					conn.SetDeadline(time.Now().Add(10 * time.Second))
					if _, err := conn.Write(payload); err != nil {
						connOK = false
						atomic.AddInt64(&fail, 1)
						break
					}
					n, err := conn.Read(readBuf)
					if err != nil || n != len(payload) {
						connOK = false
						atomic.AddInt64(&fail, 1)
						break
					}
					atomic.AddInt64(&success, 1)
					atomic.AddInt64(&rounds, 1)
				}
				conn.Close()
				if connOK {
					// Connection completed all rounds successfully
				}
			}
		}(i)
	}

	// Progress ticker
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t := atomic.LoadInt64(&total)
				s := atomic.LoadInt64(&success)
				f := atomic.LoadInt64(&fail)
				r := atomic.LoadInt64(&rounds)
				if t > 0 {
					fmt.Printf("%s conns=%d rounds=%d ok=%d err=%d rate=%.2f%%\n",
						time.Now().Format("15:04:05"), t, r, s, f, float64(f)/float64(t)*100)
				}
			case <-done:
				return
			}
		}
	}()

	wg.Wait()
	close(done)

	t := atomic.LoadInt64(&total)
	s := atomic.LoadInt64(&success)
	f := atomic.LoadInt64(&fail)
	r := atomic.LoadInt64(&rounds)

	fmt.Println()
	fmt.Println("========== L4 STREAM TCP RESULTS ==========")
	fmt.Printf("Duration     : %v\n", *duration)
	fmt.Printf("Total conns  : %d\n", t)
	fmt.Printf("Echo rounds  : %d\n", r)
	fmt.Printf("Successful   : %d\n", s)
	fmt.Printf("Failed       : %d\n", f)
	if t > 0 {
		fmt.Printf("Error rate   : %.2f%%\n", float64(f)/float64(t)*100)
		fmt.Printf("Success rate : %.2f%%\n", float64(s)/float64(t)*100)
	}
	fmt.Println("===========================================")
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build ignore

// grpc-load.go is a soak-test load generator for Jul's gRPC features.
// It exercises gRPC transcoding (REST/JSON -> gRPC) and native gRPC
// passthrough (h2c or TLS) by sending sustained traffic and recording
// latency + error rate.
//
// Usage:
//
//	go run scripts/grpc-load.go -mode transcoding -duration 1h -workers 50
//	go run scripts/grpc-load.go -mode passthrough -duration 1h -workers 50 -h2c
//
// Requires:
//   - Jul running with burn-in-phase2a.toml (ports 8092 transcoding, 8095 passthrough)
//   - gRPC echo backend on :50051 (go run scripts/grpc-echo-server.go)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var (
	mode      = flag.String("mode", "transcoding", "transcoding or passthrough")
	duration  = flag.String("duration", "5m", "soak duration (e.g. 5m, 1h)")
	workers   = flag.Int("workers", 20, "concurrent workers")
	target    = flag.String("target", "127.0.0.1:8092", "gRPC transcoding target (HTTP)")
	passt     = flag.String("passthrough", "127.0.0.1:8095", "gRPC passthrough target (gRPC native)")
	h2c       = flag.Bool("h2c", true, "use cleartext h2c for passthrough (unused, kept for compatibility)")
	failEvery = flag.Int("fail-every", 0, "inject a 'fail' message every N requests (0=never)")
	quiet     = flag.Bool("quiet", false, "print only final summary")
)

type stats struct {
	requests   atomic.Uint64
	errors     atomic.Uint64
	grpcErrors atomic.Uint64 // gRPC status errors (not conn errors)
	latencySum atomic.Int64  // microseconds
	latencyMin atomic.Int64  // microseconds; -1 = uninitialized
	latencyMax atomic.Int64  // microseconds
}

func main() {
	flag.Parse()
	dur, err := time.ParseDuration(*duration)
	if err != nil {
		log.Fatalf("bad duration %q: %v", *duration, err)
	}

	fmt.Printf("=== Jul gRPC soak ===\n")
	fmt.Printf("Mode:      %s\n", *mode)
	fmt.Printf("Duration:  %s\n", dur)
	fmt.Printf("Workers:   %d\n", *workers)
	fmt.Printf("Target:    %s\n", targetAddr())
	fmt.Printf("Fail-every: %d\n", *failEvery)
	fmt.Println()

	st := &stats{}
	st.latencyMin.Store(-1)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(ctx, st, i)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	for {
		select {
		case <-done:
			goto finish
		case <-stop:
			cancel()
			goto finish
		case <-ticker.C:
			if !*quiet {
				printProgress(st)
			}
		}
	}

finish:
	wg.Wait()
	printSummary(st, dur)
}

func targetAddr() string {
	if *mode == "passthrough" {
		return *passt
	}
	return *target
}

func worker(ctx context.Context, st *stats, id int) {
	var do func(context.Context) error
	var closeCli func()

	switch *mode {
	case "transcoding":
		do, closeCli = transcodingClient(id)
	case "passthrough":
		do, closeCli = passthroughClient(id)
	default:
		log.Fatalf("unknown mode %q", *mode)
	}
	defer closeCli()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		err := do(ctx)
		elapsed := time.Since(start)

		st.requests.Add(1)
		us := elapsed.Microseconds()
		st.latencySum.Add(us)

		for {
			curMin := st.latencyMin.Load()
			if curMin != -1 && us >= curMin {
				break
			}
			if st.latencyMin.CompareAndSwap(curMin, us) {
				break
			}
		}
		for {
			curMax := st.latencyMax.Load()
			if us <= curMax {
				break
			}
			if st.latencyMax.CompareAndSwap(curMax, us) {
				break
			}
		}

		if err != nil {
			st.errors.Add(1)
			if s, ok := status.FromError(err); ok {
				// gRPC status error (Not found, etc.)
				if s.Code() != codes.OK {
					st.grpcErrors.Add(1)
				}
			}
		}
	}
}

func buildFileDescriptor() protoreflect.FileDescriptor {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    strPtr("echo/echo.proto"),
		Package: strPtr("echo"),
		Syntax:  strPtr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: strPtr("EchoRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				strField("message", 1), strField("id", 2),
			}},
			{Name: strPtr("EchoReply"), Field: []*descriptorpb.FieldDescriptorProto{
				strField("message", 1),
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: strPtr("EchoService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       strPtr("Echo"),
				InputType:  strPtr(".echo.EchoRequest"),
				OutputType: strPtr(".echo.EchoReply"),
			}},
		}},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		log.Fatalf("build descriptor: %v", err)
	}
	return fd
}

func strPtr(s string) *string { return &s }

func strField(name string, num int32) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     strPtr(name),
		Number:   int32Ptr(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		JsonName: strPtr(name),
	}
}

func int32Ptr(n int32) *int32 { return &n }

func serviceDescs(fd protoreflect.FileDescriptor) (protoreflect.MessageDescriptor, protoreflect.MessageDescriptor) {
	svc := fd.Services().Get(0)
	m := svc.Methods().Get(0)
	return m.Input(), m.Output()
}

func transcodingClient(id int) (func(context.Context) error, func()) {
	hc := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	return func(ctx context.Context) error {
		var reqBody string
		var method string
		var path string

		// Rotate between POST /v1/echo (body) and GET /v1/echo/{id}
		if rand.Intn(2) == 0 {
			method = http.MethodPost
			msg := fmt.Sprintf("w%d-r%d", id, rand.Int63())
			if *failEvery > 0 && rand.Intn(*failEvery) == 0 {
				msg = "fail"
			}
			b, _ := json.Marshal(map[string]string{"message": msg})
			reqBody = string(b)
			path = "http://" + *target + "/v1/echo"
		} else {
			method = http.MethodGet
			msg := fmt.Sprintf("w%d-r%d", id, rand.Int63())
			path = fmt.Sprintf("http://"+*target+"/v1/echo/%d?message=%s", rand.Intn(1000), url.QueryEscape(msg))
		}

		req, _ := http.NewRequestWithContext(ctx, method, path, bytes.NewReader([]byte(reqBody)))
		req.Header.Set("Content-Type", "application/json")
		req.Host = "localhost"
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
			// 200 = normal; 404 = expected for "fail" message (transcoded gRPC NotFound)
			return nil
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}, func() { hc.CloseIdleConnections() }
}

func passthroughClient(id int) (func(context.Context) error, func()) {
	conn, err := grpc.NewClient(
		*passt,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4*1024*1024)),
	)
	if err != nil {
		log.Fatalf("grpc dial %s: %v", *passt, err)
	}

	reqDesc, respDesc := serviceDescs(buildFileDescriptor())
	methodName := "/echo.EchoService/Echo"

	return func(ctx context.Context) error {
		msg := fmt.Sprintf("w%d-r%d", id, rand.Int63())
		if *failEvery > 0 && rand.Intn(*failEvery) == 0 {
			msg = "fail"
		}

		in := dynamicpb.NewMessage(reqDesc)
		in.Set(reqDesc.Fields().ByName("message"), protoreflect.ValueOfString(msg))

		out := dynamicpb.NewMessage(respDesc)
		err := conn.Invoke(ctx, methodName, in, out)
		if err != nil {
			// "fail" message triggers NotFound — that is expected.
			if msg == "fail" {
				if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
					return nil
				}
			}
			return err
		}
		return nil
	}, func() { conn.Close() }
}

func printProgress(st *stats) {
	r := st.requests.Load()
	e := st.errors.Load()
	ge := st.grpcErrors.Load()
	sum := st.latencySum.Load()
	var avg int64
	if r > 0 {
		avg = sum / int64(r)
	}
	fmt.Printf("  req=%d err=%d grpcErr=%d avg=%dµs\n", r, e, ge, avg)
}

func printSummary(st *stats, dur time.Duration) {
	r := st.requests.Load()
	e := st.errors.Load()
	ge := st.grpcErrors.Load()
	var avg int64
	if r > 0 {
		avg = st.latencySum.Load() / int64(r)
	}
	errPct := 0.0
	if r > 0 {
		errPct = float64(e) / float64(r) * 100
	}
	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("Duration:   %s\n", dur)
	fmt.Printf("Workers:    %d\n", *workers)
	fmt.Printf("Requests:   %d\n", r)
	fmt.Printf("Errors:     %d (%.3f%%)\n", e, errPct)
	fmt.Printf("gRPC errs:  %d\n", ge)
	fmt.Printf("RPS:        %.0f\n", float64(r)/dur.Seconds())
	fmt.Printf("Latency avg: %d µs\n", avg)
	fmt.Printf("Latency min: %d µs\n", st.latencyMin.Load())
	fmt.Printf("Latency max: %d µs\n", st.latencyMax.Load())
	if errPct > 0 {
		fmt.Println("FAILED — non-zero error rate")
		os.Exit(1)
	}
	fmt.Println("PASSED — zero errors")
}

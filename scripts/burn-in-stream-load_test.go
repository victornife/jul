//go:build ignore

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEchoRoundFragmentedReply(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	payload := []byte("complete echo reply")
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(server, buf); err != nil {
			done <- err
			return
		}
		// Each Pipe write blocks until its fragment is consumed, guaranteeing
		// distinct reads without scheduling assumptions or sleeps.
		for _, part := range [][]byte{buf[:5], buf[5:10], buf[10:]} {
			if _, err := server.Write(part); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	buf := make([]byte, len(payload))
	if err := echoRound(client, payload, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("reply = %q", buf)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEchoRoundRejectsTruncationAndWrongContents(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wantEOF     bool
	}{{"truncated", "part", true}, {"wrong contents", "xxxxxxx", false}} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go func() {
				defer server.Close()
				_, _ = io.ReadFull(server, make([]byte, 7))
				_, _ = server.Write([]byte(tc.reply))
			}()
			err := echoRound(client, []byte("payload"), make([]byte, 7))
			if tc.wantEOF {
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("got %v, want unexpected EOF", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "differs") {
				t.Fatalf("got %v, want content mismatch", err)
			}
		})
	}
}

type shortWriteConn struct{ net.Conn }

func (shortWriteConn) Write(b []byte) (int, error) { return len(b) - 1, nil }

func TestEchoRoundWriteFailureAndDeadline(t *testing.T) {
	client, server := net.Pipe()
	server.Close()
	defer client.Close()
	if err := echoRound(client, []byte("payload"), make([]byte, 7)); err == nil {
		t.Fatal("closed connection accepted")
	}
	if err := echoRound(shortWriteConn{client}, []byte("payload"), make([]byte, 7)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
	client, server = net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() { _, _ = io.ReadFull(server, make([]byte, 7)) }()
	if err := client.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	err := echoRound(client, []byte("payload"), make([]byte, 7))
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("got %v, want timeout", err)
	}
}

func TestLoadReportsConnectionRates(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid fragmented echoes", true: "corrupt echoes"}[corrupt], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var handlers sync.WaitGroup
			accepted := make(chan struct{})
			go func() {
				defer close(accepted)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					handlers.Add(1)
					go func() {
						defer handlers.Done()
						defer conn.Close()
						buf := make([]byte, len("stream-echo-0-payload-data-chunk"))
						for {
							if _, err := io.ReadFull(conn, buf); err != nil {
								return
							}
							if corrupt {
								buf[0] ^= 1
							}
							if _, err := conn.Write(buf[:5]); err != nil {
								return
							}
							if _, err := conn.Write(buf[5:]); err != nil {
								return
							}
						}
					}()
				}
			}()
			args, flags, stdout := os.Args, flag.CommandLine, os.Stdout
			defer func() { os.Args, flag.CommandLine, os.Stdout = args, flags, stdout }()
			flag.CommandLine = flag.NewFlagSet("stream-test", flag.ContinueOnError)
			os.Args = []string{"stream-test", "-duration=100ms", "-workers=1", "-target=" + listener.Addr().String()}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = writer
			main()
			writer.Close()
			os.Stdout = stdout
			output, err := io.ReadAll(reader)
			reader.Close()
			listener.Close()
			<-accepted
			handlers.Wait()
			if err != nil {
				t.Fatal(err)
			}
			var total, failed, rounds int
			for _, line := range strings.Split(string(output), "\n") {
				_, _ = fmt.Sscanf(line, "Total conns  : %d", &total)
				_, _ = fmt.Sscanf(line, "Failed conns : %d", &failed)
				_, _ = fmt.Sscanf(line, "Echo rounds  : %d", &rounds)
			}
			if total == 0 || (corrupt && (failed == 0 || rounds != 0)) || (!corrupt && (failed != 0 || rounds == 0)) {
				t.Fatalf("incorrect counters:\n%s", output)
			}
			want := fmt.Sprintf("Success rate : %.2f%%", float64(total-failed)/float64(total)*100)
			if !strings.Contains(string(output), want) {
				t.Fatalf("want %q in output:\n%s", want, output)
			}
		})
	}
}

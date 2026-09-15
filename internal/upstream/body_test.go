// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockedReadBody struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockedReadBody) Read([]byte) (int, error) {
	close(b.started)
	<-b.release
	return 0, errors.New("backend read failed after close")
}
func (*blockedReadBody) Close() error { return nil }

type closingBody struct{ closeErr error }

func (*closingBody) Read([]byte) (int, error) { return 0, nil }
func (b *closingBody) Close() error           { return b.closeErr }

type readErrorBody struct{ err error }

func (b *readErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (*readErrorBody) Close() error               { return nil }

type errorReadWriteBody struct{ err error }

func (*errorReadWriteBody) Read([]byte) (int, error) { return 0, nil }
func (*errorReadWriteBody) Close() error             { return nil }
func (b *errorReadWriteBody) Write([]byte) (int, error) {
	return 0, b.err
}

type attributionTimeoutError struct{}

func (attributionTimeoutError) Error() string   { return "backend timed out" }
func (attributionTimeoutError) Timeout() bool   { return true }
func (attributionTimeoutError) Temporary() bool { return true }

func TestAttemptBodyCloseClassification(t *testing.T) {
	client, cancelClient := context.WithCancel(context.Background())
	cancelClient()
	attempt, cancelAttempt := context.WithCancel(context.Background())
	cancelAttempt()

	tests := []struct {
		name       string
		body       io.ReadCloser
		inbound    context.Context
		attempt    context.Context
		wantOrigin FailureOrigin
		wantHealth HealthConsequence
	}{
		{"client cancellation", &closingBody{}, client, client, OriginClientCancellation, HealthNeutral},
		{"Jul attempt cancellation", &closingBody{}, context.Background(), attempt, OriginJulTimeout, HealthNeutral},
		{"backend close error", &closingBody{closeErr: errors.New("backend close failed")}, context.Background(), context.Background(), OriginBackendTransport, HealthFailure},
		{"incomplete local close", &closingBody{}, context.Background(), context.Background(), OriginJulPolicy, HealthNeutral},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var got AttemptClassification
			wrapped := WrapAttemptBody(tt.body, -1, tt.inbound, tt.attempt,
				func(c AttemptClassification, _ error) {
					calls++
					got = c
				})
			_ = wrapped.Close()
			_ = wrapped.Close()
			if calls != 1 {
				t.Fatalf("completion calls = %d, want 1", calls)
			}
			if got.Origin() != tt.wantOrigin || got.Health() != tt.wantHealth {
				t.Fatalf("classification = {%q %d}, want {%q %d}", got.Origin(), got.Health(), tt.wantOrigin, tt.wantHealth)
			}
		})
	}
}

func TestAttemptBodyPreservesReadWriteCloserAndClassifiesWriteFailure(t *testing.T) {
	wantErr := errors.New("backend tunnel write failed")
	calls := 0
	var got AttemptClassification
	wrapped := WrapAttemptBody(&errorReadWriteBody{err: wantErr}, -1, context.Background(), context.Background(),
		func(c AttemptClassification, err error) {
			calls++
			got = c
			if !errors.Is(err, wantErr) {
				t.Errorf("completion error = %v, want %v", err, wantErr)
			}
		})
	writer, ok := wrapped.(io.Writer)
	if !ok {
		t.Fatal("wrapped upgraded body lost io.Writer")
	}
	if _, err := writer.Write([]byte("tunnel")); !errors.Is(err, wantErr) {
		t.Fatalf("write error = %v, want %v", err, wantErr)
	}
	_ = wrapped.Close()
	if calls != 1 || got.Origin() != OriginBackendTransport || got.Health() != HealthFailure {
		t.Fatalf("write classification = {%q %d}, calls=%d", got.Origin(), got.Health(), calls)
	}
}

func TestAttemptBodyCompleteReadIsSuccess(t *testing.T) {
	calls := 0
	var got AttemptClassification
	body := io.NopCloser(strings.NewReader("complete"))
	wrapped := WrapAttemptBody(body, int64(len("complete")), context.Background(), context.Background(),
		func(c AttemptClassification, _ error) {
			calls++
			got = c
		})
	if _, err := io.ReadAll(wrapped); err != nil {
		t.Fatal(err)
	}
	_ = wrapped.Close()
	if calls != 1 || got.Health() != HealthSuccess {
		t.Fatalf("read classification health=%d calls=%d, want success once", got.Health(), calls)
	}
}

func TestAttemptBodyEmptyResponseIsSuccess(t *testing.T) {
	var calls atomic.Int32
	var got AttemptClassification
	wrapped := WrapAttemptBody(io.NopCloser(strings.NewReader("")), 0,
		context.Background(), context.Background(), func(c AttemptClassification, _ error) {
			calls.Add(1)
			got = c
		})
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || got.Health() != HealthSuccess {
		t.Fatalf("empty completion = calls %d health %d; want success once", calls.Load(), got.Health())
	}
}

func TestAttemptBodyReadCloseRaceCompletesExactlyOnce(t *testing.T) {
	body := &blockedReadBody{started: make(chan struct{}), release: make(chan struct{})}
	var calls atomic.Int32
	wrapped := WrapAttemptBody(body, -1, context.Background(), context.Background(),
		func(AttemptClassification, error) { calls.Add(1) })
	readDone := make(chan struct{})
	go func() {
		_, _ = wrapped.Read(make([]byte, 1))
		close(readDone)
	}()
	<-body.started
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	close(body.release)
	<-readDone
	if got := calls.Load(); got != 1 {
		t.Fatalf("Read/Close race completed attempt %d times, want exactly once", got)
	}
}

func TestAttemptBodyReadFailureIsBackendFailure(t *testing.T) {
	wantErr := errors.New("backend body reset")
	var got AttemptClassification
	wrapped := WrapAttemptBody(&readErrorBody{err: wantErr}, -1, context.Background(), context.Background(),
		func(c AttemptClassification, err error) {
			got = c
			if !errors.Is(err, wantErr) {
				t.Errorf("completion error = %v, want %v", err, wantErr)
			}
		})
	if _, err := wrapped.Read(make([]byte, 1)); !errors.Is(err, wantErr) {
		t.Fatalf("read error = %v, want %v", err, wantErr)
	}
	if got.Origin() != OriginBackendTransport || got.Health() != HealthFailure {
		t.Fatalf("read classification = {%q %d}, want backend transport failure", got.Origin(), got.Health())
	}
}

func TestAttemptClassifierCoversTimeoutInterfaceAndInvalidProtocolReason(t *testing.T) {
	got := ClassifyAttemptError(attributionTimeoutError{}, context.Background(), context.Background())
	if got.Origin() != OriginBackendTransport || got.Reason() != ReasonUpstreamTimeout || got.Health() != HealthFailure {
		t.Fatalf("timeout classification = {%q %q %d}", got.Origin(), got.Reason(), got.Health())
	}

	got = BackendProtocolFailure(Reason("not-in-the-closed-set"))
	if got.Origin() != OriginBackendProtocol || got.Reason() != ReasonUpstreamConnectFailed || got.Health() != HealthFailure {
		t.Fatalf("invalid protocol reason classification = {%q %q %d}", got.Origin(), got.Reason(), got.Health())
	}
}

func TestDoReleasesSuccessfulNonRetainedAttempt(t *testing.T) {
	p := testPool(t, "127.0.0.1:1")
	reason, err := p.Do(context.Background(), RetryRequest{Replayable: true, Deadline: time.Second},
		func(context.Context, Attempt, int) AttemptResult { return AttemptResult{} })
	if err != nil || reason != StopSuccess {
		t.Fatalf("Do = %q, %v; want success", reason, err)
	}
	if got := p.Backends()[0].Inflight(); got != 0 {
		t.Fatalf("successful non-retained attempt left in-flight=%d", got)
	}
}

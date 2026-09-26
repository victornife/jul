// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"io"
	"net"
	"testing"
	"time"

	"jul/internal/config"
)

func pingRelay(t *testing.T, c net.Conn, msg string) error {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	_, err := io.ReadFull(c, buf)
	return err
}

// TestReloadRemovingListenerDoesNotWaitForActiveSessions pins the #428 L4
// drain boundary. Removing a [[stream]] listener stops accepting at once and
// must not block Reload — it runs on the HTTP reload goroutine, so blocking it
// on a long-lived session stalled every later reload and wedged shutdown. The
// established session keeps relaying until it ends on its own, and process
// shutdown bounds it by shutdownDrainGrace.
func TestReloadRemovingListenerDoesNotWaitForActiveSessions(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)

	prev := shutdownDrainGrace
	shutdownDrainGrace = 200 * time.Millisecond
	defer func() { shutdownDrainGrace = prev }()

	s := NewServer(Options{Logger: discardLogger()})
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: backend}}, nil); err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if err := pingRelay(t, client, "before"); err != nil {
		t.Fatalf("relay before reload: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Reload(nil, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("removing reload: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Reload blocked on an active session of a removed listener")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("removed listener still accepts new connections")
	}
	if err := pingRelay(t, client, "draining"); err != nil {
		t.Fatalf("established session cut by listener removal: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not bound the removed listener's drain")
	}
	if err := pingRelay(t, client, "after"); err == nil {
		t.Fatal("session outlived the shutdown drain grace")
	}
}

// TestCloseWaitsForSessionsThatEndWithinGrace proves the shutdown bound only
// closes sessions that outlive it: one that ends inside the grace completes
// normally and Close returns as soon as it does.
func TestCloseWaitsForSessionsThatEndWithinGrace(t *testing.T) {
	backend, stop := tcpEcho(t)
	defer stop()
	addr := freeTCPAddr(t)
	s := NewServer(Options{Logger: discardLogger()})
	if err := s.Reload([]config.StreamServer{{Listen: addr, Protocol: "tcp", ProxyPass: backend}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := pingRelay(t, client, "x"); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before an in-grace session ended")
	case <-time.After(100 * time.Millisecond):
	}
	_ = client.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the session ended")
	}
}

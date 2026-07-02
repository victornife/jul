package cache

import (
	"sync"
	"testing"
)

// TestDiskStoreConcurrentGetSet exercises concurrent get and set on the
// same key and on distinct keys, targeting the mutex-protected map/LRU.
// If a race existed in the map or list mutation, the race detector
// (-race) would flag it because the goroutines are truly concurrent
// (not merely serialized by the same mutex across distinct operations).
func TestDiskStoreConcurrentGetSet(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	const keys = 50
	const rounds = 100

	var wg sync.WaitGroup
	for i := 0; i < keys; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := "key-" + string(rune('0'+id%10))
			val := &Entry{Status: 200, Body: []byte(key)}
			for r := 0; r < rounds; r++ {
				d.set(key, val)
				d.get(key)
			}
		}(i)
	}
	wg.Wait()
}

// TestDiskStoreConcurrentDel interleaves deletes with gets on the same key.
func TestDiskStoreConcurrentDel(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Pre-populate
	d.set("x", &Entry{Status: 200, Body: []byte("v1")})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				d.get("x")
				d.del("x")
			}
		}()
	}
	wg.Wait()
}

// TestDiskStoreConcurrentPurgeDuringAccess interleaves purge with writes.
func TestDiskStoreConcurrentPurgeDuringAccess(t *testing.T) {
	dir := t.TempDir()
	d, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.set("k", &Entry{Status: 200, Body: []byte("v")})
			}
		}(i)
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				d.purge()
			}
		}()
	}
	wg.Wait()
}

// TestDiskStoreConcurrentRehydrate spins up a second diskStore over the
// same directory while writes are still in flight. The goal is to stress
// the rehydrate path and ensure no panic or data corruption.
func TestDiskStoreConcurrentRehydrate(t *testing.T) {
	dir := t.TempDir()
	d1, err := newDiskStore(dir, 1<<20, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d1.set("k", &Entry{Status: 200, Body: []byte("v1")})
			// Deliberate interleave purge to exercise the purge/rehydrate gap.
			if i%50 == 0 {
				d1.purge()
			}
		}
	}()

	// Multiple concurrent rehydrates
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = newDiskStore(dir, 1<<20, testLogger())
			}
		}()
	}

	wg.Wait()
}

// TestDiskStoreConcurrentOverflowEviction verifies that the LRU eviction
// path is safe when multiple goroutines are writing entries that exceed
// the tiny capacity budget.
func TestDiskStoreConcurrentOverflowEviction(t *testing.T) {
	dir := t.TempDir()
	// Tiny maxBytes to force eviction on almost every set.
	d, err := newDiskStore(dir, 256, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				body := make([]byte, 80) // each entry is ~80 bytes
				body[0] = byte(id)
				d.set("k-"+string(rune('0'+id%10)), &Entry{Status: 200, Body: body})
			}
		}(i)
	}
	wg.Wait()

	if d.curBytes > d.maxBytes {
		t.Errorf("curBytes=%d > maxBytes=%d", d.curBytes, d.maxBytes)
	}
}

package cache

import (
	"runtime"
	"sync"
	"testing"
)

// raceScale returns a divisor for goroutine/iteration counts in tight-loop
// concurrent tests. On resource-constrained CI runners (≤4 vCPU) the race
// detector's shadow memory multiplies peak footprint, so we scale down to
// keep the test fast but still exercise the concurrency paths.
func raceScale() int {
	if runtime.NumCPU() <= 4 {
		return 4
	}
	return 1
}

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

	s := raceScale()
	keys := 50 / s
	if keys < 4 {
		keys = 4
	}
	rounds := 100 / s
	if rounds < 10 {
		rounds = 10
	}

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

	s := raceScale()
	workers := 20 / s
	if workers < 2 {
		workers = 2
	}
	rounds := 200 / s
	if rounds < 20 {
		rounds = 20
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
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

	s := raceScale()
	writers := 10 / s
	if writers < 2 {
		writers = 2
	}
	purgers := 5 / s
	if purgers < 1 {
		purgers = 1
	}
	writeRounds := 50 / s
	if writeRounds < 5 {
		writeRounds = 5
	}
	purgeRounds := 30 / s
	if purgeRounds < 3 {
		purgeRounds = 3
	}

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < writeRounds; j++ {
				d.set("k", &Entry{Status: 200, Body: []byte("v")})
			}
		}(i)
	}
	for i := 0; i < purgers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < purgeRounds; j++ {
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

	s := raceScale()
	writeRounds := 200 / s
	if writeRounds < 20 {
		writeRounds = 20
	}
	rehydrateWorkers := 3 / s
	if rehydrateWorkers < 1 {
		rehydrateWorkers = 1
	}
	rehydrateRounds := 10 / s
	if rehydrateRounds < 2 {
		rehydrateRounds = 2
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < writeRounds; i++ {
			d1.set("k", &Entry{Status: 200, Body: []byte("v1")})
			// Deliberate interleave purge to exercise the purge/rehydrate gap.
			if i%50 == 0 {
				d1.purge()
			}
		}
	}()

	// Multiple concurrent rehydrates
	for i := 0; i < rehydrateWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rehydrateRounds; j++ {
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

	s := raceScale()
	workers := 20 / s
	if workers < 4 {
		workers = 4
	}
	rounds := 30 / s
	if rounds < 5 {
		rounds = 5
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
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

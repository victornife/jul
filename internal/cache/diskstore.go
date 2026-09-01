// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"jul/internal/atomicfile"
)

// diskStore is the overflow tier: a size-bounded, content-addressed file cache.
// Each entry is gob-encoded into a file named by the SHA-256 of its key, so a
// lookup can locate (and decode) a file purely from the request key, surviving
// restarts.
type diskStore struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	curBytes int64
	ll       *list.List // front = most recently used; values are *diskItem
	items    map[string]*list.Element
	log      *slog.Logger
}

type diskItem struct {
	hash string
	size int64
}

func newDiskStore(dir string, maxBytes int64, logger *slog.Logger) (*diskStore, error) {
	// 0o700: cached response bodies may carry sensitive content, so the cache
	// directory is owned by and readable only to the server's user.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	if logger == nil {
		logger = slog.Default()
	}
	d := &diskStore{
		dir:      dir,
		maxBytes: maxBytes,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		log:      logger,
	}
	d.rehydrate()
	return d, nil
}

// isCacheFile reports whether name matches the cache's own file-naming scheme:
// the lowercase hex SHA-256 of a key (64 hex chars). Restricting rehydrate and
// eviction to such names means a directory that also holds unrelated files (an
// operator pointing disk_path at a shared or pre-populated directory) never has
// those foreign files indexed, served, or — critically — deleted by LRU
// eviction. The atomicfile temp files (".<name>.tmp-*") also fail this check and
// are ignored.
func isCacheFile(name string) bool {
	if len(name) != 2*sha256.Size {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// rehydrate seeds the LRU index from existing files (ordered oldest-first by
// mtime) so disk usage is bounded across process restarts. Only files that match
// the cache's own naming scheme are indexed; foreign files are left untouched so
// the cache never serves or evicts data it did not write.
func (d *diskStore) rehydrate() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	type fi struct {
		hash string
		size int64
		mod  int64
	}
	var files []fi
	var foreign int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !isCacheFile(e.Name()) {
			foreign++
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fi{hash: e.Name(), size: info.Size(), mod: info.ModTime().UnixNano()})
	}
	if foreign > 0 {
		d.log.Warn("cache: ignoring foreign files in disk cache directory",
			"dir", d.dir, "count", foreign,
			"hint", "point cache disk_path at a directory dedicated to the cache")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files {
		el := d.ll.PushFront(&diskItem{hash: f.hash, size: f.size})
		d.items[f.hash] = el
		d.curBytes += f.size
	}
	d.evictLocked()
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (d *diskStore) path(hash string) string { return filepath.Join(d.dir, hash) }

func (d *diskStore) get(key string) (*Entry, bool) {
	hash := hashKey(key)
	d.mu.Lock()
	el, ok := d.items[hash]
	if ok {
		d.ll.MoveToFront(el)
	}
	d.mu.Unlock()
	if !ok {
		return nil, false
	}

	data, err := os.ReadFile(d.path(hash))
	if err != nil {
		d.del(key)
		return nil, false
	}
	var e Entry
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&e); err != nil {
		d.del(key)
		return nil, false
	}
	return &e, true
}

func (d *diskStore) set(key string, e *Entry) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(e); err != nil {
		return
	}
	hash := hashKey(key)
	// Atomic, crash-safe, owner-only (0o600): a temp file in the same directory
	// is fsync'd and renamed over the target, so a reader or a restart never sees
	// a half-written entry and the file is never world-readable.
	if err := atomicfile.Write(d.path(hash), buf.Bytes(), 0o600); err != nil {
		d.log.Warn("cache: disk write failed", "dir", d.dir, "error", err)
		return
	}
	size := int64(buf.Len())

	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		d.curBytes += size - it.size
		it.size = size
		d.ll.MoveToFront(el)
	} else {
		el := d.ll.PushFront(&diskItem{hash: hash, size: size})
		d.items[hash] = el
		d.curBytes += size
	}
	d.evictLocked()
}

func (d *diskStore) evictLocked() {
	for d.curBytes > d.maxBytes && d.ll.Len() > 0 {
		el := d.ll.Back()
		it := el.Value.(*diskItem)
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
		_ = os.Remove(d.path(it.hash))
	}
}

func (d *diskStore) del(key string) {
	hash := hashKey(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
	}
	_ = os.Remove(d.path(hash))
}

func (d *diskStore) purge() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for hash := range d.items {
		_ = os.Remove(d.path(hash))
	}
	d.ll.Init()
	d.items = make(map[string]*list.Element)
	d.curBytes = 0
}

// Resize atomically changes the disk byte cap (#92). Increasing it never
// deletes. Decreasing it deletes least-recently-used cache-owned files, in
// eviction order, until curBytes <= maxBytes.
//
// Unlike evictLocked's passive, best-effort os.Remove (silently ignored — it
// runs on the normal set path, where a stray leftover file merely wastes a
// little disk until the next eviction pass), a failed removal here is not
// swallowed: it would silently misreport the new cap as enforced. Chosen,
// documented behavior: stop this resize pass at the first removal failure
// (the failed file's index entry is retained, so curBytes still reflects
// reality and a later resize/eviction can retry it) and report the count so
// the caller can surface it through Cache's bounded failure counter/logs —
// never silently claiming the limit is enforced when it is not.
func (d *diskStore) Resize(maxBytes int64) (evictedCount int, evictedBytes int64, failedRemovals int) {
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.maxBytes = maxBytes
	for d.curBytes > d.maxBytes && d.ll.Len() > 0 {
		el := d.ll.Back()
		it := el.Value.(*diskItem)
		if err := os.Remove(d.path(it.hash)); err != nil && !os.IsNotExist(err) {
			d.log.Warn("cache: disk eviction failed to remove file", "dir", d.dir, "error", err)
			failedRemovals++
			break
		}
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
		evictedCount++
		evictedBytes += it.size
	}
	return evictedCount, evictedBytes, failedRemovals
}

package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
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
}

type diskItem struct {
	hash string
	size int64
}

func newDiskStore(dir string, maxBytes int64) (*diskStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 512 << 20
	}
	d := &diskStore{
		dir:      dir,
		maxBytes: maxBytes,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
	d.rehydrate()
	return d, nil
}

// rehydrate seeds the LRU index from existing files (ordered oldest-first by
// mtime) so disk usage is bounded across process restarts.
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
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fi{hash: e.Name(), size: info.Size(), mod: info.ModTime().UnixNano()})
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
	if err := os.WriteFile(d.path(hash), buf.Bytes(), 0o644); err != nil {
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

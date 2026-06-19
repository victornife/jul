//go:build wasmplugins

package plugins

import (
	"hash/maphash"
	"sync"
)

// KVStore is the backing store for the plugin key/value host functions. The
// default implementation is an in-memory sharded map; a future version may add
// a Redis-backed store behind the same interface. Keys are already namespaced by
// plugin name before they reach the store, so implementations treat keys as
// opaque.
type KVStore interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte)
}

const kvShards = 16

// memKV is a concurrency-safe in-memory KVStore split across a fixed number of
// shards to reduce lock contention.
type memKV struct {
	seed   maphash.Seed
	shards [kvShards]struct {
		mu sync.RWMutex
		m  map[string][]byte
	}
}

func newMemKV() *memKV {
	kv := &memKV{seed: maphash.MakeSeed()}
	for i := range kv.shards {
		kv.shards[i].m = make(map[string][]byte)
	}
	return kv
}

func (kv *memKV) shard(key string) int {
	return int(maphash.String(kv.seed, key) % kvShards)
}

func (kv *memKV) Get(key string) ([]byte, bool) {
	s := &kv.shards[kv.shard(key)]
	s.mu.RLock()
	v, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true
}

func (kv *memKV) Set(key string, value []byte) {
	cp := make([]byte, len(value))
	copy(cp, value)
	s := &kv.shards[kv.shard(key)]
	s.mu.Lock()
	s.m[key] = cp
	s.mu.Unlock()
}

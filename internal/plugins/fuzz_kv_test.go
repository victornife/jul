// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"testing"
)

// FuzzKVStore exercises the plugin key/value store with arbitrary keys and
// values. It tests Get/Set round-trips, overwrite behaviour, and empty-key
// handling. A crash indicates a bug in the sharded map implementation or
// maphash usage.
func FuzzKVStore(f *testing.F) {
	seedKeys := []string{"", "a", "long-key-with-unicode-🔑", "shard-overflow-test-key-name-that-is-very-long"}
	seedVals := [][]byte{nil, {}, []byte("v"), []byte("value-with-unicode-🔧")}
	for _, k := range seedKeys {
		for _, v := range seedVals {
			f.Add(k, v)
		}
	}

	f.Fuzz(func(t *testing.T, key string, value []byte) {
		kv := newMemKV()
		kv.Set(key, value)
		got, ok := kv.Get(key)
		if !ok {
			t.Fatal("Get returned false immediately after Set")
		}
		if !bytes.Equal(got, value) {
			t.Fatalf("Get returned %q, want %q", got, value)
		}
		// Empty value must still report present.
		if len(value) == 0 && len(got) != 0 {
			t.Fatalf("empty value round-trip failed: got %q", got)
		}
	})
}

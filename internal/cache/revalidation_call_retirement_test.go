// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"testing"

	"jul/internal/config"
)

func TestOldSuccessfulValidationDoesNotInvalidateNewValidator(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	key := keyFor(http.MethodGet, "example.test", "/item")
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte("old")})
	_, gen, ok := c.getWithGen(key)
	if !ok {
		t.Fatal("old entry missing")
	}
	startGen := c.genSeq.Add(1)
	version := validationVersion{base: key, effective: key, baseGen: gen, entryGen: gen}
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte("new")})
	newCall := newRevalidateCall()
	newKey := revalidateKey{key: key, gen: 2}
	c.reMu.Lock()
	c.calls[newKey] = newCall
	c.reMu.Unlock()
	c.retireValidationIfSelected(version, startGen, newRevalidateCall())
	if newCall.invalidated.Load() {
		t.Fatal("old successful validation invalidated the newer validator")
	}
	if e, ok := c.get(key); !ok || string(e.Body) != "new" {
		t.Fatal("old successful validation removed the newer entry")
	}
}

func TestRetirementInvalidatesOtherValidatorsOfSameEntry(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	key := keyFor(http.MethodGet, "example.test", "/item")
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte("old")})
	_, gen, ok := c.getWithGen(key)
	if !ok {
		t.Fatal("old entry missing")
	}
	startGen := c.genSeq.Add(1)
	version := validationVersion{base: key, effective: key, baseGen: gen, entryGen: gen}
	other := newRevalidateCall()
	otherKey := revalidateKey{key: key, gen: 2}
	c.reMu.Lock()
	c.calls[otherKey] = other
	c.reMu.Unlock()
	c.retireValidation(version, startGen, newRevalidateCall())
	if !other.invalidated.Load() {
		t.Fatal("other validator was not invalidated by retirement")
	}
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte("after-retirement")})
	c.retireValidation(version, startGen, other)
	if e, ok := c.get(key); !ok || string(e.Body) != "after-retirement" {
		t.Fatal("retired validator removed a later publication")
	}
}

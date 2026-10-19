// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package versionid derives confidential, context-scoped effective identifiers.
// Raw configuration CAS is deliberately owned elsewhere and remains unchanged.
package versionid

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"jul/internal/config"
)

// Context is immutable after construction. It holds no provider or filesystem
// handles; status/apply/reload use only the already-resolved snapshot and memory.
type Context struct {
	key [32]byte
	id  string
}

func New(key []byte) (*Context, error) {
	if len(key) != 32 {
		return nil, errors.New("effective version key must contain exactly 32 bytes")
	}
	c := &Context{}
	copy(c.key[:], key)
	c.id = hex.EncodeToString(c.mac("context-id", nil)[:16])
	return c, nil
}

func Random() (*Context, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, errors.New("cannot generate effective version key")
	}
	return New(key[:])
}

// Process is the private boot-scoped context for internal embedding/test callers
// that do not provide a durable context. The production composition root always
// loads an explicitly provisioned durable context before it starts listeners.
var process = func() *Context {
	c, err := Random()
	if err != nil {
		panic(err)
	}
	return c
}()

func Process() *Context { return process }

func (c *Context) mac(domain string, data []byte) []byte {
	h := hmac.New(sha256.New, c.key[:])
	_, _ = h.Write([]byte("jul/versionid/v1\x00"))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(domain)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(domain))
	binary.BigEndian.PutUint64(length[:], uint64(len(data)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func (c *Context) Digest(domain string, data []byte) string {
	if c == nil {
		c = Process()
	}
	return "ev1:" + c.id + ":" + hex.EncodeToString(c.mac(domain, data))
}
func (c *Context) Config(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	data, err := config.Marshal(cfg)
	if err != nil {
		return ""
	}
	return c.Digest("effective-config", data)
}
func (c *Context) ID() string {
	if c == nil {
		return Process().id
	}
	return c.id
}
func (c Context) String() string   { return "effective-version-context:" + c.id }
func (c Context) GoString() string { return c.String() }

// Parse validates the entire format, including canonical lowercase hex. A
// legacy value or an unknown future format is not a comparable identifier.
func Parse(value string) (string, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "ev1" || len(parts[1]) != 32 || len(parts[2]) != 64 {
		return "", false
	}
	for _, part := range parts[1:] {
		if strings.ToLower(part) != part {
			return "", false
		}
		if _, err := hex.DecodeString(part); err != nil {
			return "", false
		}
	}
	return parts[1], true
}
func Comparison(a, b string) string {
	ca, oka := Parse(a)
	cb, okb := Parse(b)
	if !oka || !okb {
		return "unverifiable"
	}
	if ca != cb {
		return "different_context"
	}
	return "same_context"
}

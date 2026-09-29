// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// basicAuth validates HTTP Basic credentials against an htpasswd file of
// bcrypt-hashed passwords.
type basicAuth struct {
	realm string
	users map[string]string // username -> bcrypt hash
}

// newBasicAuth loads an htpasswd file. Only bcrypt entries ($2a$/$2b$/$2y$) are
// supported; other hash schemes are rejected at load so a misconfiguration is
// caught at startup rather than silently denying every request.
func newBasicAuth(file, realm string) (*basicAuth, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("basic auth: read htpasswd %q: %w", file, err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("basic auth: htpasswd %q must be a regular file", file)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("basic auth: read htpasswd %q: %w", file, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("basic auth: close htpasswd %q: %w", file, closeErr)
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("basic auth: htpasswd %q exceeds 1 MiB", file)
	}
	users := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; sc.Scan(); line++ {
		row := strings.TrimSpace(sc.Text())
		if row == "" || strings.HasPrefix(row, "#") {
			continue
		}
		user, hash, ok := strings.Cut(row, ":")
		if !ok || user == "" {
			return nil, fmt.Errorf("basic auth: %q line %d: malformed entry (want user:hash)", file, line)
		}
		if !isBcryptHash(hash) {
			return nil, fmt.Errorf("basic auth: %q line %d: only bcrypt hashes are supported", file, line)
		}
		if _, exists := users[user]; exists {
			return nil, fmt.Errorf("basic auth: %q line %d: duplicate username", file, line)
		}
		users[user] = hash
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("basic auth: read htpasswd %q: %w", file, err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("basic auth: htpasswd %q has no usable entries", file)
	}
	return &basicAuth{realm: realm, users: users}, nil
}

func isBcryptHash(h string) bool {
	return strings.HasPrefix(h, "$2a$") || strings.HasPrefix(h, "$2b$") || strings.HasPrefix(h, "$2y$")
}

// check validates the request's Basic credentials. It returns true when the
// username exists and the password matches the stored bcrypt hash.
func (b *basicAuth) check(r *http.Request) bool {
	if len(r.Header.Values("Authorization")) != 1 {
		return false
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	hash, known := b.users[user]
	if !known {
		// Run a bcrypt comparison against a fixed dummy hash so the response
		// time does not reveal whether the username exists.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(pass))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil
}

// dummyBcryptHash is a valid bcrypt hash (of a random password) used to keep the
// failed-lookup path doing the same work as a real comparison, mitigating
// username-enumeration timing attacks.
const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// challenge writes a 401 response with a WWW-Authenticate header so clients are
// prompted for credentials. The realm is sent verbatim.
func (b *basicAuth) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=%q, charset=\"UTF-8\"", b.realm))
	http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
}

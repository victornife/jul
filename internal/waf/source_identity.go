// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
)

// sourceRecorder wraps the OS filesystem the rule parser reads from and
// records a digest of every file's bytes. Each read is served from the exact
// bytes that were hashed, so the digest identifies what was compiled, with no
// hash-then-reread window (#440, following #429).
type sourceRecorder struct {
	inner fs.FS

	mu    sync.Mutex
	names map[string]struct{}
	chain bytes.Buffer
}

func newSourceRecorder(inner fs.FS) *sourceRecorder {
	return &sourceRecorder{inner: inner, names: map[string]struct{}{}}
}

func (r *sourceRecorder) record(name string, data []byte) {
	sum := sha256.Sum256(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names[name] = struct{}{}
	r.chain.WriteString("sha256:")
	r.chain.WriteString(hex.EncodeToString(sum[:]))
	r.chain.WriteByte('\n')
}

// identity returns the number of distinct files read and the aggregate
// digest over the ordered per-read digests ("" when nothing was read).
func (r *sourceRecorder) identity() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.names) == 0 {
		return 0, ""
	}
	sum := sha256.Sum256(r.chain.Bytes())
	return len(r.names), "sha256:" + hex.EncodeToString(sum[:])
}

func (r *sourceRecorder) Open(name string) (fs.File, error) {
	f, err := r.inner.Open(name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.IsDir() {
		return f, nil
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	r.record(name, data)
	return &memFile{Reader: bytes.NewReader(data), info: st}, nil
}

func (r *sourceRecorder) ReadFile(name string) ([]byte, error) {
	data, err := fs.ReadFile(r.inner, name)
	if err != nil {
		return nil, err
	}
	r.record(name, data)
	return data, nil
}

func (r *sourceRecorder) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(r.inner, name)
}

func (r *sourceRecorder) Glob(pattern string) ([]string, error) {
	return fs.Glob(r.inner, pattern)
}

func (r *sourceRecorder) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(r.inner, name)
}

// memFile serves already-read file bytes.
type memFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (m *memFile) Stat() (fs.FileInfo, error) { return m.info, nil }
func (m *memFile) Close() error               { return nil }

// ruleCounter classifies rules as the parser adds them, by source file:
// embedded assets are "@"-prefixed, inline directives have no file or the
// parser's "_inline_" marker, anything else was read from disk.
type ruleCounter struct{ counts RuleCounts }

func (c *ruleCounter) observe(file string) {
	c.counts.Total++
	switch {
	case file == "" || file == "_inline_":
		c.counts.Inline++
	case strings.HasPrefix(file, "@"):
		c.counts.Embedded++
	default:
		c.counts.External++
	}
}

var crsSignature = regexp.MustCompile(`SecComponentSignature\s+"OWASP_CRS/([0-9][0-9A-Za-z.\-]*)"`)

// EmbeddedCRSVersion returns the version of the OWASP CRS compiled into this
// binary, read from the embedded rule set's own component signature — the
// rules Jul actually loads, not a duplicated constant. "" if unreadable.
var EmbeddedCRSVersion = sync.OnceValue(func() string {
	data, err := fs.ReadFile(coreruleset.FS, "@owasp_crs/REQUEST-901-INITIALIZATION.conf")
	if err != nil {
		return ""
	}
	m := crsSignature.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
})

// EngineVersion returns the linked Coraza module version from the binary's
// build information, or "" when the build carries none.
var EngineVersion = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/corazawaf/coraza/v3" {
			if dep.Replace != nil {
				dep = dep.Replace
			}
			return dep.Version
		}
	}
	return ""
})

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// BodyReader provides sequential streaming, bounded random access (io.ReaderAt
// and io.Seeker), and known byte length over an immutable cached
// representation's body while holding a lifetime lease on its backing storage
// (#525 Part 2, ADR 0023).
//
// Callers MUST call Close when finished serving the response so disk-backed
// entries can close their file descriptor and complete any deferred unlink.
type BodyReader interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
	Len() int64
}

type memBodyReader struct {
	*bytes.Reader
}

func (r memBodyReader) Len() int64   { return r.Size() }
func (r memBodyReader) Close() error { return nil }

var retiredBlobSeq atomic.Uint64

// diskBlob is the reference-counted lifetime lease for a published on-disk
// cache file. Active readers open independent *os.File handles against path;
// if the entry is evicted, invalidated, purged, or replaced while readers are
// active, retire renames the file to a unique same-directory retired path and
// defers os.Remove until the last active reader closes its handle.
type diskBlob struct {
	mu             sync.Mutex
	store          *diskStore
	hash           string
	dir            string
	path           string
	pendingTarget  string
	bodyLen        int64
	fileSize       int64
	readers        int32
	unlinked       bool
	removed        bool
	countedRetired bool
	onPromote      func()
}

func (b *diskBlob) remove(path string) error {
	if b.store != nil {
		return b.store.removePath(path)
	}
	return os.Remove(path)
}

func (b *diskBlob) rename(oldpath, newpath string) error {
	if b.store != nil {
		return b.store.renamePath(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (b *diskBlob) acquireLease() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unlinked || b.removed {
		return false
	}
	b.readers++
	return true
}

func (b *diskBlob) openAcquiredReader() (*diskBodyReader, error) {
	b.mu.Lock()
	f, err := os.Open(b.path)
	bodyLen := b.bodyLen
	b.mu.Unlock()
	if err != nil {
		b.releaseReader()
		return nil, err
	}
	return &diskBodyReader{
		SectionReader: io.NewSectionReader(f, 0, bodyLen),
		file:          f,
		blob:          b,
	}, nil
}

func (b *diskBlob) openReader() (BodyReader, error) {
	if !b.acquireLease() {
		return nil, os.ErrNotExist
	}
	return b.openAcquiredReader()
}

// retire marks b as unlinked from the cache index. It returns
// unlinkedImmediately=true when no readers are active and the backing file was
// removed from disk immediately; unlinkedImmediately=false with err=nil when
// active readers hold open leases so physical removal is deferred to the last
// reader's Close; or err!=nil when immediate removal failed and the file
// remains on disk.
func (b *diskBlob) retire() (unlinkedImmediately bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.removed {
		return true, nil
	}
	if b.unlinked {
		return false, nil
	}
	if b.readers == 0 {
		err := b.remove(b.path)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		b.unlinked = true
		b.removed = true
		return true, nil
	}
	b.unlinked = true
	retiredPath := filepath.Join(b.dir, fmt.Sprintf(".jul-cache-retired-%d", retiredBlobSeq.Add(1)))
	if err := b.rename(b.path, retiredPath); err == nil {
		b.path = retiredPath
	}
	return false, nil
}

func (b *diskBlob) isPinning(targetPath string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.unlinked && !b.removed && b.path == targetPath
}

func (b *diskBlob) isRemoved() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.removed
}

func (b *diskBlob) setOnClose(fn func()) {
	b.mu.Lock()
	if b.removed {
		b.mu.Unlock()
		fn()
		return
	}
	prev := b.onPromote
	b.onPromote = func() {
		if prev != nil {
			prev()
		}
		fn()
	}
	b.mu.Unlock()
}

func (b *diskBlob) promote(targetPath string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unlinked || b.removed {
		return false
	}
	if b.path == targetPath {
		b.pendingTarget = ""
		return true
	}
	if err := b.rename(b.path, targetPath); err == nil {
		b.path = targetPath
		b.pendingTarget = ""
		return true
	}
	b.pendingTarget = targetPath
	return false
}

func (b *diskBlob) releaseReader() {
	b.mu.Lock()
	b.readers--
	if b.readers == 0 && !b.unlinked && b.pendingTarget != "" {
		if err := b.rename(b.path, b.pendingTarget); err == nil {
			b.path = b.pendingTarget
			b.pendingTarget = ""
		}
	}
	shouldRemove := b.readers == 0 && b.unlinked && !b.removed
	path := b.path
	store := b.store
	var hook func()
	if store != nil {
		hook = store.onBeforeLastReaderRemove
	}
	b.mu.Unlock()
	if shouldRemove {
		if hook != nil {
			hook()
		}
		if err := b.remove(path); err != nil && !os.IsNotExist(err) {
			if store != nil {
				store.onDeferredRemoveFailed(b, path, err)
			}
			return
		}
		b.mu.Lock()
		b.removed = true
		onPromote := b.onPromote
		b.onPromote = nil
		b.mu.Unlock()
		if onPromote != nil {
			onPromote()
		}
		if store != nil {
			store.onRetiredBlobClosed(b.hash, b)
		}
	}
}

func (b *diskBlob) retryDeferredRemove() bool {
	b.mu.Lock()
	if b.removed || !b.unlinked || b.readers > 0 {
		b.mu.Unlock()
		return b.removed
	}
	path := b.path
	b.mu.Unlock()
	if err := b.remove(path); err != nil && !os.IsNotExist(err) {
		return false
	}
	b.mu.Lock()
	b.removed = true
	onPromote := b.onPromote
	b.onPromote = nil
	b.mu.Unlock()
	if onPromote != nil {
		onPromote()
	}
	return true
}

type diskBodyReader struct {
	*io.SectionReader
	file *os.File
	blob *diskBlob
	once sync.Once
}

func (r *diskBodyReader) Len() int64 { return r.Size() }

func (r *diskBodyReader) Close() error {
	var err error
	r.once.Do(func() {
		err = r.file.Close()
		r.blob.releaseReader()
	})
	return err
}

const streamCopyChunkBytes = 32 << 10

var streamCopyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, streamCopyChunkBytes)
		return &buf
	},
}

// onlyWriter hides io.ReaderFrom on an http.ResponseWriter when streaming a
// disk-backed body via io.CopyBuffer so a writer such as httptest.ResponseRecorder
// consumes the fixed 32 KiB pooled buffer chunk by chunk rather than calling
// bytes.Buffer.ReadFrom without the bounded buffer.
type onlyWriter struct {
	io.Writer
}

func copyWithPooledBuffer(dst io.Writer, src io.Reader) (int64, error) {
	bufPtr := streamCopyBufPool.Get().(*[]byte)
	defer streamCopyBufPool.Put(bufPtr)
	return io.CopyBuffer(onlyWriter{Writer: dst}, src, *bufPtr)
}

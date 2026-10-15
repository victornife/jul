// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/logthrottle"
)

const (
	// diskFormatMagic is the 8-byte trailer magic for ADR 0023's framed on-disk
	// cache format: [raw body bytes][gob(diskEntryMeta)][metaLen:8][bodyLen:8]["JULDCH02":8].
	diskFormatMagic     = "JULDCH02"
	diskTrailerSize     = 24
	maxDiskMetaBytes    = 1 << 20 // 1 MiB ceiling on encoded entry metadata
	maxDiskMetaEnvelope = maxDiskMetaBytes + diskTrailerSize
)

var errCorruptDiskEntry = errors.New("cache: corrupt or incompatible disk cache file")

type diskEntryMeta struct {
	Status             int
	Header             http.Header
	CreatedAt          time.Time
	ExpiresAt          time.Time
	StaleUntil         time.Time
	ETag               string
	LastModified       string
	RequiresValidation bool
	MustRevalidate     bool
	SharedAuthReuse    bool
	StaleIfError       time.Duration
	HasStaleIfError    bool
	Vary               []string
	VaryValues         map[string]string
	IsVaryStub         bool
	Variants           []string
}

func encodeDiskTrailerAndMeta(e *Entry, bodyLen int64) ([]byte, error) {
	meta := diskEntryMeta{
		Status:             e.Status,
		Header:             e.Header,
		CreatedAt:          e.CreatedAt,
		ExpiresAt:          e.ExpiresAt,
		StaleUntil:         e.StaleUntil,
		ETag:               e.ETag,
		LastModified:       e.LastModified,
		RequiresValidation: e.RequiresValidation,
		MustRevalidate:     e.MustRevalidate,
		SharedAuthReuse:    e.SharedAuthReuse,
		StaleIfError:       e.StaleIfError,
		HasStaleIfError:    e.HasStaleIfError,
		Vary:               e.Vary,
		VaryValues:         e.VaryValues,
		IsVaryStub:         e.IsVaryStub,
		Variants:           e.Variants,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&meta); err != nil {
		return nil, err
	}
	metaLen := buf.Len()
	if metaLen == 0 || metaLen > maxDiskMetaBytes {
		return nil, errCorruptDiskEntry
	}
	var trailer [diskTrailerSize]byte
	binary.BigEndian.PutUint64(trailer[0:8], uint64(metaLen))
	binary.BigEndian.PutUint64(trailer[8:16], uint64(bodyLen))
	copy(trailer[16:24], diskFormatMagic)
	buf.Write(trailer[:])
	return buf.Bytes(), nil
}

func readDiskEntryMeta(path string) (entry *Entry, bodyLen, fileSize int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = f.Close() }()
	return readDiskEntryMetaFile(f)
}

func readDiskEntryMetaFile(f *os.File) (entry *Entry, bodyLen, fileSize int64, err error) {
	info, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	fileSize = info.Size()
	if fileSize < diskTrailerSize {
		return nil, 0, 0, errCorruptDiskEntry
	}
	var trailer [diskTrailerSize]byte
	if _, err := f.ReadAt(trailer[:], fileSize-diskTrailerSize); err != nil {
		return nil, 0, 0, err
	}
	if string(trailer[16:24]) != diskFormatMagic {
		return nil, 0, 0, errCorruptDiskEntry
	}
	metaLenU := binary.BigEndian.Uint64(trailer[0:8])
	bodyLenU := binary.BigEndian.Uint64(trailer[8:16])
	if metaLenU == 0 || metaLenU > maxDiskMetaBytes {
		return nil, 0, 0, errCorruptDiskEntry
	}
	if bodyLenU > uint64(math.MaxInt64-diskTrailerSize-int64(metaLenU)) {
		return nil, 0, 0, errCorruptDiskEntry
	}
	bodyLen = int64(bodyLenU)
	metaLen := int64(metaLenU)
	if bodyLen+metaLen+diskTrailerSize != fileSize {
		return nil, 0, 0, errCorruptDiskEntry
	}
	metaBuf := make([]byte, metaLen)
	if _, err := f.ReadAt(metaBuf, bodyLen); err != nil {
		return nil, 0, 0, err
	}
	var meta diskEntryMeta
	if err := gob.NewDecoder(bytes.NewReader(metaBuf)).Decode(&meta); err != nil {
		return nil, 0, 0, errCorruptDiskEntry
	}
	return &Entry{
		Status:             meta.Status,
		Header:             meta.Header,
		CreatedAt:          meta.CreatedAt,
		ExpiresAt:          meta.ExpiresAt,
		StaleUntil:         meta.StaleUntil,
		ETag:               meta.ETag,
		LastModified:       meta.LastModified,
		RequiresValidation: meta.RequiresValidation,
		MustRevalidate:     meta.MustRevalidate,
		SharedAuthReuse:    meta.SharedAuthReuse,
		StaleIfError:       meta.StaleIfError,
		HasStaleIfError:    meta.HasStaleIfError,
		Vary:               meta.Vary,
		VaryValues:         meta.VaryValues,
		IsVaryStub:         meta.IsVaryStub,
		Variants:           meta.Variants,
	}, bodyLen, fileSize, nil
}

// diskStore is the overflow and large-object tier: a size-bounded,
// content-addressed file cache (#525 Part 2, ADR 0023). Each entry is stored
// in a single trailer-framed file named by the SHA-256 of its key so a lookup
// can read metadata at the end of the file without loading the body into
// memory, and stream or slice the body at offset 0.
type diskStore struct {
	mu        sync.Mutex
	dir       string
	maxBytes  int64
	curBytes  int64 // logical LRU capacity charge (sum of it.size)
	physBytes int64 // physical on-disk bytes occupied (sum of it.fileSize)
	// evictions counts LRU-capacity evictions from evictLocked and Resize (not
	// explicit del/purge calls), for jul_cache_evictions_total{tier="disk"}.
	evictions int64
	ll        *list.List // front = most recently used; values are *diskItem
	items     map[string]*list.Element
	pinned    map[string]*diskBlob
	log       *slog.Logger

	// tempBudget bounds all concurrent temporary capture files in dir so
	// concurrent large misses and 304 rewrites cannot bypass disk_max_size.
	tempBudget captureBudget
	onSkip     func(reason string)

	// Test hooks for deterministic fault and concurrency-window injection.
	testPublishFault     string
	onLookupLeaseAcquire func()

	// writeFailLog bounds the write-failure warning: a full filesystem fails
	// every store, and one line per failure turned ENOSPC into a log flood
	// (#422 disk-pressure evidence). writeFails counts all of them.
	writeFailLog logthrottle.Limiter
	writeFails   atomic.Int64
	reported     atomic.Int64
	// failing is true while the most recent store failed (#437).
	failing atomic.Bool
}

// diskWriteFailureLogInterval is the minimum spacing of write-failure warnings.
const diskWriteFailureLogInterval = 10 * time.Second

type diskItem struct {
	hash     string
	size     int64
	fileSize int64
	bodyLen  int64
	metaLen  int64
	blob     *diskBlob
}

func (it *diskItem) effectiveSize(maxBytes int64) int64 {
	if it.bodyLen <= maxBytes && it.metaLen <= maxDiskMetaEnvelope && it.fileSize > maxBytes {
		return maxBytes
	}
	return it.fileSize
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

func (d *diskStore) maxBytesCap() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.maxBytes
}

// isCacheFile reports whether name matches the cache's own file-naming scheme:
// the lowercase hex SHA-256 of a key (64 hex chars). Restricting rehydrate and
// eviction to such names means a directory that also holds unrelated files (an
// operator pointing disk_path at a shared or pre-populated directory) never has
// those foreign files indexed, served, or — critically — deleted by LRU
// eviction.
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

// isCacheTempFile reports whether name is an incomplete temporary capture or
// retired reader artifact created by Jul's cache in disk_path. Startup
// rehydration removes these leftovers without counting or deleting foreign files.
func isCacheTempFile(name string) bool {
	if strings.HasPrefix(name, ".jul-cache-tmp-") || strings.HasPrefix(name, ".jul-cache-retired-") {
		return true
	}
	if strings.HasPrefix(name, ".") {
		if idx := strings.Index(name[1:], ".tmp-"); idx == 2*sha256.Size {
			return isCacheFile(name[1 : 1+idx])
		}
	}
	return false
}

// rehydrate seeds the LRU index from existing valid files (ordered oldest-first
// by mtime) so disk usage is bounded across process restarts. Incomplete
// temporary files and corrupt/incompatible cache files are removed; foreign
// files are left untouched.
func (d *diskStore) rehydrate() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	type fi struct {
		hash    string
		size    int64
		bodyLen int64
		mod     int64
	}
	var files []fi
	var foreign int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if isCacheTempFile(name) {
			_ = os.Remove(filepath.Join(d.dir, name))
			continue
		}
		if !isCacheFile(name) {
			foreign++
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fullPath := d.path(name)
		_, bodyLen, fileSize, err := readDiskEntryMeta(fullPath)
		if err != nil {
			_ = os.Remove(fullPath)
			continue
		}
		files = append(files, fi{hash: name, size: fileSize, bodyLen: bodyLen, mod: info.ModTime().UnixNano()})
	}
	if foreign > 0 {
		d.log.Warn("cache: ignoring foreign files in disk cache directory",
			"dir", d.dir, "count", foreign,
			"hint", "point cache disk_path at a directory dedicated to the cache")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files {
		blob := &diskBlob{
			dir:      d.dir,
			path:     d.path(f.hash),
			bodyLen:  f.bodyLen,
			fileSize: f.size,
		}
		it := &diskItem{
			hash:     f.hash,
			fileSize: f.size,
			bodyLen:  f.bodyLen,
			metaLen:  f.size - f.bodyLen,
			blob:     blob,
		}
		it.size = it.effectiveSize(d.maxBytes)
		el := d.ll.PushFront(it)
		d.items[f.hash] = el
		d.curBytes += it.size
		d.physBytes += it.fileSize
	}
	d.evictLocked()
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (d *diskStore) path(hash string) string { return filepath.Join(d.dir, hash) }

func (d *diskStore) recordWriteFailure(err error) {
	d.failing.Store(true)
	total := d.writeFails.Add(1)
	if d.writeFailLog.Allow(diskWriteFailureLogInterval) {
		suppressed := total - d.reported.Swap(total) - 1
		d.log.Warn("cache: disk write failed; entry not stored on disk", "dir", d.dir, "error", err, "failed_writes", total, "suppressed", suppressed)
	}
}

func (d *diskStore) get(key string) (*Entry, bool) {
	return d.getWithMemCap(key, math.MaxInt64)
}

func (d *diskStore) getWithMemCap(key string, memCap int64) (*Entry, bool) {
	hash := hashKey(key)
	d.mu.Lock()
	el, ok := d.items[hash]
	var blob *diskBlob
	if ok {
		blob = el.Value.(*diskItem).blob
		if blob == nil || !blob.acquireLease() {
			d.mu.Unlock()
			return nil, false
		}
		d.ll.MoveToFront(el)
	}
	hook := d.onLookupLeaseAcquire
	d.mu.Unlock()
	if !ok {
		return nil, false
	}
	if hook != nil {
		hook()
	}

	rc, err := blob.openAcquiredReader()
	if err != nil {
		d.delIfBlob(key, blob)
		return nil, false
	}
	defer func() { _ = rc.Close() }()

	e, bodyLen, _, err := readDiskEntryMetaFile(rc.file)
	if err != nil {
		d.delIfBlob(key, blob)
		return nil, false
	}
	if bodyLen <= memCap {
		if bodyLen > 0 {
			body := make([]byte, bodyLen)
			if _, err := io.ReadFull(rc, body); err != nil {
				d.delIfBlob(key, blob)
				return nil, false
			}
			e.Body = body
		}
		return e, true
	}
	e.diskBody = blob
	return e, true
}

func (d *diskStore) set(key string, e *Entry) {
	var acct captureAccount
	if e.diskBody != nil {
		acct = captureAccount{budget: &d.tempBudget, onSkip: d.onSkip}
		if e.BodyLen() > 0 && !acct.grow(int(e.BodyLen()), d.maxBytesCap()) {
			acct.skip(captureSkipBudget)
			return
		}
	}
	s := d.beginStreamCapture(d.onSkip)
	if s == nil {
		acct.release()
		return
	}
	s.acct = acct
	defer d.abortStream(s)
	if e.diskBody != nil {
		rc, err := e.diskBody.openReader()
		if err != nil {
			d.recordWriteFailure(err)
			return
		}
		n, copyErr := copyWithPooledBuffer(s.file, rc)
		_ = rc.Close()
		if copyErr != nil {
			d.recordWriteFailure(copyErr)
			return
		}
		s.bodyLen = n
	} else if len(e.Body) > 0 {
		if _, err := s.file.Write(e.Body); err != nil {
			d.recordWriteFailure(err)
			return
		}
		s.bodyLen = int64(len(e.Body))
	}
	if blob, ok := d.publishStream(key, e, s); ok && e.diskBody != nil {
		e.diskBody = blob
	}
}

var tempFileSeq atomic.Uint64

func (d *diskStore) tempPath(seq uint64) string {
	return filepath.Join(d.dir, ".jul-cache-tmp-"+strconv.FormatUint(seq, 10))
}

func (d *diskStore) retireBlobLocked(hash string, b *diskBlob) error {
	if b == nil {
		return nil
	}
	err := b.retire()
	if b.isPinning(d.path(hash)) {
		if d.pinned == nil {
			d.pinned = make(map[string]*diskBlob)
		}
		d.pinned[hash] = b
	}
	return err
}

// diskStreamCapture manages a temporary file receiving a streamed response body
// larger than the memory tier (#525 Part 2).
type diskStreamCapture struct {
	file      *os.File
	seq       uint64
	bodyLen   int64
	acct      captureAccount
	closed    bool
	published bool
}

func (d *diskStore) beginStreamCapture(onSkip func(reason string)) *diskStreamCapture {
	seq := tempFileSeq.Add(1)
	tmp, err := os.OpenFile(d.tempPath(seq), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		d.recordWriteFailure(err)
		return nil
	}
	return &diskStreamCapture{
		file: tmp,
		seq:  seq,
		acct: captureAccount{budget: &d.tempBudget, onSkip: onSkip},
	}
}

func (d *diskStore) writeStream(s *diskStreamCapture, chunks ...[]byte) (ok bool, skipReason string) {
	if d == nil || s == nil || s.closed {
		return false, ""
	}
	maxB := d.maxBytesCap()
	for _, p := range chunks {
		if len(p) == 0 {
			continue
		}
		if !s.acct.grow(len(p), maxB) {
			return false, captureSkipBudget
		}
		if _, err := s.file.Write(p); err != nil {
			d.recordWriteFailure(err)
			return false, ""
		}
		s.bodyLen += int64(len(p))
	}
	return true, ""
}

func openStreamBody(s *diskStreamCapture, bodyLen int64) (io.ReadCloser, bool) {
	if s != nil && !s.closed && s.file != nil {
		return io.NopCloser(io.NewSectionReader(s.file, 0, bodyLen)), true
	}
	return nil, false
}

func (d *diskStore) abortStream(s *diskStreamCapture) {
	if s == nil {
		return
	}
	if !s.closed {
		s.closed = true
		_ = s.file.Close()
	}
	if !s.published && s.seq != 0 && d != nil {
		// lgtm[go/path-injection] – d.dir is the operator-configured cache directory and s.seq is an internal atomic uint64 counter.
		_ = os.Remove(d.tempPath(s.seq))
		s.seq = 0
	}
	s.acct.release()
}

func (d *diskStore) publishStream(key string, e *Entry, s *diskStreamCapture) (*diskBlob, bool) {
	if s == nil || s.closed {
		return nil, false
	}
	tail, err := encodeDiskTrailerAndMeta(e, s.bodyLen)
	if err != nil {
		return nil, false
	}
	totalSize := s.bodyLen + int64(len(tail))
	fault := d.testPublishFault
	if fault == "append" {
		d.recordWriteFailure(errors.New("injected append failure"))
		return nil, false
	}
	if _, err := s.file.Write(tail); err != nil {
		d.recordWriteFailure(err)
		return nil, false
	}
	if fault == "sync" {
		d.recordWriteFailure(errors.New("injected sync failure"))
		return nil, false
	}
	if err := s.file.Sync(); err != nil {
		d.recordWriteFailure(err)
		return nil, false
	}
	if err := s.file.Close(); err != nil || fault == "close" {
		s.closed = true
		if err == nil {
			err = errors.New("injected close failure")
		}
		d.recordWriteFailure(err)
		return nil, false
	}
	s.closed = true

	hash := hashKey(key)
	targetPath := d.path(hash)
	tmpPath := d.tempPath(s.seq)

	d.mu.Lock()
	defer d.mu.Unlock()
	if s.bodyLen > d.maxBytes || int64(len(tail)) > maxDiskMetaEnvelope {
		if s.acct.onSkip != nil {
			s.acct.onSkip(captureSkipOversize)
		}
		return nil, false
	}
	if fault == "rename" {
		d.recordWriteFailure(errors.New("injected rename failure"))
		return nil, false
	}
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		_ = d.retireBlobLocked(hash, it.blob)
	}
	finalPath := targetPath
	var pinned *diskBlob
	// lgtm[go/path-injection] – tmpPath is derived from an internal uint64 sequence and targetPath from a SHA-256 hex digest under d.dir.
	if err := os.Rename(tmpPath, targetPath); err != nil {
		if p := d.pinned[hash]; p != nil && p.isPinning(targetPath) {
			pinned = p
			finalPath = tmpPath
		} else {
			if el, ok := d.items[hash]; ok {
				it := el.Value.(*diskItem)
				d.ll.Remove(el)
				delete(d.items, hash)
				d.curBytes -= it.size
				d.physBytes -= it.fileSize
			}
			d.recordWriteFailure(err)
			return nil, false
		}
	} else {
		delete(d.pinned, hash)
	}
	s.published = true
	s.acct.release()
	if d.failing.Load() {
		d.failing.Store(false)
	}

	blob := &diskBlob{
		dir:      d.dir,
		path:     finalPath,
		bodyLen:  s.bodyLen,
		fileSize: totalSize,
	}
	if pinned != nil {
		pinned.setOnClose(func() {
			blob.promote(targetPath)
		})
	}
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		it.fileSize = totalSize
		it.bodyLen = s.bodyLen
		it.metaLen = int64(len(tail))
		it.size = it.effectiveSize(d.maxBytes)
		it.blob = blob
		d.curBytes += it.size
		d.physBytes += it.fileSize
		d.ll.MoveToFront(el)
	} else {
		it := &diskItem{
			hash:     hash,
			fileSize: totalSize,
			bodyLen:  s.bodyLen,
			metaLen:  int64(len(tail)),
			blob:     blob,
		}
		it.size = it.effectiveSize(d.maxBytes)
		el := d.ll.PushFront(it)
		d.items[hash] = el
		d.curBytes += it.size
		d.physBytes += it.fileSize
	}
	d.evictLocked()
	if el, ok := d.items[hash]; !ok || el.Value.(*diskItem).blob != blob {
		return nil, false
	}
	return blob, true
}

func (d *diskStore) recomputeSizesLocked() {
	var totalCharge, totalPhys int64
	for el := d.ll.Front(); el != nil; el = el.Next() {
		it := el.Value.(*diskItem)
		it.size = it.effectiveSize(d.maxBytes)
		totalCharge += it.size
		totalPhys += it.fileSize
	}
	d.curBytes = totalCharge
	d.physBytes = totalPhys
}

func (d *diskStore) evictLocked() {
	for d.curBytes > d.maxBytes && d.ll.Len() > 0 {
		el := d.ll.Back()
		it := el.Value.(*diskItem)
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		_ = d.retireBlobLocked(it.hash, it.blob)
		d.evictions++
	}
}

// stats returns a snapshot of the tier's live physical occupancy; see memStore.stats.
func (d *diskStore) stats() (curBytes, maxBytes int64, entries int, evictions int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.physBytes, d.maxBytes, d.ll.Len(), d.evictions
}

func (d *diskStore) delIfBlob(key string, target *diskBlob) {
	hash := hashKey(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		if it.blob == target {
			d.ll.Remove(el)
			delete(d.items, it.hash)
			d.curBytes -= it.size
			d.physBytes -= it.fileSize
			_ = d.retireBlobLocked(hash, it.blob)
		}
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
		d.physBytes -= it.fileSize
		_ = d.retireBlobLocked(hash, it.blob)
		return
	}
	_ = os.Remove(d.path(hash))
}

func (d *diskStore) purge() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, el := range d.items {
		it := el.Value.(*diskItem)
		_ = d.retireBlobLocked(it.hash, it.blob)
	}
	d.ll.Init()
	d.items = make(map[string]*list.Element)
	d.curBytes = 0
	d.physBytes = 0
}

// Resize atomically changes the disk byte cap (#92). Increasing it never
// deletes. Decreasing it deletes least-recently-used cache-owned files, in
// eviction order, until within capacity.
//
// Unlike evictLocked's passive, best-effort removal, a failed removal here is
// not swallowed: it would silently misreport the new cap as enforced. Chosen,
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
	d.recomputeSizesLocked()
	for d.curBytes > d.maxBytes && d.ll.Len() > 0 {
		el := d.ll.Back()
		it := el.Value.(*diskItem)
		if err := d.retireBlobLocked(it.hash, it.blob); err != nil {
			d.log.Warn("cache: disk eviction failed to remove file", "dir", d.dir, "error", err)
			failedRemovals++
			break
		}
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		evictedCount++
		evictedBytes += it.fileSize
	}
	d.evictions += int64(evictedCount)
	return evictedCount, evictedBytes, failedRemovals
}

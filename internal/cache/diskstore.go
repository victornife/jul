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
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"jul/internal/atomicfile"
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
	Evidence           *publicationEvidence
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
	Key                string
	BaseKey            string
	Gen                uint64
	Tags               []string
	TagPolicyTracked   bool
	TagHeaders         []string
}

func encodeDiskTrailerAndMetaWithKeyGen(key string, gen uint64, e *Entry, bodyLen int64) ([]byte, error) {
	var tagHeaders []string
	if e.tagPolicyTracked && e.ingestedTagHeaderCount > 0 && e.ingestedTagHeaderCount <= len(e.ingestedTagHeaders) {
		tagHeaders = append([]string(nil), e.ingestedTagHeaders[:e.ingestedTagHeaderCount]...)
	}
	meta := diskEntryMeta{
		Status:             e.Status,
		Evidence:           e.Evidence,
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
		Key:                key,
		BaseKey:            baseKeyOf(key),
		Gen:                gen,
		Tags:               e.Tags,
		TagPolicyTracked:   e.tagPolicyTracked,
		TagHeaders:         tagHeaders,
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
	entry, _, bodyLen, fileSize, err = readDiskEntryMetaWithKey(path)
	return entry, bodyLen, fileSize, err
}

func readDiskEntryMetaWithKey(path string) (entry *Entry, key string, bodyLen, fileSize int64, err error) {
	entry, key, _, bodyLen, fileSize, err = readDiskEntryMetaWithKeyGen(path)
	return entry, key, bodyLen, fileSize, err
}

func readDiskEntryMetaWithKeyGen(path string) (entry *Entry, key string, gen uint64, bodyLen, fileSize int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", 0, 0, 0, err
	}
	defer func() { _ = f.Close() }()
	return readDiskEntryMetaFileWithKeyGen(f)
}

func readDiskEntryMetaFile(f *os.File) (entry *Entry, bodyLen, fileSize int64, err error) {
	entry, _, _, bodyLen, fileSize, err = readDiskEntryMetaFileWithKeyGen(f)
	return entry, bodyLen, fileSize, err
}

func readDiskEntryMetaFileWithKeyGen(f *os.File) (entry *Entry, key string, gen uint64, bodyLen, fileSize int64, err error) {
	info, err := f.Stat()
	if err != nil {
		return nil, "", 0, 0, 0, err
	}
	fileSize = info.Size()
	if fileSize < diskTrailerSize {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	var trailer [diskTrailerSize]byte
	if _, err := f.ReadAt(trailer[:], fileSize-diskTrailerSize); err != nil {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	if string(trailer[16:24]) != diskFormatMagic {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	metaLenU := binary.BigEndian.Uint64(trailer[0:8])
	bodyLenU := binary.BigEndian.Uint64(trailer[8:16])
	if metaLenU == 0 || metaLenU > maxDiskMetaBytes {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	if bodyLenU > uint64(math.MaxInt64-diskTrailerSize-int64(metaLenU)) {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	bodyLen = int64(bodyLenU)
	metaLen := int64(metaLenU)
	if bodyLen+metaLen+diskTrailerSize != fileSize {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	metaBuf := make([]byte, metaLen)
	if _, err := f.ReadAt(metaBuf, bodyLen); err != nil {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	var meta diskEntryMeta
	if err := gob.NewDecoder(bytes.NewReader(metaBuf)).Decode(&meta); err != nil {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	if meta.BaseKey != "" && meta.BaseKey != baseKeyOf(meta.Key) {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	if len(meta.Tags) > MaxTagsPerEntry || len(meta.TagHeaders) > 8 {
		return nil, "", 0, 0, 0, errCorruptDiskEntry
	}
	totalTagBytes := 0
	for _, t := range meta.Tags {
		if ValidateTag(t) != nil {
			return nil, "", 0, 0, 0, errCorruptDiskEntry
		}
		totalTagBytes += len(t)
		if totalTagBytes > MaxTagBytesPerEntry {
			return nil, "", 0, 0, 0, errCorruptDiskEntry
		}
	}
	var ingestedHeaders [8]string
	copy(ingestedHeaders[:], meta.TagHeaders)
	return &Entry{
		Status:                 meta.Status,
		Evidence:               meta.Evidence,
		Header:                 meta.Header,
		CreatedAt:              meta.CreatedAt,
		ExpiresAt:              meta.ExpiresAt,
		StaleUntil:             meta.StaleUntil,
		ETag:                   meta.ETag,
		LastModified:           meta.LastModified,
		RequiresValidation:     meta.RequiresValidation,
		MustRevalidate:         meta.MustRevalidate,
		SharedAuthReuse:        meta.SharedAuthReuse,
		StaleIfError:           meta.StaleIfError,
		HasStaleIfError:        meta.HasStaleIfError,
		Vary:                   meta.Vary,
		VaryValues:             meta.VaryValues,
		IsVaryStub:             meta.IsVaryStub,
		Variants:               meta.Variants,
		Tags:                   meta.Tags,
		ingestedTagHeaders:     ingestedHeaders,
		ingestedTagHeaderCount: len(meta.TagHeaders),
		tagPolicyTracked:       meta.TagPolicyTracked,
	}, meta.Key, meta.Gen, bodyLen, fileSize, nil
}

// diskStore is the overflow and large-object tier: a size-bounded,
// content-addressed file cache (#525 Part 2, ADR 0023). Each entry is stored
// in a single trailer-framed file named by the SHA-256 of its key so a lookup
// can read metadata at the end of the file without loading the body into
// memory, and stream or slice the body at offset 0.
// diskTombstoneFile records SHA-256 cache file hashes whose deletion, purge,
// or eviction removal failed so startup rehydration can retry removing them
// and never resurrect invalidated or evicted entries across restarts.
const (
	diskTombstoneFile   = ".jul-cache-tombstones"
	diskTombstoneMagic  = "JULTOMB1"
	diskPurgeLedgerFile = ".jul-cache-purge-ledger"
	diskHighWaterFile   = ".jul-cache-gen"
)

type diskStore struct {
	mu             sync.Mutex
	dir            string
	maxBytes       int64
	curBytes       int64 // logical LRU capacity charge (sum of it.size)
	physBytes      int64 // physical on-disk bytes of indexed entries (sum of it.fileSize)
	retiredBytes   int64 // physical on-disk bytes of retired entries still held by active readers
	tombstoneBytes int64 // physical on-disk bytes of unremoved tombstoned files awaiting retry
	// evictions counts LRU-capacity evictions from evictLocked and Resize (not
	// explicit del/purge calls), for jul_cache_evictions_total{tier="disk"}.
	onEviction                     func(string)
	evictions                      int64
	ll                             *list.List // front = most recently used; values are *diskItem
	items                          map[string]*list.Element
	pinned                         map[string]*diskBlob
	tombstones                     map[string]int64
	hashKeys                       map[string]string
	highWaterGen                   uint64
	tombstoneDirty                 bool
	corruptTombstoneCleanupPending bool
	failedRetired                  map[*diskBlob]struct{}
	activeTempFiles                sync.Map
	log                            *slog.Logger

	// tempBudget bounds all concurrent temporary capture files in dir so
	// concurrent large misses, memory write-throughs, Vary stubs, and 304
	// rewrites cannot bypass disk_max_size.
	tempBudget                captureBudget
	onSkip                    func(reason string)
	checkGen                  func(key string, gen uint64) bool
	canCommit                 func(key string, gen, startGen uint64, hasStartGen bool, e *Entry, extraTags []string) bool
	onKeyDiskState            func(key string, inDisk bool)
	onRehydrate               func(items []diskItemSnapshot)
	unrecoverableInvalidation atomic.Bool

	// Test hooks for deterministic fault and concurrency-window injection.
	testPublishFault         string
	testRemoveFile           func(path string) error
	testRenameFile           func(oldpath, newpath string) error
	testTruncateFile         func(path string, size int64) error
	testReadTombstoneFile    func(path string) ([]byte, error)
	testReadDir              func(path string) ([]os.DirEntry, error)
	testSyncDir              func(dir string) error
	testPersistTombstoneErr  error
	testWriteHighWaterErr    error
	onLookupLeaseAcquire     func()
	onBeforePublishLock      func(key string, gen uint64)
	onBeforeLastReaderRemove func()

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
	hash             string
	key              string
	gen              uint64
	size             int64
	fileSize         int64
	bodyLen          int64
	metaLen          int64
	isVaryStub       bool
	variants         []string
	tags             []string
	tagPolicyTracked bool
	tagHeaders       [8]string
	tagHeaderCount   int
	createdAt        time.Time
	evidence         *publicationEvidence
	expiresAt        time.Time
	staleUntil       time.Time
	varyFields       []string
	blob             *diskBlob
}

type diskItemSnapshot struct {
	Key              string
	Gen              uint64
	FileSize         int64
	BodyLen          int64
	IsVaryStub       bool
	Variants         []string
	Tags             []string
	TagPolicyTracked bool
	TagHeaders       [8]string
	TagHeaderCount   int
	CreatedAt        time.Time
	Evidence         *publicationEvidence
	ExpiresAt        time.Time
	StaleUntil       time.Time
	VaryFields       []string
}

func (it *diskItem) effectiveSize(maxBytes int64) int64 {
	if it.isVaryStub && it.bodyLen == 0 && it.metaLen <= maxDiskMetaEnvelope {
		return 0
	}
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
		dir:        dir,
		maxBytes:   maxBytes,
		ll:         list.New(),
		items:      make(map[string]*list.Element),
		tombstones: make(map[string]int64),
		hashKeys:   make(map[string]string),
		log:        logger,
	}
	d.rehydrate()
	return d, nil
}

func (d *diskStore) maxBytesCap() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.maxBytes
}

func (d *diskStore) availableBudgetCap() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	avail := d.maxBytes - d.retiredBytes - d.tombstoneBytes
	if avail < 0 {
		return 0
	}
	return avail
}

func (d *diskStore) effectiveUsedLocked() int64 {
	return d.curBytes + d.retiredBytes + d.tombstoneBytes
}

func (d *diskStore) removePath(path string) error {
	if d.testRemoveFile != nil {
		return d.testRemoveFile(path)
	}
	return os.Remove(path)
}

func (d *diskStore) renamePath(oldpath, newpath string) error {
	if d.testRenameFile != nil {
		return d.testRenameFile(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (d *diskStore) truncatePath(path string, size int64) error {
	if d.testTruncateFile != nil {
		return d.testTruncateFile(path, size)
	}
	return os.Truncate(path, size)
}

func (d *diskStore) readTombstoneManifest(path string) ([]byte, error) {
	if d.testReadTombstoneFile != nil {
		return d.testReadTombstoneFile(path)
	}
	return os.ReadFile(path)
}

func (d *diskStore) readDir(path string) ([]os.DirEntry, error) {
	if d.testReadDir != nil {
		return d.testReadDir(path)
	}
	return os.ReadDir(path)
}

func (d *diskStore) rememberHashKeyLocked(hash, key string) {
	if hash == "" || key == "" {
		return
	}
	if d.hashKeys == nil {
		d.hashKeys = make(map[string]string)
	}
	d.hashKeys[hash] = key
}

func (d *diskStore) hasHashStateLocked(hash string) bool {
	if _, ok := d.items[hash]; ok {
		return true
	}
	if d.pinned[hash] != nil {
		return true
	}
	if _, ok := d.tombstones[hash]; ok {
		return true
	}
	return false
}

func (d *diskStore) notifyKeyDiskStateLocked(key, hash string) {
	if key == "" && d.hashKeys != nil {
		key = d.hashKeys[hash]
	}
	inDisk := d.hasHashStateLocked(hash)
	if inDisk {
		d.rememberHashKeyLocked(hash, key)
	} else if d.hashKeys != nil {
		delete(d.hashKeys, hash)
	}
	if key != "" && d.onKeyDiskState != nil {
		d.onKeyDiskState(key, inDisk)
	}
}

func (d *diskStore) newBlob(hash, path string, bodyLen, fileSize int64) *diskBlob {
	return &diskBlob{
		store:    d,
		hash:     hash,
		dir:      d.dir,
		path:     path,
		bodyLen:  bodyLen,
		fileSize: fileSize,
	}
}

func (d *diskStore) syncDirLocked() error {
	if d.testSyncDir != nil {
		return d.testSyncDir(d.dir)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dirHandle, err := os.Open(d.dir)
	if err != nil {
		return err
	}
	syncErr := dirHandle.Sync()
	closeErr := dirHandle.Close()
	if syncErr != nil {
		if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.EBADF) || errors.Is(syncErr, syscall.ENOSYS) {
			return nil
		}
		return syncErr
	}
	return closeErr
}

func (d *diskStore) recordHighWaterGen(gen uint64) error {
	if d == nil || gen == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.testWriteHighWaterErr != nil {
		d.recordWriteFailure(d.testWriteHighWaterErr)
		return d.testWriteHighWaterErr
	}
	if gen > d.highWaterGen {
		d.highWaterGen = gen
	}
	if err := atomicfile.Write(filepath.Join(d.dir, diskHighWaterFile), []byte(strconv.FormatUint(d.highWaterGen, 10)+"\n"), 0o600); err != nil {
		d.recordWriteFailure(err)
		return err
	}
	return nil
}

func (d *diskStore) highWaterMark() uint64 {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.highWaterGen
}

func (d *diskStore) persistTombstonesLocked() error {
	if d.testPersistTombstoneErr != nil {
		d.tombstoneDirty = true
		return d.testPersistTombstoneErr
	}
	tombPath := filepath.Join(d.dir, diskTombstoneFile)
	if len(d.tombstones) == 0 {
		err := os.Remove(tombPath)
		if err != nil && !os.IsNotExist(err) {
			d.tombstoneDirty = true
			return err
		}
		if err == nil || d.tombstoneDirty {
			if sErr := d.syncDirLocked(); sErr != nil {
				d.tombstoneDirty = true
				return sErr
			}
		}
		d.tombstoneDirty = false
		return nil
	}
	var sb strings.Builder
	count := 0
	for h, sz := range d.tombstones {
		sb.WriteString(h)
		sb.WriteByte(' ')
		sb.WriteString(strconv.FormatInt(sz, 10))
		sb.WriteByte('\n')
		count++
	}
	sb.WriteString(diskTombstoneMagic)
	sb.WriteByte(' ')
	sb.WriteString(strconv.Itoa(count))
	sb.WriteByte('\n')

	if err := atomicfile.Write(tombPath, []byte(sb.String()), 0o600); err != nil {
		d.tombstoneDirty = true
		return err
	}
	if sErr := d.syncDirLocked(); sErr != nil {
		d.tombstoneDirty = true
		return sErr
	}
	d.tombstoneDirty = false
	return nil
}

func (d *diskStore) recordTombstoneLocked(hash string, fileSize int64) {
	if d.tombstones == nil {
		d.tombstones = make(map[string]int64)
	}
	if prev, exists := d.tombstones[hash]; exists {
		d.tombstoneBytes -= prev
	}
	d.tombstones[hash] = fileSize
	d.tombstoneBytes += fileSize
	if err := d.persistTombstonesLocked(); err != nil {
		if p := d.pinned[hash]; p != nil && p.hasActiveReaders() {
			// Active readers still hold the canonical file open: preserve their
			// contents (do not truncate) and mark invalidation unrecoverable
			// until the manifest can be persisted or the last reader unlinks it.
			d.unrecoverableInvalidation.Store(true)
			d.recordWriteFailure(err)
			return
		}
		// Fail-closed fallback: truncate the invalidated cache file in place so
		// its 24-byte JULDCH02 trailer is destroyed even when creating a new
		// temporary tombstone manifest file fails.
		if tErr := d.truncatePath(d.path(hash), 0); tErr != nil && !os.IsNotExist(tErr) {
			d.unrecoverableInvalidation.Store(true)
			d.recordWriteFailure(tErr)
			d.log.Error("cache: durable invalidation failed (unlink, tombstone manifest, and truncate all failed)",
				"dir", d.dir, "error", tErr)
		}
		d.recordWriteFailure(err)
	}
}

func (d *diskStore) syncTombstoneStateLocked() {
	if err := d.persistTombstonesLocked(); err != nil {
		d.recordWriteFailure(err)
	} else if !d.corruptTombstoneCleanupPending && len(d.tombstones) == 0 && !d.tombstoneDirty {
		d.unrecoverableInvalidation.Store(false)
	}
}

func (d *diskStore) clearTombstoneLocked(hash string) {
	if prev, exists := d.tombstones[hash]; exists {
		delete(d.tombstones, hash)
		key := d.hashKeys[hash]
		d.tombstoneBytes -= prev
		d.syncTombstoneStateLocked()
		d.notifyKeyDiskStateLocked(key, hash)
	}
}

func (d *diskStore) sweepTombstonesLocked() {
	if d.corruptTombstoneCleanupPending {
		d.purgeAllCacheFilesOnCorruptTombstoneLocked()
		if d.corruptTombstoneCleanupPending {
			return
		}
	}
	for b := range d.failedRetired {
		if b.retryDeferredRemove() {
			d.onRetiredBlobClosedLocked(b.hash, b)
		}
	}
	if len(d.tombstones) == 0 && !d.tombstoneDirty {
		d.unrecoverableInvalidation.Store(false)
		return
	}
	changed := d.tombstoneDirty
	for h, sz := range d.tombstones {
		if d.pinned[h] != nil {
			continue
		}
		err := d.removePath(d.path(h))
		if err == nil || os.IsNotExist(err) {
			delete(d.tombstones, h)
			key := d.hashKeys[h]
			d.tombstoneBytes -= sz
			changed = true
			d.notifyKeyDiskStateLocked(key, h)
		} else {
			_ = d.truncatePath(d.path(h), 0)
		}
	}
	if changed {
		d.syncTombstoneStateLocked()
	}
}

// reconcileInvalidation repairs durable cleanup even when an earlier attempt
// advanced or reclaimed the cache catalog. Pinned readers may finish normally
// once their retirement is durably recorded.
func (d *diskStore) reconcileInvalidation() (remaining int, uncertain bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()
	pending := make(map[string]struct{}, len(d.tombstones)+len(d.failedRetired))
	for hash := range d.tombstones {
		if blob := d.pinned[hash]; blob != nil && blob.hasActiveReaders() {
			continue
		}
		pending[hash] = struct{}{}
	}
	for blob := range d.failedRetired {
		pending[blob.hash] = struct{}{}
	}
	uncertain = d.tombstoneDirty || d.corruptTombstoneCleanupPending
	d.unrecoverableInvalidation.Store(uncertain)
	return len(pending), uncertain
}

func (d *diskStore) dropIndexedHashLocked(hash string) {
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		d.ll.Remove(el)
		delete(d.items, hash)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		d.rememberHashKeyLocked(hash, it.key)
	}
}

func (d *diskStore) purgeCorruptTombstoneCacheFileLocked(name string, e os.DirEntry) {
	if el, ok := d.items[name]; ok {
		it := el.Value.(*diskItem)
		d.dropIndexedHashLocked(name)
		if err := d.retireBlobLocked(name, it.blob); err != nil {
			d.recordTombstoneLocked(name, it.fileSize)
			_ = d.truncatePath(d.path(name), 0)
			d.recordWriteFailure(err)
		}
		d.notifyKeyDiskStateLocked(it.key, name)
		return
	}
	if d.pinned[name] != nil {
		d.recordTombstoneLocked(name, 0)
		d.notifyKeyDiskStateLocked("", name)
		return
	}
	if err := d.removePath(d.path(name)); err != nil && !os.IsNotExist(err) {
		var sz int64
		if info, stErr := e.Info(); stErr == nil {
			sz = info.Size()
		}
		d.recordTombstoneLocked(name, sz)
		_ = d.truncatePath(d.path(name), 0)
	}
	d.notifyKeyDiskStateLocked("", name)
}

func (d *diskStore) purgeAllCacheFilesOnCorruptTombstoneLocked() {
	entries, err := d.readDir(d.dir)
	if err != nil {
		d.corruptTombstoneCleanupPending = true
		d.unrecoverableInvalidation.Store(true)
		d.recordWriteFailure(err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if isCacheFile(name) {
			d.purgeCorruptTombstoneCacheFileLocked(name, e)
		} else if isCacheTempFile(name) {
			if _, active := d.activeTempFiles.Load(name); !active {
				_ = d.removePath(filepath.Join(d.dir, name))
			}
		}
	}
	d.corruptTombstoneCleanupPending = false
	d.syncTombstoneStateLocked()
}

func (d *diskStore) loadAndSweepTombstonesLocked() {
	tombPath := filepath.Join(d.dir, diskTombstoneFile)
	raw, err := d.readTombstoneManifest(tombPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		// Non-ENOENT read error (e.g. permission or I/O error on an existing
		// tombstone manifest): fail closed by purging all cache files in d.dir
		// so invalidated entries listed in the unreadable manifest cannot be
		// rehydrated.
		d.recordWriteFailure(err)
		d.purgeAllCacheFilesOnCorruptTombstoneLocked()
		return
	}
	lines := strings.Split(string(raw), "\n")
	var parsed []struct {
		hash string
		size int64
	}
	validFooter := false
	expectedCount := -1
	corrupt := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if validFooter {
			corrupt = true
			break
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == diskTombstoneMagic {
			n, pErr := strconv.Atoi(fields[1])
			if pErr != nil || n != len(parsed) {
				corrupt = true
				break
			}
			expectedCount = n
			validFooter = true
			continue
		}
		if len(fields) < 1 || !isCacheFile(fields[0]) {
			corrupt = true
			break
		}
		var sz int64
		if len(fields) > 1 {
			var pErr error
			sz, pErr = strconv.ParseInt(fields[1], 10, 64)
			if pErr != nil || sz < 0 {
				corrupt = true
				break
			}
		}
		parsed = append(parsed, struct {
			hash string
			size int64
		}{hash: fields[0], size: sz})
	}
	if !validFooter || expectedCount != len(parsed) {
		corrupt = true
	}
	for _, item := range parsed {
		sz := item.size
		if sz <= 0 {
			if st, stErr := os.Stat(d.path(item.hash)); stErr == nil {
				sz = st.Size()
			}
		}
		if _, exists := d.tombstones[item.hash]; !exists {
			d.tombstones[item.hash] = sz
			d.tombstoneBytes += sz
		}
	}
	if corrupt {
		// Fail closed: if the tombstone manifest was truncated or corrupted,
		// purge all cache files in the directory rather than risking rehydration
		// of an invalidated entry whose tombstone line was lost.
		d.purgeAllCacheFilesOnCorruptTombstoneLocked()
		return
	}
	d.sweepTombstonesLocked()
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
	if strings.HasPrefix(name, ".jul-cache-tmp-") ||
		strings.HasPrefix(name, ".jul-cache-retired-") ||
		strings.HasPrefix(name, diskTombstoneFile+".tmp-") ||
		strings.HasPrefix(name, "."+diskTombstoneFile+".tmp-") ||
		strings.HasPrefix(name, diskHighWaterFile+".tmp-") ||
		strings.HasPrefix(name, "."+diskHighWaterFile+".tmp-") {
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
	var fileHW uint64
	if rawHW, err := os.ReadFile(filepath.Join(d.dir, diskHighWaterFile)); err == nil {
		if parsedHW, pErr := strconv.ParseUint(strings.TrimSpace(string(rawHW)), 10, 64); pErr == nil {
			fileHW = parsedHW
			if parsedHW > d.highWaterGen {
				d.highWaterGen = parsedHW
			}
		}
	}
	if rawLedger, err := os.ReadFile(filepath.Join(d.dir, diskPurgeLedgerFile)); err == nil {
		var ledgerRecs []struct {
			Response struct {
				FenceSequence uint64 `json:"fence_sequence"`
			} `json:"response"`
		}
		if json.Unmarshal(rawLedger, &ledgerRecs) == nil {
			for _, rec := range ledgerRecs {
				if rec.Response.FenceSequence > d.highWaterGen {
					d.highWaterGen = rec.Response.FenceSequence
				}
			}
		}
	}
	d.loadAndSweepTombstonesLocked()
	entries, err := d.readDir(d.dir)
	if err != nil || d.corruptTombstoneCleanupPending {
		if d.highWaterGen > fileHW {
			_ = atomicfile.Write(filepath.Join(d.dir, diskHighWaterFile), []byte(strconv.FormatUint(d.highWaterGen, 10)+"\n"), 0o600)
		}
		return
	}
	type fi struct {
		hash             string
		key              string
		gen              uint64
		size             int64
		bodyLen          int64
		mod              int64
		isVaryStub       bool
		variants         []string
		tags             []string
		tagPolicyTracked bool
		tagHeaders       [8]string
		tagHeaderCount   int
		createdAt        time.Time
		evidence         *publicationEvidence
		expiresAt        time.Time
		staleUntil       time.Time
		varyFields       []string
	}
	var files []fi
	var foreign int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == diskTombstoneFile || name == diskPurgeLedgerFile || name == diskHighWaterFile {
			continue
		}
		if isCacheTempFile(name) {
			_ = d.removePath(filepath.Join(d.dir, name))
			continue
		}
		if !isCacheFile(name) {
			foreign++
			continue
		}
		if _, isTomb := d.tombstones[name]; isTomb {
			if err := d.removePath(d.path(name)); err == nil || os.IsNotExist(err) {
				d.clearTombstoneLocked(name)
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fullPath := d.path(name)
		entryMeta, entryKey, entryGen, bodyLen, fileSize, err := readDiskEntryMetaWithKeyGen(fullPath)
		if err != nil || entryKey == "" || hashKey(entryKey) != name {
			_ = d.removePath(fullPath)
			continue
		}
		if entryGen > d.highWaterGen {
			d.highWaterGen = entryGen
		}
		files = append(files, fi{
			hash:             name,
			key:              entryKey,
			gen:              entryGen,
			size:             fileSize,
			bodyLen:          bodyLen,
			mod:              info.ModTime().UnixNano(),
			isVaryStub:       entryMeta.IsVaryStub,
			variants:         entryMeta.Variants,
			tags:             entryMeta.Tags,
			tagPolicyTracked: entryMeta.tagPolicyTracked,
			tagHeaders:       entryMeta.ingestedTagHeaders,
			tagHeaderCount:   entryMeta.ingestedTagHeaderCount,
			createdAt:        entryMeta.CreatedAt,
			evidence:         entryMeta.Evidence,
			expiresAt:        entryMeta.ExpiresAt,
			staleUntil:       entryMeta.StaleUntil,
			varyFields:       entryMeta.Vary,
		})
	}
	if foreign > 0 {
		d.log.Warn("cache: ignoring foreign files in disk cache directory",
			"dir", d.dir, "count", foreign,
			"hint", "point cache disk_path at a directory dedicated to the cache")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files {
		blob := d.newBlob(f.hash, d.path(f.hash), f.bodyLen, f.size)
		it := &diskItem{
			hash:             f.hash,
			key:              f.key,
			gen:              f.gen,
			fileSize:         f.size,
			bodyLen:          f.bodyLen,
			metaLen:          f.size - f.bodyLen,
			isVaryStub:       f.isVaryStub,
			variants:         f.variants,
			tags:             f.tags,
			tagPolicyTracked: f.tagPolicyTracked,
			tagHeaders:       f.tagHeaders,
			tagHeaderCount:   f.tagHeaderCount,
			createdAt:        f.createdAt,
			evidence:         f.evidence,
			expiresAt:        f.expiresAt,
			staleUntil:       f.staleUntil,
			varyFields:       f.varyFields,
			blob:             blob,
		}
		it.size = it.effectiveSize(d.maxBytes)
		el := d.ll.PushFront(it)
		d.items[f.hash] = el
		d.curBytes += it.size
		d.physBytes += it.fileSize
		d.rememberHashKeyLocked(f.hash, f.key)
	}
	d.pruneOrphanedVaryStubsLocked()
	d.evictLocked()
	if d.highWaterGen > fileHW {
		_ = atomicfile.Write(filepath.Join(d.dir, diskHighWaterFile), []byte(strconv.FormatUint(d.highWaterGen, 10)+"\n"), 0o600)
	}
	if d.onRehydrate != nil {
		d.onRehydrate(d.snapshotItemsLocked())
	}
}

func (d *diskStore) snapshotItemsLocked() []diskItemSnapshot {
	out := make([]diskItemSnapshot, 0, len(d.items))
	for el := d.ll.Front(); el != nil; el = el.Next() {
		it := el.Value.(*diskItem)
		if it.key == "" {
			continue
		}
		out = append(out, diskItemSnapshot{
			Key:              it.key,
			Gen:              it.gen,
			FileSize:         it.fileSize,
			BodyLen:          it.bodyLen,
			IsVaryStub:       it.isVaryStub,
			Variants:         append([]string(nil), it.variants...),
			Tags:             append([]string(nil), it.tags...),
			TagPolicyTracked: it.tagPolicyTracked,
			TagHeaders:       it.tagHeaders,
			TagHeaderCount:   it.tagHeaderCount,
			CreatedAt:        it.createdAt,
			Evidence:         it.evidence,
			ExpiresAt:        it.expiresAt,
			StaleUntil:       it.staleUntil,
			VaryFields:       append([]string(nil), it.varyFields...),
		})
	}
	return out
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

func (d *diskStore) hasKey(key string) bool {
	hash := hashKey(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	el, ok := d.items[hash]
	if ok {
		it := el.Value.(*diskItem)
		if it.key == "" {
			it.key = key
		}
		d.rememberHashKeyLocked(hash, key)
	}
	return ok
}

func (d *diskStore) get(key string) (*Entry, bool) {
	e, _, ok := d.getWithMemCapGen(key, math.MaxInt64)
	return e, ok
}

func (d *diskStore) getWithMemCapGen(key string, memCap int64) (*Entry, uint64, bool) {
	return d.getWithMemCapObserved(key, memCap, nil)
}

func (d *diskStore) getWithMemCapObserved(key string, memCap int64, onFailure func()) (*Entry, uint64, bool) {
	hash := hashKey(key)
	d.mu.Lock()
	d.sweepTombstonesLocked()
	if d.unrecoverableInvalidation.Load() {
		d.mu.Unlock()
		return nil, 0, false
	}
	el, ok := d.items[hash]
	var blob *diskBlob
	var gen uint64
	if ok {
		it := el.Value.(*diskItem)
		if it.key == "" {
			it.key = key
		}
		blob = it.blob
		gen = it.gen
		if blob == nil || !blob.acquireLease() {
			d.mu.Unlock()
			return nil, 0, false
		}
		d.ll.MoveToFront(el)
		d.notifyKeyDiskStateLocked(key, hash)
	}
	hook := d.onLookupLeaseAcquire
	d.mu.Unlock()
	if !ok {
		return nil, 0, false
	}
	if hook != nil {
		hook()
	}

	rc, err := blob.openAcquiredReader()
	if err != nil {
		d.delIfBlob(key, blob)
		if onFailure != nil {
			onFailure()
		}
		return nil, 0, false
	}
	defer func() { _ = rc.Close() }()

	e, bodyLen, _, err := readDiskEntryMetaFile(rc.file)
	if err != nil {
		_ = rc.Close()
		d.delIfBlob(key, blob)
		if onFailure != nil {
			onFailure()
		}
		return nil, 0, false
	}
	if bodyLen <= memCap {
		if bodyLen > 0 {
			body := make([]byte, bodyLen)
			if _, err := io.ReadFull(rc, body); err != nil {
				_ = rc.Close()
				d.delIfBlob(key, blob)
				if onFailure != nil {
					onFailure()
				}
				return nil, 0, false
			}
			e.Body = body
		}
		return e, gen, true
	}
	e.diskBody = blob
	return e, gen, true
}

func (d *diskStore) set(key string, e *Entry) {
	d.setWithGen(key, e, 0)
}

func (d *diskStore) setWithGen(key string, e *Entry, gen uint64) bool {
	return d.setWithGenFull(key, e, gen, 0, false, nil)
}

func (d *diskStore) setWithGenFull(key string, e *Entry, gen, startGen uint64, hasStartGen bool, extraTags []string) bool {
	return d.setWithGenObserved(key, e, gen, startGen, hasStartGen, extraTags, d.onSkip, nil)
}

func (d *diskStore) setWithGenObserved(key string, e *Entry, gen, startGen uint64, hasStartGen bool, extraTags []string, onSkip func(string), onFailure func()) bool {
	if e == nil {
		return false
	}
	acct := captureAccount{budget: &d.tempBudget, onSkip: onSkip}
	bodyLen := e.BodyLen()
	if bodyLen > 0 && !acct.grow(int(bodyLen), d.availableBudgetCap()) {
		acct.skip(captureSkipBudget)
		return false
	}
	s := d.beginStreamCaptureObserved(onSkip, onFailure)
	if s == nil {
		acct.release()
		return false
	}
	s.acct = acct
	defer d.abortStream(s)
	if e.diskBody != nil {
		rc, err := e.diskBody.openReader()
		if err != nil {
			d.recordWriteFailure(err)
			if onFailure != nil {
				onFailure()
			}
			return false
		}
		n, copyErr := copyWithPooledBuffer(s.file, rc)
		_ = rc.Close()
		if copyErr != nil {
			d.recordWriteFailure(copyErr)
			if onFailure != nil {
				onFailure()
			}
			return false
		}
		s.bodyLen = n
	} else if len(e.Body) > 0 {
		if _, err := s.file.Write(e.Body); err != nil {
			d.recordWriteFailure(err)
			if onFailure != nil {
				onFailure()
			}
			return false
		}
		s.bodyLen = int64(len(e.Body))
	}
	blob, ok := d.publishStreamWithGenFull(key, e, s, gen, startGen, hasStartGen, extraTags)
	if ok && e.diskBody != nil {
		e.diskBody = blob
	}
	return ok
}

var tempFileSeq atomic.Uint64

func (d *diskStore) tempPath(seq uint64) string {
	return filepath.Join(d.dir, ".jul-cache-tmp-"+strconv.FormatUint(seq, 10))
}

func (d *diskStore) onRetiredBlobClosedLocked(hash string, b *diskBlob) {
	if b.countedRetired {
		b.countedRetired = false
		d.retiredBytes -= b.fileSize
		if d.retiredBytes < 0 {
			d.retiredBytes = 0
		}
	}
	wasPinned := d.pinned != nil && d.pinned[hash] == b
	if wasPinned {
		delete(d.pinned, hash)
	}
	if d.failedRetired != nil {
		delete(d.failedRetired, b)
	}
	if wasPinned {
		if _, ok := d.tombstones[hash]; ok {
			d.clearTombstoneLocked(hash)
		}
	}
	d.notifyKeyDiskStateLocked("", hash)
}

func (d *diskStore) onRetiredBlobClosed(hash string, b *diskBlob) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onRetiredBlobClosedLocked(hash, b)
}

func (d *diskStore) onDeferredRemoveFailed(b *diskBlob, path string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recordWriteFailure(err)
	if d.failedRetired == nil {
		d.failedRetired = make(map[*diskBlob]struct{})
	}
	d.failedRetired[b] = struct{}{}
	if isCacheFile(filepath.Base(path)) {
		// Record a 0-byte tombstone (physical bytes are already counted in
		// d.retiredBytes via b.countedRetired) so a restart cannot rehydrate the
		// unremoved canonical file.
		d.recordTombstoneLocked(filepath.Base(path), 0)
	}
	_ = d.truncatePath(path, 0)
}

func (d *diskStore) retireBlobLocked(hash string, b *diskBlob) error {
	if b == nil {
		return nil
	}
	unlinkedImm, err := b.retire()
	if err != nil {
		return err
	}
	if !unlinkedImm {
		if !b.countedRetired {
			b.countedRetired = true
			d.retiredBytes += b.fileSize
		}
		if b.isPinning(d.path(hash)) {
			if d.pinned == nil {
				d.pinned = make(map[string]*diskBlob)
			}
			d.pinned[hash] = b
			d.recordTombstoneLocked(hash, 0)
		}
	}
	return nil
}

// diskStreamCapture manages a temporary file receiving a streamed response body
// larger than the memory tier (#525 Part 2).
type diskStreamCapture struct {
	onFailure func()
	file      *os.File
	seq       uint64
	bodyLen   int64
	acct      captureAccount
	closed    bool
	published bool
}

func (d *diskStore) beginStreamCapture(onSkip func(reason string)) *diskStreamCapture {
	return d.beginStreamCaptureObserved(onSkip, nil)
}

func (d *diskStore) beginStreamCaptureObserved(onSkip func(string), onFailure func()) *diskStreamCapture {
	seq := tempFileSeq.Add(1)
	tmpPath := d.tempPath(seq)
	tmpName := filepath.Base(tmpPath)
	d.activeTempFiles.Store(tmpName, struct{}{})
	tmp, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		d.activeTempFiles.Delete(tmpName)
		d.recordWriteFailure(err)
		if onFailure != nil {
			onFailure()
		}
		return nil
	}
	return &diskStreamCapture{
		file:      tmp,
		onFailure: onFailure,
		seq:       seq,
		acct:      captureAccount{budget: &d.tempBudget, onSkip: onSkip},
	}
}

func (d *diskStore) writeStream(s *diskStreamCapture, chunks ...[]byte) (ok bool, skipReason string) {
	if d == nil || s == nil || s.closed {
		return false, ""
	}
	maxB := d.availableBudgetCap()
	for _, p := range chunks {
		if len(p) == 0 {
			continue
		}
		if !s.acct.grow(len(p), maxB) {
			return false, captureSkipBudget
		}
		if _, err := s.file.Write(p); err != nil {
			d.recordWriteFailure(err)
			if s.onFailure != nil {
				s.onFailure()
			}
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
		tmpPath := d.tempPath(s.seq)
		// lgtm[go/path-injection] – d.dir is the operator-configured cache directory and s.seq is an internal atomic uint64 counter.
		_ = os.Remove(tmpPath)
		d.activeTempFiles.Delete(filepath.Base(tmpPath))
		s.seq = 0
	}
	s.acct.release()
}

func (d *diskStore) publishStream(key string, e *Entry, s *diskStreamCapture) (*diskBlob, bool) {
	return d.publishStreamWithGen(key, e, s, 0)
}

func (d *diskStore) publishStreamWithGen(key string, e *Entry, s *diskStreamCapture, gen uint64) (*diskBlob, bool) {
	return d.publishStreamWithGenFull(key, e, s, gen, 0, false, nil)
}

func (d *diskStore) publishStreamWithGenFull(key string, e *Entry, s *diskStreamCapture, gen, startGen uint64, hasStartGen bool, extraTags []string) (*diskBlob, bool) {
	if s == nil || s.closed {
		return nil, false
	}
	tail, err := encodeDiskTrailerAndMetaWithKeyGen(key, gen, e, s.bodyLen)
	if err != nil {
		return nil, false
	}
	budgetCap := d.availableBudgetCap()
	allowCap := budgetCap
	if (s.bodyLen <= budgetCap && int64(len(tail)) <= maxDiskMetaEnvelope && s.bodyLen+int64(len(tail)) > budgetCap && d.tempBudget.inflight.Load() == s.bodyLen) ||
		(e.IsVaryStub && s.bodyLen == 0 && int64(len(tail)) <= maxDiskMetaEnvelope && d.tempBudget.inflight.Load() == 0) {
		allowCap = budgetCap + int64(len(tail))
	}
	if !s.acct.grow(len(tail), allowCap) {
		s.acct.skip(captureSkipBudget)
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
		if s.onFailure != nil {
			s.onFailure()
		}
		return nil, false
	}
	if fault == "sync" {
		d.recordWriteFailure(errors.New("injected sync failure"))
		return nil, false
	}
	if err := s.file.Sync(); err != nil {
		d.recordWriteFailure(err)
		if s.onFailure != nil {
			s.onFailure()
		}
		return nil, false
	}
	if err := s.file.Close(); err != nil || fault == "close" {
		s.closed = true
		if err == nil {
			err = errors.New("injected close failure")
		}
		d.recordWriteFailure(err)
		if s.onFailure != nil {
			s.onFailure()
		}
		return nil, false
	}
	s.closed = true

	if d.onBeforePublishLock != nil {
		d.onBeforePublishLock(key, gen)
	}

	hash := hashKey(key)
	targetPath := d.path(hash)
	tmpPath := d.tempPath(s.seq)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()
	if s.bodyLen > d.maxBytes || int64(len(tail)) > maxDiskMetaEnvelope {
		if s.acct.onSkip != nil {
			s.acct.onSkip(captureSkipOversize)
		}
		return nil, false
	}
	if gen != 0 {
		if d.canCommit != nil {
			if !d.canCommit(key, gen, startGen, hasStartGen, e, extraTags) {
				return nil, false
			}
		} else if d.checkGen != nil && !d.checkGen(key, gen) {
			return nil, false
		}
		if el, ok := d.items[hash]; ok {
			if it := el.Value.(*diskItem); it.gen > gen {
				return nil, false
			}
		}
	}
	if gen > d.highWaterGen {
		d.highWaterGen = gen
	}
	if fault == "rename" {
		d.recordWriteFailure(errors.New("injected rename failure"))
		return nil, false
	}
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		if err := d.retireBlobLocked(hash, it.blob); err != nil {
			d.recordWriteFailure(err)
			if s.onFailure != nil {
				s.onFailure()
			}
			return nil, false
		}
	}
	finalPath := targetPath
	var pinned *diskBlob
	if p := d.pinned[hash]; p != nil && p.isPinning(targetPath) {
		// The retired blob still owns targetPath and its last reader will call
		// os.Remove(targetPath) on Close. Keep the new file at tmpPath until
		// that removal completes, then promote tmpPath -> targetPath via setOnClose.
		pinned = p
		finalPath = tmpPath
	} else if err := d.renamePath(tmpPath, targetPath); err != nil {
		if el, ok := d.items[hash]; ok {
			it := el.Value.(*diskItem)
			d.ll.Remove(el)
			delete(d.items, hash)
			d.curBytes -= it.size
			d.physBytes -= it.fileSize
			d.notifyKeyDiskStateLocked(it.key, hash)
		}
		d.recordWriteFailure(err)
		if s.onFailure != nil {
			s.onFailure()
		}
		return nil, false
	} else {
		d.activeTempFiles.Delete(filepath.Base(tmpPath))
		delete(d.pinned, hash)
		d.clearTombstoneLocked(hash)
	}
	s.published = true
	s.acct.release()
	if d.failing.Load() && !d.tombstoneDirty && !d.unrecoverableInvalidation.Load() {
		d.failing.Store(false)
	}

	blob := d.newBlob(hash, finalPath, s.bodyLen, totalSize)
	if pinned != nil {
		pinned.setOnClose(func() {
			blob.promote(targetPath)
		})
	}
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		it.key = key
		if gen != 0 {
			it.gen = gen
		}
		it.fileSize = totalSize
		it.bodyLen = s.bodyLen
		it.metaLen = int64(len(tail))
		it.isVaryStub = e.IsVaryStub
		it.variants = append([]string(nil), e.Variants...)
		it.tags = append([]string(nil), e.Tags...)
		it.tagPolicyTracked = e.tagPolicyTracked
		it.tagHeaders = e.ingestedTagHeaders
		it.tagHeaderCount = e.ingestedTagHeaderCount
		it.createdAt = e.CreatedAt
		it.evidence = e.Evidence
		it.expiresAt = e.ExpiresAt
		it.staleUntil = e.StaleUntil
		it.varyFields = append([]string(nil), e.Vary...)
		it.size = it.effectiveSize(d.maxBytes)
		it.blob = blob
		d.curBytes += it.size
		d.physBytes += it.fileSize
		d.ll.MoveToFront(el)
	} else {
		it := &diskItem{
			hash:             hash,
			key:              key,
			gen:              gen,
			fileSize:         totalSize,
			bodyLen:          s.bodyLen,
			metaLen:          int64(len(tail)),
			isVaryStub:       e.IsVaryStub,
			variants:         append([]string(nil), e.Variants...),
			tags:             append([]string(nil), e.Tags...),
			tagPolicyTracked: e.tagPolicyTracked,
			tagHeaders:       e.ingestedTagHeaders,
			tagHeaderCount:   e.ingestedTagHeaderCount,
			createdAt:        e.CreatedAt,
			evidence:         e.Evidence,
			expiresAt:        e.ExpiresAt,
			staleUntil:       e.StaleUntil,
			varyFields:       append([]string(nil), e.Vary...),
			blob:             blob,
		}
		it.size = it.effectiveSize(d.maxBytes)
		el := d.ll.PushFront(it)
		d.items[hash] = el
		d.curBytes += it.size
		d.physBytes += it.fileSize
	}
	d.notifyKeyDiskStateLocked(key, hash)
	d.evictLocked()
	if el, ok := d.items[hash]; !ok || el.Value.(*diskItem).blob != blob {
		if s.acct.onSkip != nil {
			s.acct.onSkip(captureSkipBudget)
		}
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

func (d *diskStore) pruneOrphanedVaryStubsLocked() {
	// First prune any variant entry whose base Vary stub is missing or does not
	// claim the variant (ADR 0027 §4).
	for el := d.ll.Back(); el != nil; {
		prev := el.Prev()
		it := el.Value.(*diskItem)
		if !it.isVaryStub && it.key != "" {
			if nul := strings.IndexByte(it.key, 0x00); nul > 0 {
				baseKey := it.key[:nul]
				stubEl, ok := d.items[hashKey(baseKey)]
				claimed := false
				if ok {
					stubIt := stubEl.Value.(*diskItem)
					if stubIt.isVaryStub {
						for _, vk := range stubIt.variants {
							if vk == it.key {
								claimed = true
								break
							}
						}
					}
				}
				if !claimed {
					if err := d.retireBlobLocked(it.hash, it.blob); err == nil {
						d.ll.Remove(el)
						delete(d.items, it.hash)
						d.curBytes -= it.size
						d.physBytes -= it.fileSize
						d.notifyKeyDiskStateLocked(it.key, it.hash)
					}
				}
			}
		}
		el = prev
	}
	// Next prune any Vary stub that has no live variants on disk.
	for el := d.ll.Back(); el != nil; {
		prev := el.Prev()
		it := el.Value.(*diskItem)
		if it.isVaryStub && len(it.variants) > 0 {
			live := make([]string, 0, len(it.variants))
			for _, vk := range it.variants {
				if _, ok := d.items[hashKey(vk)]; ok {
					live = append(live, vk)
				}
			}
			it.variants = live
			if len(live) == 0 {
				if err := d.retireBlobLocked(it.hash, it.blob); err == nil {
					d.ll.Remove(el)
					delete(d.items, it.hash)
					d.curBytes -= it.size
					d.physBytes -= it.fileSize
					d.notifyKeyDiskStateLocked(it.key, it.hash)
				}
			}
		}
		el = prev
	}
}

func (d *diskStore) evictLocked() {
	evictedAny := false
	for el := d.ll.Back(); el != nil && d.effectiveUsedLocked() > d.maxBytes; {
		prev := el.Prev()
		it := el.Value.(*diskItem)
		if it.size == 0 {
			el = prev
			continue
		}
		if err := d.retireBlobLocked(it.hash, it.blob); err != nil {
			d.recordWriteFailure(err)
			el = prev
			continue
		}
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		if d.onEviction != nil {
			d.onEviction(it.key)
		}
		d.evictions++
		evictedAny = true
		d.notifyKeyDiskStateLocked(it.key, it.hash)
		el = prev
	}
	if evictedAny {
		d.pruneOrphanedVaryStubsLocked()
	}
}

// stats returns a snapshot of the tier's live physical occupancy (including
// retired files still held by active readers and unremoved tombstones); see
// memStore.stats.
func (d *diskStore) stats() (curBytes, maxBytes int64, entries int, evictions int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()
	return d.physBytes + d.retiredBytes + d.tombstoneBytes, d.maxBytes, d.ll.Len(), d.evictions
}

func (d *diskStore) delIfBlob(key string, target *diskBlob) {
	hash := hashKey(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		if it.blob == target {
			d.ll.Remove(el)
			delete(d.items, it.hash)
			d.curBytes -= it.size
			d.physBytes -= it.fileSize
			d.rememberHashKeyLocked(hash, key)
			if err := d.retireBlobLocked(hash, it.blob); err != nil {
				d.recordTombstoneLocked(hash, it.fileSize)
				d.recordWriteFailure(err)
			}
			d.notifyKeyDiskStateLocked(key, hash)
		}
	}
}

func (d *diskStore) delIfOlderGen(key string, maxGen uint64) {
	hash := hashKey(key)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()
	if el, ok := d.items[hash]; ok {
		it := el.Value.(*diskItem)
		if maxGen != math.MaxUint64 && it.gen >= maxGen {
			return
		}
		d.dropIndexedHashLocked(hash)
		d.rememberHashKeyLocked(hash, key)
		if err := d.retireBlobLocked(hash, it.blob); err != nil {
			d.recordTombstoneLocked(hash, it.fileSize)
			d.recordWriteFailure(err)
		}
		d.notifyKeyDiskStateLocked(key, hash)
		return
	}
	if d.pinned[hash] != nil {
		d.rememberHashKeyLocked(hash, key)
		d.recordTombstoneLocked(hash, 0)
		d.notifyKeyDiskStateLocked(key, hash)
		return
	}
	if err := d.removePath(d.path(hash)); err != nil && !os.IsNotExist(err) {
		if st, stErr := os.Stat(d.path(hash)); stErr == nil {
			d.rememberHashKeyLocked(hash, key)
			d.recordTombstoneLocked(hash, st.Size())
		}
	}
	d.notifyKeyDiskStateLocked(key, hash)
}

func (d *diskStore) del(key string) {
	d.delIfOlderGen(key, math.MaxUint64)
}

// deleteBatchAtMostGen performs a crash-consistent prepare/commit deletion for
// a set of keys whose stored generation is at most maxGen (ADR 0027 §4).
//
// It writes target hashes to the durable tombstone manifest before unlinking
// files, retires active reader leases to .jul-cache-retired-* files, and
// reports exact removed, retired-reader, failed, and uncertainty outcomes.
func (d *diskStore) deleteBatchAtMostGen(keys []string, maxGen uint64) (removedEntries, retiredReaders, failedEntries int, uncertain bool) {
	if d == nil || len(keys) == 0 {
		return 0, 0, 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()

	type batchTarget struct {
		key  string
		hash string
		item *diskItem
	}
	targets := make([]batchTarget, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		hash := hashKey(key)
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		if el, ok := d.items[hash]; ok {
			it := el.Value.(*diskItem)
			if maxGen != math.MaxUint64 && it.gen > maxGen {
				continue
			}
			if it.key == "" {
				it.key = key
			}
			targets = append(targets, batchTarget{key: key, hash: hash, item: it})
		} else if d.pinned[hash] != nil {
			targets = append(targets, batchTarget{key: key, hash: hash})
		}
	}
	if len(targets) == 0 {
		return 0, 0, 0, d.unrecoverableInvalidation.Load()
	}

	// Step 1: Prepare durable tombstones before dropping visibility or unlinking.
	if d.tombstones == nil {
		d.tombstones = make(map[string]int64, len(targets))
	}
	addedTombSize := make(map[string]int64, len(targets))
	for _, tgt := range targets {
		d.rememberHashKeyLocked(tgt.hash, tgt.key)
		var sz int64
		if tgt.item != nil {
			sz = tgt.item.fileSize
		}
		if prev, exists := d.tombstones[tgt.hash]; exists {
			d.tombstoneBytes -= prev
		}
		d.tombstones[tgt.hash] = sz
		d.tombstoneBytes += sz
		addedTombSize[tgt.hash] = sz
	}
	prePersistErr := d.persistTombstonesLocked()
	if prePersistErr != nil {
		// Corrupt any existing tombstone manifest in place so startup recovery
		// triggers purgeAllCacheFilesOnCorruptTombstoneLocked if the process crashes,
		// and mark the batch outcome uncertain fail-closed (ADR 0027 §4.3).
		_ = d.truncatePath(filepath.Join(d.dir, diskTombstoneFile), 0)
		d.recordWriteFailure(prePersistErr)
		d.unrecoverableInvalidation.Store(true)
		uncertain = true
	}

	// Step 2: Drop indexed entries and retire/unlink backing files.
	for _, tgt := range targets {
		if tgt.item != nil {
			d.dropIndexedHashLocked(tgt.hash)
			hadReaders := tgt.item.blob != nil && tgt.item.blob.hasActiveReaders()
			if err := d.retireBlobLocked(tgt.hash, tgt.item.blob); err != nil {
				failedEntries++
				d.recordWriteFailure(err)
				if hadReaders {
					d.unrecoverableInvalidation.Store(true)
					uncertain = true
				} else if tErr := d.truncatePath(d.path(tgt.hash), 0); (tErr != nil && !os.IsNotExist(tErr)) || prePersistErr != nil {
					d.unrecoverableInvalidation.Store(true)
					uncertain = true
				}
			} else {
				removedEntries++
				if hadReaders {
					retiredReaders++
				}
				if tgt.item.blob == nil || !tgt.item.blob.isPinning(d.path(tgt.hash)) {
					if sz, exists := d.tombstones[tgt.hash]; exists {
						delete(d.tombstones, tgt.hash)
						d.tombstoneBytes -= sz
					}
				} else if prePersistErr != nil {
					d.unrecoverableInvalidation.Store(true)
					uncertain = true
				}
			}
			d.notifyKeyDiskStateLocked(tgt.key, tgt.hash)
			continue
		}
		// Pinned blob without indexed entry.
		if prePersistErr != nil {
			d.unrecoverableInvalidation.Store(true)
			uncertain = true
			failedEntries++
		} else {
			removedEntries++
		}
		d.notifyKeyDiskStateLocked(tgt.key, tgt.hash)
	}

	// Step 3: Commit cleaned-up tombstone manifest.
	d.syncTombstoneStateLocked()
	if d.unrecoverableInvalidation.Load() {
		uncertain = true
	}
	return removedEntries, retiredReaders, failedEntries, uncertain
}

func (d *diskStore) purge() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepTombstonesLocked()
	items := make([]*diskItem, 0, len(d.items))
	for _, el := range d.items {
		items = append(items, el.Value.(*diskItem))
	}
	d.ll.Init()
	d.items = make(map[string]*list.Element)
	d.curBytes = 0
	d.physBytes = 0
	for _, it := range items {
		d.rememberHashKeyLocked(it.hash, it.key)
		if err := d.retireBlobLocked(it.hash, it.blob); err != nil {
			d.recordTombstoneLocked(it.hash, it.fileSize)
			d.recordWriteFailure(err)
		}
		d.notifyKeyDiskStateLocked(it.key, it.hash)
	}
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
	d.sweepTombstonesLocked()
	d.maxBytes = maxBytes
	d.recomputeSizesLocked()
	for el := d.ll.Back(); el != nil && d.effectiveUsedLocked() > d.maxBytes; {
		prev := el.Prev()
		it := el.Value.(*diskItem)
		if it.size == 0 {
			el = prev
			continue
		}
		if err := d.retireBlobLocked(it.hash, it.blob); err != nil {
			d.log.Warn("cache: disk eviction failed to remove file", "dir", d.dir, "error", err)
			failedRemovals++
			break
		}
		d.ll.Remove(el)
		delete(d.items, it.hash)
		d.curBytes -= it.size
		d.physBytes -= it.fileSize
		if d.onEviction != nil {
			d.onEviction(it.key)
		}
		evictedCount++
		evictedBytes += it.fileSize
		d.notifyKeyDiskStateLocked(it.key, it.hash)
		el = prev
	}
	if evictedCount > 0 {
		d.pruneOrphanedVaryStubsLocked()
	}
	d.evictions += int64(evictedCount)
	return evictedCount, evictedBytes, failedRemovals
}

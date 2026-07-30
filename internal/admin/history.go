// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jul/internal/atomicfile"
)

// historyTimeLayout is the timestamp format used for snapshot filenames. It is
// filename-safe (no colons) and sorts lexicographically in chronological order,
// so a reverse string sort yields newest-first ordering without parsing.
const historyTimeLayout = "20060102T150405.000Z"

// historyExt is the snapshot file extension. Only files with this suffix are
// treated as snapshots, so unrelated files in the directory are ignored.
const historyExt = ".toml"

// historyMetaExt is the optional sidecar extension holding structured metadata
// about a snapshot (AC-05). A snapshot is fully described by "<id>.toml" (the
// raw configuration) plus an optional "<id>.json" sidecar. Snapshots written by
// earlier releases have no sidecar and remain valid: list() and get() tolerate
// its absence, and pruning removes both files together.
const historyMetaExt = ".json"

// historyMetaSchemaVersion is the current schema version stamped into every
// sidecar so future readers can migrate older metadata.
const historyMetaSchemaVersion = 1

// HistoryMetadata is the structured sidecar describing why and how a snapshot
// was created (AC-05). It is written next to the raw "<id>.toml" file as
// "<id>.json". Every field is redacted, low-cardinality provenance — it never
// contains raw configuration, secret values, or credentials.
type HistoryMetadata struct {
	SchemaVersion int `json:"schema_version"`

	ApplyID   string         `json:"apply_id,omitempty"`
	Operation ApplyOperation `json:"operation,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	Outcome   string         `json:"outcome,omitempty"`
	Actor     string         `json:"actor,omitempty"`

	// Reason distinguishes an ordinary pre-apply snapshot of the prior config
	// from an emergency recovery snapshot written when a restoration failed.
	Reason string `json:"reason"` // "pre_apply" | "recovery"

	PreviousVersion  string `json:"previous_version,omitempty"`
	CandidateVersion string `json:"candidate_version,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// History snapshot reason constants (AC-05).
const (
	historyReasonPreApply = "pre_apply"
	historyReasonRecovery = "recovery"
)

// history persists point-in-time snapshots of the raw configuration so the
// console can offer one-click rollback. It is safe for concurrent use through
// the admin server's single-flight request handling; filesystem operations are
// idempotent and tolerate races by ignoring already-removed files.
type history struct {
	dir  string
	keep int
}

// newHistory builds a history rooted at dir, retaining at most keep snapshots.
// A blank dir disables snapshotting (all methods become no-ops returning empty
// results), which keeps callers branch-free.
func newHistory(dir string, keep int) *history {
	return &history{dir: strings.TrimSpace(dir), keep: keep}
}

// enabled reports whether snapshotting is active.
func (h *history) enabled() bool { return h != nil && h.dir != "" }

// historyEntry describes one stored snapshot for the API listing. Beyond the
// intrinsic id/time/size it carries the optional, redacted provenance projected
// from the snapshot's metadata sidecar (AC-05): every extra field is
// low-cardinality attribution — never raw configuration, secret values, or
// credentials — and is emitted only when present so raw-only snapshots keep the
// original three-field shape. MetadataError records a per-entry sidecar read or
// decode failure so a single malformed sidecar degrades one row instead of
// failing the whole listing.
type historyEntry struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`

	ApplyID          string         `json:"apply_id,omitempty"`
	Operation        ApplyOperation `json:"operation,omitempty"`
	Mode             string         `json:"mode,omitempty"`
	Outcome          string         `json:"outcome,omitempty"`
	Actor            string         `json:"actor,omitempty"`
	Reason           string         `json:"reason,omitempty"`
	PreviousVersion  string         `json:"previous_version,omitempty"`
	CandidateVersion string         `json:"candidate_version,omitempty"`

	MetadataError string `json:"metadata_error,omitempty"`
}

// snapshot writes raw to a new timestamped file and prunes the directory to the
// retention bound. Empty content is skipped (there is nothing meaningful to roll
// back to). It returns the new snapshot ID, or an empty ID when skipped.
func (h *history) snapshot(raw []byte) (string, error) {
	if !h.enabled() || len(strings.TrimSpace(string(raw))) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(h.dir, 0o750); err != nil {
		return "", fmt.Errorf("create history dir: %w", err)
	}
	id := time.Now().UTC().Format(historyTimeLayout)
	name := id + historyExt
	// Guard against the unlikely sub-millisecond collision by appending a
	// counter so a rapid pair of writes never clobbers an earlier snapshot. The
	// separator must sort lexicographically after '.' (the extension delimiter)
	// so a suffixed snapshot still orders as newer than its unsuffixed sibling
	// under the reverse-string sort in snapshotFiles; '_' (0x5F) satisfies this
	// where '-' (0x2D) would not.
	path := filepath.Join(h.dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s_%d%s", id, i, historyExt)
		id = strings.TrimSuffix(name, historyExt)
		path = filepath.Join(h.dir, name)
	}
	if err := atomicfile.Write(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	h.prune()
	return id, nil
}

// snapshotWithMeta writes raw as a new snapshot and, when meta is non-nil, an
// accompanying "<id>.json" sidecar describing its provenance (AC-05). It
// returns the snapshot ID and a separate metaErr for the sidecar: a sidecar
// write failure never discards the already-written raw snapshot, since the raw
// TOML alone is still fully roll-back-able. Callers surface metaErr as a
// degraded-history condition rather than a failed save. When meta is nil this
// behaves exactly like snapshot.
func (h *history) snapshotWithMeta(raw []byte, meta *HistoryMetadata) (id string, metaErr error, err error) {
	id, err = h.snapshot(raw)
	if err != nil || id == "" || meta == nil {
		return id, nil, err
	}
	m := *meta
	m.SchemaVersion = historyMetaSchemaVersion
	if m.CreatedAt.IsZero() {
		m.CreatedAt = parseHistoryID(id)
	}
	if m.Reason == "" {
		m.Reason = historyReasonPreApply
	}
	encoded, mErr := json.MarshalIndent(&m, "", "  ")
	if mErr != nil {
		return id, fmt.Errorf("encode history metadata: %w", mErr), nil
	}
	metaPath := filepath.Join(h.dir, id+historyMetaExt)
	if wErr := atomicfile.Write(metaPath, encoded, 0o600); wErr != nil {
		return id, fmt.Errorf("write history metadata: %w", wErr), nil
	}
	return id, nil, nil
}

// getMeta returns the structured metadata sidecar for a snapshot, or (nil, nil)
// when the snapshot exists without a sidecar (older raw-only snapshots remain
// valid). The id is validated so it can never escape the history directory.
func (h *history) getMeta(id string) (*HistoryMetadata, error) {
	if !h.enabled() {
		return nil, fmt.Errorf("history is disabled")
	}
	if !validHistoryID(id) {
		return nil, fmt.Errorf("invalid snapshot id")
	}
	data, err := os.ReadFile(filepath.Join(h.dir, id+historyMetaExt))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // raw-only snapshot; no metadata is not an error
		}
		return nil, err
	}
	var m HistoryMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode history metadata: %w", err)
	}
	return &m, nil
}

// list returns stored snapshots newest-first.
func (h *history) list() ([]historyEntry, error) {
	if !h.enabled() {
		return nil, nil
	}
	names, err := h.snapshotFiles()
	if err != nil {
		return nil, err
	}
	out := make([]historyEntry, 0, len(names))
	for _, name := range names {
		fi, err := os.Stat(filepath.Join(h.dir, name))
		if err != nil {
			continue // removed concurrently; skip
		}
		id := strings.TrimSuffix(name, historyExt)
		entry := historyEntry{ID: id, Time: parseHistoryID(id), Size: fi.Size()}
		// Project the optional provenance sidecar. A missing sidecar (older
		// raw-only snapshot) leaves the projection empty; a malformed sidecar
		// degrades only this row via MetadataError so one bad file never fails
		// the whole listing (AC-05).
		switch meta, metaErr := h.getMeta(id); {
		case metaErr != nil:
			entry.MetadataError = metaErr.Error()
		case meta != nil:
			entry.ApplyID = meta.ApplyID
			entry.Operation = meta.Operation
			entry.Mode = meta.Mode
			entry.Outcome = meta.Outcome
			entry.Actor = meta.Actor
			entry.Reason = meta.Reason
			entry.PreviousVersion = meta.PreviousVersion
			entry.CandidateVersion = meta.CandidateVersion
		}
		out = append(out, entry)
	}
	return out, nil
}

// get returns the raw TOML of a single snapshot. The id is validated to a strict
// charset so it can never escape the history directory (path-traversal safe).
func (h *history) get(id string) ([]byte, error) {
	if !h.enabled() {
		return nil, fmt.Errorf("history is disabled")
	}
	if !validHistoryID(id) {
		return nil, fmt.Errorf("invalid snapshot id")
	}
	return os.ReadFile(filepath.Join(h.dir, id+historyExt))
}

// snapshotFiles returns snapshot filenames sorted newest-first.
func (h *history) snapshotFiles() ([]string, error) {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		id := strings.TrimSuffix(name, historyExt)
		if name == id || !validHistoryID(id) {
			continue // not a <id>.toml snapshot we wrote
		}
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, nil
}

// prune deletes the oldest snapshots beyond the retention bound. A keep of zero
// or less is treated as "no pruning" so an unbounded history is still possible.
func (h *history) prune() {
	if h.keep <= 0 {
		return
	}
	names, err := h.snapshotFiles()
	if err != nil {
		return
	}
	for _, name := range names[min(len(names), h.keep):] {
		_ = os.Remove(filepath.Join(h.dir, name))
		// AC-05: remove the metadata sidecar alongside the raw snapshot so the
		// two never drift. Absent sidecars (older snapshots) are ignored.
		id := strings.TrimSuffix(name, historyExt)
		_ = os.Remove(filepath.Join(h.dir, id+historyMetaExt))
	}
}

// validHistoryID reports whether id is a safe snapshot identifier: a non-empty
// string of a conservative charset with no path separators or "..". This is the
// sole gate protecting get() from path traversal.
func validHistoryID(id string) bool {
	if id == "" || len(id) > 64 || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z',
			r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// parseHistoryID recovers the snapshot time from its ID, tolerating the optional
// "_N" collision suffix appended by snapshot() when two writes land in the same
// millisecond. A parse failure yields the zero time rather than an error so
// listing never fails on a malformed-but-safe name. The suffix is stripped only
// when it is a trailing run of decimal digits AND the remaining prefix is itself
// a valid timestamp, so an underscore that is genuinely part of an unexpected
// name is never mistaken for a counter.
func parseHistoryID(id string) time.Time {
	base := id
	if i := strings.LastIndexByte(base, '_'); i > 0 {
		suffix := base[i+1:]
		if isDecimalDigits(suffix) {
			candidate := base[:i]
			if _, err := time.Parse(historyTimeLayout, candidate); err == nil {
				base = candidate
			}
		}
	}
	t, err := time.Parse(historyTimeLayout, base)
	if err != nil {
		return time.Time{}
	}
	return t
}

// isDecimalDigits reports whether value is a non-empty run of ASCII decimal
// digits. It backs parseHistoryID's collision-suffix detection.
func isDecimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

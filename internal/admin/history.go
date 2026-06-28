package admin

import (
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

// historyEntry describes one stored snapshot for the API listing.
type historyEntry struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
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
		out = append(out, historyEntry{ID: id, Time: parseHistoryID(id), Size: fi.Size()})
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
// "-N" collision suffix. A parse failure yields the zero time rather than an
// error so listing never fails on a malformed-but-safe name.
func parseHistoryID(id string) time.Time {
	base := id
	if i := strings.IndexByte(base, '-'); i >= 0 {
		// Only strip a trailing "-N" counter, not characters inside the stamp.
		if _, err := time.Parse(historyTimeLayout, base[:i]); err == nil {
			base = base[:i]
		}
	}
	t, err := time.Parse(historyTimeLayout, base)
	if err != nil {
		return time.Time{}
	}
	return t
}

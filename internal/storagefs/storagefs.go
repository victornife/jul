// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package storagefs reports free-space headroom for the filesystems backing
// storage paths Jul itself is configured to write (#437).
//
// It is deliberately narrow: callers pass the configured paths of Jul-owned
// storage, and the package answers "how much room does the filesystem that
// holds this path have". It never enumerates mounts, never creates a file or
// directory to discover capacity, and never returns a path, mount point,
// volume name, or device identity — results carry only byte counts, a bounded
// state, a bounded reason, and an opaque per-call filesystem group index used
// to say "these categories share one filesystem".
//
// Path resolution (documented in docs/observability.md):
//
//   - A relative path is made absolute against the process working directory,
//     exactly as the owning subsystem's open/mkdir would resolve it.
//   - The path is followed with stat (symlinks are followed, as a write would
//     follow them). An existing file or directory is measured directly.
//   - A path that does not exist yet is measured at its nearest existing
//     ancestor: that is the filesystem the subsystem's MkdirAll/create would
//     land on. The result is flagged ViaParent.
//   - A dangling symlink anywhere on that walk is reported as
//     ReasonPathUnresolved rather than guessing where its target would land.
//   - A permission error is ReasonPermissionDenied; any other stat error is
//     ReasonStatFailed. Neither falls back to a parent: a path Jul cannot stat
//     is a path Jul cannot write, and a parent's capacity would be a guess.
package storagefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Reason is a bounded explanation for a result without capacity numbers.
type Reason string

const (
	// ReasonNone means capacity was read successfully.
	ReasonNone Reason = ""
	// ReasonPathUnresolved means no existing filesystem object could be found
	// for the path (a dangling symlink, or no existing ancestor at all).
	ReasonPathUnresolved Reason = "path_unresolved"
	// ReasonPermissionDenied means the path or one of its ancestors could not
	// be examined because of permissions.
	ReasonPermissionDenied Reason = "permission_denied"
	// ReasonStatFailed means stat or the filesystem-capacity call failed for a
	// reason other than permissions or absence.
	ReasonStatFailed Reason = "stat_failed"
	// ReasonUnsupported means this platform has no filesystem-capacity
	// implementation; nothing is fabricated in its place.
	ReasonUnsupported Reason = "unsupported_platform"
)

// Usage is the capacity of one filesystem as seen by an unprivileged writer.
// A Have* flag is false when the platform could not report that field; the
// matching byte count is then meaningless and must be shown as unavailable,
// never as zero.
type Usage struct {
	// Available is the space an unprivileged process can still allocate. On
	// Unix it excludes root-reserved blocks (f_bavail), so it can reach zero
	// while df still shows a little "free" space.
	Available     uint64
	HaveAvailable bool
	Total         uint64
	HaveTotal     bool
}

// Result is the answer for one probed path.
type Result struct {
	Usage
	// Reason is non-empty when capacity could not be read.
	Reason Reason
	// ViaParent reports that the configured path does not exist yet and the
	// capacity is that of its nearest existing ancestor.
	ViaParent bool
	// Group is an opaque index, valid only within one Probe call, that is equal
	// for two results measured on the same filesystem. It is -1 when Reason is
	// set. It carries no device or mount identity.
	Group int
}

// errUnsupported is returned by platforms without a capacity implementation.
var errUnsupported = errors.New("storagefs: filesystem capacity is not supported on this platform")

// resolved is one path's resolution: the existing object to measure and the
// internal filesystem identity used to deduplicate capacity calls.
type resolved struct {
	anchor    string
	viaParent bool
	key       fsKey
	reason    Reason
}

// Probe resolves every path and reads each distinct filesystem's capacity
// once. Results are returned in the order of paths.
func Probe(paths []string) []Result {
	out := make([]Result, len(paths))
	groups := make(map[fsKey]int)
	type measured struct {
		usage  Usage
		reason Reason
	}
	var byGroup []measured
	for i, p := range paths {
		r := resolve(p)
		if r.reason != ReasonNone {
			out[i] = Result{Reason: r.reason, ViaParent: r.viaParent, Group: -1}
			continue
		}
		g, ok := groups[r.key]
		if !ok {
			u, err := statFS(r.anchor)
			m := measured{usage: u}
			if err != nil {
				m.reason = classifyErr(err)
			}
			g = len(byGroup)
			groups[r.key] = g
			byGroup = append(byGroup, m)
		}
		m := byGroup[g]
		if m.reason != ReasonNone {
			out[i] = Result{Reason: m.reason, ViaParent: r.viaParent, Group: -1}
			continue
		}
		out[i] = Result{Usage: m.usage, ViaParent: r.viaParent, Group: g}
	}
	return out
}

// statObject is os.Stat, indirected so tests can simulate a tree without an
// existing root.
var statObject = os.Stat

// resolve maps a configured path to the existing filesystem object whose
// filesystem the owning subsystem would write to. See the package comment.
func resolve(path string) resolved {
	abs, err := filepath.Abs(path)
	if err != nil {
		return resolved{reason: ReasonStatFailed}
	}
	p := filepath.Clean(abs)
	viaParent := false
	for {
		fi, err := statObject(p)
		if err == nil {
			key, kerr := identity(p, fi)
			if kerr != nil {
				return resolved{viaParent: viaParent, reason: classifyErr(kerr)}
			}
			return resolved{anchor: p, viaParent: viaParent, key: key}
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return resolved{viaParent: viaParent, reason: classifyErr(err)}
		}
		// stat follows symlinks; a name that lstat can see but stat cannot is a
		// dangling link whose eventual target filesystem is not knowable here.
		if _, lerr := os.Lstat(p); lerr == nil {
			return resolved{viaParent: viaParent, reason: ReasonPathUnresolved}
		}
		parent := filepath.Dir(p)
		if parent == p {
			return resolved{viaParent: viaParent, reason: ReasonPathUnresolved}
		}
		p = parent
		viaParent = true
	}
}

func classifyErr(err error) Reason {
	switch {
	case errors.Is(err, errUnsupported):
		return ReasonUnsupported
	case errors.Is(err, fs.ErrPermission):
		return ReasonPermissionDenied
	default:
		return ReasonStatFailed
	}
}

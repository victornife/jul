//go:build waf

package waf

import (
	"errors"
	"io"
	"io/fs"
	"strings"
)

// normalizeFS wraps an fs.FS so that every path uses forward slashes.
// Coraza's parser resolves glob results with filepath.Join, which on Windows
// converts '/' into '\'.  embed.FS (used by the embedded CRS) only accepts
// '/', so '\' names never match.  This adapter transparently remaps names
// before every downstream call.
type normalizeFS struct {
	inner fs.FS
}

func (n *normalizeFS) Open(name string) (fs.File, error) {
	return n.inner.Open(normSlashes(name))
}

func (n *normalizeFS) ReadFile(name string) ([]byte, error) {
	if rfs, ok := n.inner.(fs.ReadFileFS); ok {
		return rfs.ReadFile(normSlashes(name))
	}
	f, err := n.inner.Open(normSlashes(name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (n *normalizeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if rfs, ok := n.inner.(fs.ReadDirFS); ok {
		return rfs.ReadDir(normSlashes(name))
	}
	f, err := n.inner.Open(normSlashes(name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rdf, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("not a directory")
	}
	return rdf.ReadDir(-1)
}

func (n *normalizeFS) Glob(pattern string) ([]string, error) {
	normalized := normSlashes(pattern)
	if gfs, ok := n.inner.(fs.GlobFS); ok {
		matches, err := gfs.Glob(normalized)
		for i := range matches {
			matches[i] = normSlashes(matches[i])
		}
		return matches, err
	}
	return nil, fs.ErrInvalid
}

func (n *normalizeFS) Stat(name string) (fs.FileInfo, error) {
	if sfs, ok := n.inner.(fs.StatFS); ok {
		return sfs.Stat(normSlashes(name))
	}
	f, err := n.inner.Open(normSlashes(name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

func normSlashes(s string) string { return strings.ReplaceAll(s, "\\", "/") }

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"jul/internal/config"
)

const defaultPluginUploadDir = "./jul-data/plugins"

// adminRuntimeContextKey stores the immutable admin generation selected for one
// request. Authentication installs the snapshot before invoking a protected
// handler; public operational handlers capture it themselves. Downstream code
// must use requestAdminSnapshot rather than loading live state again, so a
// Publish that races an in-flight request cannot mix old authentication with a
// new Console/upload policy (HR-06B/#157).
type adminRuntimeContextKey struct{}

func withAdminRuntimeSnapshot(r *httpRequest, snap *authSnapshot) *httpRequest {
	return r.WithContext(context.WithValue(r.Context(), adminRuntimeContextKey{}, snap))
}

// httpRequest is kept as an alias so the request-snapshot helpers stay visually
// distinct from filesystem request handling below without introducing a wrapper
// type or allocation.
type httpRequest = http.Request

// requestAdminSnapshot returns the exact immutable snapshot captured for r when
// one is already present, otherwise it performs the single live atomic load for
// this public/direct request.
func (s *Server) requestAdminSnapshot(r *http.Request) *authSnapshot {
	if r != nil {
		if snap, ok := r.Context().Value(adminRuntimeContextKey{}).(*authSnapshot); ok && snap != nil {
			return snap
		}
	}
	return s.currentAuth()
}

// completeAdminRuntimeSnapshot attaches process-constant build capabilities and
// a safe generation digest to an already-built immutable auth snapshot. It
// clones rather than mutating the prepared snapshot so publication remains a
// single pointer store and PreparedAuth stays reusable by tests.
func (s *Server) completeAdminRuntimeSnapshot(in *authSnapshot) *authSnapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.consoleCompiled = consoleV2Compiled
	out.pluginsCompiled = s.deps.PluginsCompiled
	out.runtimeGen = adminRuntimeGeneration(out.cfg, out.gen, out.consoleCompiled, out.pluginsCompiled)
	return &out
}

func adminRuntimeGeneration(cfg config.AdminConfig, authGen string, consoleCompiled, pluginsCompiled bool) string {
	h := sha256.New()
	writeRuntimeGenerationPart := func(v string) {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	writeRuntimeGenerationPart(authGen)
	writeRuntimeGenerationPart(strconv.FormatBool(cfg.ConsoleEnabled()))
	writeRuntimeGenerationPart(strconv.FormatBool(pluginUploadEnabled(cfg)))
	writeRuntimeGenerationPart(strconv.Itoa(cfg.PluginUploadMaxSize))
	writeRuntimeGenerationPart(normalizePluginUploadDir(cfg.PluginUploadDir))
	writeRuntimeGenerationPart(strconv.FormatBool(consoleCompiled))
	writeRuntimeGenerationPart(strconv.FormatBool(pluginsCompiled))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func pluginUploadEnabled(cfg config.AdminConfig) bool {
	return cfg.PluginUploadEnabled != nil && *cfg.PluginUploadEnabled
}

// normalizePluginUploadDir resolves the startup-compatible default and makes the
// stored policy absolute/clean. A relative path is therefore interpreted once
// for the candidate generation instead of being re-resolved independently by
// every request.
func normalizePluginUploadDir(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultPluginUploadDir
	}
	cleaned := filepath.Clean(raw)
	if abs, err := filepath.Abs(cleaned); err == nil {
		return filepath.Clean(abs)
	}
	return cleaned
}

// preflightPluginUploadDir proves that the candidate upload directory is a
// directory (not a symlink/file) and writable/creatable without leaving the
// candidate path behind. Existing directories receive a uniquely named probe
// file which is removed before return. Missing directories are exercised in a
// temporary sibling tree under their nearest existing ancestor, then removed.
func preflightPluginUploadDir(raw string) error {
	dir := normalizePluginUploadDir(raw)
	fi, err := os.Lstat(dir)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("[admin] plugin_upload_dir %q: symbolic links are not allowed", dir)
		}
		if !fi.IsDir() {
			return fmt.Errorf("[admin] plugin_upload_dir %q: path is not a directory", dir)
		}
		if err := requireUnaliasedDirectory(dir); err != nil {
			return fmt.Errorf("[admin] plugin_upload_dir %q: %w", dir, err)
		}
		return probePluginUploadDirectory(dir)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("[admin] plugin_upload_dir %q: inspect path: %w", dir, err)
	}

	ancestor, err := nearestExistingDirectory(dir)
	if err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: %w", dir, err)
	}
	if err := requireUnaliasedDirectory(ancestor); err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: unsafe parent: %w", dir, err)
	}

	probeRoot, err := os.MkdirTemp(ancestor, ".jul-upload-preflight-*")
	if err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: parent is not writable: %w", dir, err)
	}
	defer func() { _ = os.RemoveAll(probeRoot) }()
	if err := os.Chmod(probeRoot, 0o700); err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: secure temporary directory: %w", dir, err)
	}

	rel, err := filepath.Rel(ancestor, dir)
	if err != nil || rel == "." || rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("[admin] plugin_upload_dir %q: cannot derive a safe relative path", dir)
	}
	candidateProbe := filepath.Join(probeRoot, rel)
	if err := os.MkdirAll(candidateProbe, 0o700); err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: cannot create directory shape: %w", dir, err)
	}
	return probePluginUploadDirectory(candidateProbe)
}

func nearestExistingDirectory(path string) (string, error) {
	cur := filepath.Clean(path)
	for {
		fi, err := os.Lstat(cur)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("parent %q is a symbolic link", cur)
			}
			if !fi.IsDir() {
				return "", fmt.Errorf("parent %q is not a directory", cur)
			}
			return cur, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect parent %q: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing parent directory")
		}
		cur = parent
	}
}

// requireUnaliasedDirectory rejects an existing directory whose cleaned path
// resolves through any symbolic-link component. The request path later uses
// os.Root as the race-resistant confinement primitive; this preflight rule also
// keeps operator intent explicit instead of silently accepting aliases.
func requireUnaliasedDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve configured path: %w", err)
	}
	if filepath.Clean(resolvedAbs) != filepath.Clean(pathAbs) {
		return fmt.Errorf("symbolic-link path components are not allowed")
	}
	return nil
}

func probePluginUploadDirectory(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: open directory: %w", dir, err)
	}
	defer root.Close()

	name := nextPluginTempName("probe")
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: directory not writable: %w", dir, err)
	}
	closeErr := f.Close()
	removeErr := root.Remove(name)
	if closeErr != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: close write probe: %w", dir, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: remove write probe: %w", dir, removeErr)
	}
	return nil
}

var pluginTempSequence atomic.Uint64

func nextPluginTempName(kind string) string {
	return fmt.Sprintf(".jul-upload-%s-%d-%d.tmp", kind, os.Getpid(), pluginTempSequence.Add(1))
}

// openPluginUploadRoot returns a race-resistant os.Root for the captured
// directory generation. If Jul creates the final directory it uses owner-only
// mode. The pre/post SameFile checks ensure OpenRoot did not race a replacement
// of the configured directory path; once open, Root methods remain anchored to
// that directory even if its pathname is renamed.
func openPluginUploadRoot(dir string) (*os.Root, error) {
	dir = normalizePluginUploadDir(dir)
	created := false
	before, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create upload directory: %w", err)
		}
		created = true
		before, err = os.Lstat(dir)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect upload directory: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, fmt.Errorf("upload directory is not a safe directory")
	}
	if created {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("secure upload directory: %w", err)
		}
	}
	if err := requireUnaliasedDirectory(dir); err != nil {
		return nil, fmt.Errorf("unsafe upload directory: %w", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open upload directory: %w", err)
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("stat opened upload directory: %w", err)
	}
	after, err := os.Lstat(dir)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(rootInfo, after) {
		root.Close()
		return nil, fmt.Errorf("upload directory changed while opening")
	}
	return root, nil
}

// writePluginUploadFile atomically replaces name inside root. Both the
// temporary file and the final file are owner-only. Existing symlinks or
// non-regular targets are rejected; Root.Rename replaces a regular destination
// without ever following a filename outside the captured root.
func writePluginUploadFile(root *os.Root, name string, data []byte) error {
	if root == nil {
		return errors.New("upload root is unavailable")
	}
	if fi, err := root.Lstat(name); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return fmt.Errorf("upload destination is not a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect upload destination: %w", err)
	}

	tmpName := nextPluginTempName("write")
	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create upload temp file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = root.Remove(tmpName)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write upload temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync upload temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close upload temp file: %w", err)
	}

	// Re-check immediately before rename for a clearer error if another actor
	// replaced a regular target with a symlink/device. A race after this check is
	// still confined: Rename replaces the directory entry rather than following
	// the target.
	if fi, err := root.Lstat(name); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return fmt.Errorf("upload destination changed to an unsafe file type")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("recheck upload destination: %w", err)
	}

	var renameErr error
	for i := 0; i < 5; i++ {
		if renameErr = root.Rename(tmpName, name); renameErr == nil {
			break
		}
		if i < 4 {
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
		}
	}
	if renameErr != nil {
		return fmt.Errorf("finalize upload: %w", renameErr)
	}
	cleanup = false
	if d, err := root.Open("."); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"jul/internal/config"
)

const defaultPluginUploadDir = "./jul-data/plugins"

type adminRuntimeContextKey struct{}

// captureAdminRuntimeSnapshot pins the already-existing #95 immutable snapshot
// to one request. The snapshot already contains the complete effective
// AdminConfig and auth policy, so #157 deliberately does not introduce a second
// runtime authority.
func (s *Server) captureAdminRuntimeSnapshot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.currentAuth()
		ctx := context.WithValue(r.Context(), adminRuntimeContextKey{}, snap)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requestAdminSnapshot(r *http.Request) *authSnapshot {
	if r != nil {
		if snap, ok := r.Context().Value(adminRuntimeContextKey{}).(*authSnapshot); ok && snap != nil {
			return snap
		}
	}
	return s.currentAuth()
}

// PrepareAdminRuntime validates the candidate operational upload policy before
// Publish and returns a prepared snapshot whose AdminConfig contains a single
// normalized upload-directory interpretation. No final upload target is created
// during preparation.
func (s *Server) PrepareAdminRuntime(cfg config.AdminConfig, p *rbac.Policy) (*PreparedAuth, error) {
	cfg.PluginUploadDir = normalizePluginUploadDir(cfg.PluginUploadDir)
	if pluginUploadEnabled(cfg) {
		if err := preflightPluginUploadDir(cfg.PluginUploadDir); err != nil {
			return nil, err
		}
	}
	return PrepareAuth(cfg, p), nil
}

func pluginUploadEnabled(cfg config.AdminConfig) bool {
	return cfg.PluginUploadEnabled != nil && *cfg.PluginUploadEnabled
}

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

// preflightPluginUploadDir validates an existing directory directly. For a
// missing configured directory it proves the same directory shape can be
// created under the nearest existing ancestor, but removes that probe tree
// before returning so preparation never creates the live upload destination.
func preflightPluginUploadDir(raw string) error {
	dir := normalizePluginUploadDir(raw)
	fi, err := os.Lstat(dir)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("[admin] plugin_upload_dir %q: path must be a real directory, not a symlink or special file", dir)
		}
		if err := requireUnaliasedDirectory(dir); err != nil {
			return fmt.Errorf("[admin] plugin_upload_dir %q: %w", dir, err)
		}
		return probePluginUploadDirectory(dir)
	}
	if !errors.Is(err, fs.ErrNotExist) {
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
		return fmt.Errorf("[admin] plugin_upload_dir %q: secure probe directory: %w", dir, err)
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
			if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
				return "", fmt.Errorf("parent %q is not a real directory", cur)
			}
			return cur, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("inspect parent %q: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errors.New("no existing parent directory")
		}
		cur = parent
	}
}

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
		return errors.New("symbolic-link path components are not allowed")
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

// openPluginUploadRoot anchors request writes to the captured directory. os.Root
// rejects path escapes, including symlinks that resolve outside the root.
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
		return nil, errors.New("upload directory is not a safe directory")
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
		_ = root.Close()
		return nil, fmt.Errorf("stat opened upload directory: %w", err)
	}
	after, err := os.Lstat(dir)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(rootInfo, after) {
		_ = root.Close()
		return nil, errors.New("upload directory changed while opening")
	}
	return root, nil
}

func writePluginUploadFile(root *os.Root, name string, data []byte) error {
	if root == nil {
		return errors.New("upload root is unavailable")
	}
	if fi, err := root.Lstat(name); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return errors.New("upload destination is not a regular file")
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
	if fi, err := root.Lstat(name); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return errors.New("upload destination changed to an unsafe file type")
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

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
// AdminConfig and auth policy, so #157 does not introduce a second authority.
func (s *Server) captureAdminRuntimeSnapshot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), adminRuntimeContextKey{}, s.currentAuth())
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

// PrepareAdminRuntime is the typed #157 candidate-resource seam shared by
// managed, SIGHUP and file-watch reloads. It normalizes and validates candidate
// upload storage without publishing policy or creating the configured final directory.
func (s *Server) PrepareAdminRuntime(cfg config.AdminConfig, prepared *PreparedAuth) (*PreparedAuth, error) {
	cfg.PluginUploadDir = normalizePluginUploadDir(cfg.PluginUploadDir)
	if pluginUploadEnabled(cfg) && cfg.PluginUploadMaxSize > 0 {
		if err := preflightPluginUploadDir(cfg.PluginUploadDir); err != nil {
			s.recordAdminPrepareFailure(adminPrepareFailureUploadDirectory)
			return nil, newAdminRuntimePrepareError(adminPrepareFailureUploadDirectory, err)
		}
	}
	s.clearAdminPrepareFailure()
	if prepared == nil || prepared.snapshot == nil {
		return prepared, nil
	}
	out := *prepared.snapshot
	out.cfg = cfg
	return &PreparedAuth{snapshot: s.completeAdminRuntimeSnapshot(&out)}, nil
}

func (s *Server) completeAdminRuntimeSnapshot(in *authSnapshot) *authSnapshot {
	if in == nil {
		return nil
	}
	out := *in
	out.consoleCompiled = consoleV2Compiled
	out.pluginsCompiled = s.deps.PluginsCompiled
	out.uploadDirHealth = inspectPluginUploadDirHealth(out.cfg.PluginUploadDir)
	return &out
}

// Nil means the documented/default-enabled state. A non-positive size still
// disables the endpoint and is checked by the caller.
func pluginUploadEnabled(cfg config.AdminConfig) bool {
	return cfg.PluginUploadEnabled == nil || *cfg.PluginUploadEnabled
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

// preflightPluginUploadDir leaves no live directory or final upload artifact.
// Existing directories receive a reversible os.Root-confined probe. Missing
// paths are exercised beneath their nearest existing ancestor in a temporary
// sibling tree and removed before return.
func preflightPluginUploadDir(raw string) error {
	dir := normalizePluginUploadDir(raw)
	fi, err := os.Lstat(dir)
	if err == nil {
		// Reject a configured final component that is itself a symlink. Ancestor
		// path aliases are intentionally allowed (notably /var -> /private/var on
		// macOS); os.Root provides confinement for descendant operations.
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("[admin] plugin_upload_dir %q must be a real directory", dir)
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
		return fmt.Errorf("[admin] plugin_upload_dir %q: unsafe relative path", dir)
	}
	probeDir := filepath.Join(probeRoot, rel)
	if err := os.MkdirAll(probeDir, 0o700); err != nil {
		return fmt.Errorf("[admin] plugin_upload_dir %q: cannot create directory shape: %w", dir, err)
	}
	return probePluginUploadDirectory(probeDir)
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
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errors.New("no existing parent directory")
		}
		cur = parent
	}
}

func probePluginUploadDirectory(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := nextPluginTempName("probe")
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closeErr := f.Close()
	removeErr := root.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

var pluginTempSequence atomic.Uint64

func nextPluginTempName(kind string) string {
	return fmt.Sprintf(".jul-upload-%s-%d-%d.tmp", kind, os.Getpid(), pluginTempSequence.Add(1))
}

// openPluginUploadRoot anchors a request to the captured directory generation.
// The configured final component must not be a symlink. Once open, os.Root
// confines every descendant lookup and rename even if the path is raced.
func openPluginUploadRoot(dir string) (*os.Root, error) {
	dir = normalizePluginUploadDir(dir)
	before, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
		before, err = os.Lstat(dir)
	}
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errors.New("upload directory is not a safe directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	after, err := os.Lstat(dir)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(rootInfo, after) {
		_ = root.Close()
		return nil, errors.New("upload directory changed while opening")
	}
	return root, nil
}

// writePluginUploadFile writes a 0600 temporary file and atomically renames it
// inside the captured os.Root. Unsafe pre-existing destinations are rejected;
// temporary files are removed on every pre-rename failure.
func writePluginUploadFile(root *os.Root, name string, data []byte) error {
	if root == nil {
		return errors.New("upload root unavailable")
	}
	if fi, err := root.Lstat(name); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return errors.New("upload destination is not a regular file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmp := nextPluginTempName("write")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if fi, err := root.Lstat(name); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular()) {
		return errors.New("upload destination changed to an unsafe file type")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	var renameErr error
	for i := 0; i < 5; i++ {
		if renameErr = root.Rename(tmp, name); renameErr == nil {
			break
		}
		if i < 4 {
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
		}
	}
	if renameErr != nil {
		return renameErr
	}
	cleanup = false
	if d, err := root.Open("."); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteFile builds and safely publishes an owner-only tar.gz archive. Existing
// files are never overwritten, and a temporary file is removed on every error.
func (generator *Generator) WriteFile(ctx context.Context, output string, snapshot Snapshot) (FileResult, error) {
	bundle, err := generator.Build(ctx, snapshot)
	if err != nil {
		return FileResult{}, err
	}
	path, err := validateOutputPath(output)
	if err != nil {
		return FileResult{}, err
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".jul-support-*.tmp")
	if err != nil {
		return FileResult{}, fmt.Errorf("create support-bundle temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return FileResult{}, fmt.Errorf("set support-bundle temporary permissions: %w", err)
	}

	compressedBytes, err := WriteArchive(ctx, temporary, bundle)
	if err != nil {
		return FileResult{}, err
	}
	if err := temporary.Sync(); err != nil {
		return FileResult{}, fmt.Errorf("sync support-bundle temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return FileResult{}, fmt.Errorf("close support-bundle temporary file: %w", err)
	}

	// A same-directory hard link publishes a fully written inode atomically and
	// fails when the destination exists. This avoids os.Rename's overwrite race.
	if err := os.Link(temporaryPath, path); err != nil {
		if _, statErr := os.Lstat(path); statErr == nil {
			return FileResult{}, ErrOutputExists
		}
		return FileResult{}, fmt.Errorf("publish support bundle without overwrite: %w", err)
	}
	published = true
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(path)
		published = false
		return FileResult{}, fmt.Errorf("remove support-bundle temporary link: %w", err)
	}
	if directoryHandle, openErr := os.Open(directory); openErr == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return FileResult{Path: path, CompressedBytes: compressedBytes, Manifest: bundle.Manifest}, nil
}

// WriteArchive streams a deterministic tar.gz archive to writer and enforces
// the compressed and uncompressed limits recorded in the manifest.
func WriteArchive(ctx context.Context, writer io.Writer, bundle Bundle) (int64, error) {
	limits := normalizeLimits(bundle.limits)
	manifestBytes, err := json.MarshalIndent(bundle.Manifest, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("marshal support-bundle manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	var uncompressed int64
	for _, artifact := range bundle.Artifacts {
		uncompressed += int64(len(artifact.Data))
	}
	uncompressed += int64(len(manifestBytes))
	if uncompressed > limits.MaxUncompressedBytes {
		return 0, ErrBundleTooLarge
	}

	limited := &limitWriter{writer: writer, limit: limits.MaxCompressedBytes}
	contextual := &contextWriter{ctx: ctx, writer: limited}
	gzipWriter := gzip.NewWriter(contextual)
	tarWriter := tar.NewWriter(gzipWriter)
	closed := false
	defer func() {
		if !closed {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
		}
	}()

	createdAt := bundle.Manifest.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if err := writeTarEntry(tarWriter, "manifest.json", "application/json", manifestBytes, createdAt); err != nil {
		return limited.written, err
	}
	for _, artifact := range bundle.Artifacts {
		if _, err := safeArtifactPath(artifact.Path); err != nil {
			return limited.written, err
		}
		if err := writeTarEntry(tarWriter, artifact.Path, artifact.ContentType, artifact.Data, createdAt); err != nil {
			return limited.written, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return limited.written, normalizeArchiveError(err)
	}
	if err := gzipWriter.Close(); err != nil {
		return limited.written, normalizeArchiveError(err)
	}
	closed = true
	return limited.written, nil
}

func writeTarEntry(writer *tar.Writer, name, contentType string, data []byte, modified time.Time) error {
	header := &tar.Header{
		Name:       name,
		Mode:       0o600,
		Size:       int64(len(data)),
		ModTime:    modified,
		AccessTime: modified,
		ChangeTime: modified,
		Typeflag:   tar.TypeReg,
		Format:     tar.FormatPAX,
		PAXRecords: map[string]string{"JUL.content-type": contentType},
	}
	if err := writer.WriteHeader(header); err != nil {
		return normalizeArchiveError(err)
	}
	if _, err := writer.Write(data); err != nil {
		return normalizeArchiveError(err)
	}
	return nil
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer *contextWriter) Write(data []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(data)
}

type limitWriter struct {
	writer  io.Writer
	limit   int64
	written int64
}

func (writer *limitWriter) Write(data []byte) (int, error) {
	remaining := writer.limit - writer.written
	if remaining <= 0 {
		return 0, ErrArchiveTooLarge
	}
	if int64(len(data)) > remaining {
		count, err := writer.writer.Write(data[:remaining])
		writer.written += int64(count)
		if err != nil {
			return count, err
		}
		if int64(count) < remaining {
			return count, io.ErrShortWrite
		}
		return count, ErrArchiveTooLarge
	}
	count, err := writer.writer.Write(data)
	writer.written += int64(count)
	if err != nil {
		return count, err
	}
	if count < len(data) {
		return count, io.ErrShortWrite
	}
	return count, nil
}

func normalizeArchiveError(err error) error {
	if errors.Is(err, ErrArchiveTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("write support-bundle archive: %w", err)
}

func validateOutputPath(output string) (string, error) {
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafeOutputPath)
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return "", fmt.Errorf("resolve support-bundle output path: %w", err)
	}
	if filepath.Base(absolute) == "." || filepath.Base(absolute) == string(filepath.Separator) {
		return "", fmt.Errorf("%w: invalid filename", ErrUnsafeOutputPath)
	}
	if err := rejectSymlinkComponents(absolute); err != nil {
		return "", err
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect support-bundle output: %w", err)
	}
	parent := filepath.Dir(absolute)
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect support-bundle output directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: parent is not a directory", ErrUnsafeOutputPath)
	}
	return absolute, nil
}

func rejectSymlinkComponents(target string) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	root := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(absolute, root)
	components := strings.Split(remainder, string(filepath.Separator))
	current := root
	for index, component := range components {
		if component == "" {
			continue
		}
		next := filepath.Join(current, component)
		info, statErr := os.Lstat(next)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect support-bundle path component: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// macOS exposes system-owned roots such as /var and /tmp through a
			// first-component symlink. Resolve that immutable root alias, but
			// continue to reject every later/user-controlled symlink component.
			if index != 0 {
				return fmt.Errorf("%w: symbolic-link component", ErrUnsafeOutputPath)
			}
			resolved, resolveErr := filepath.EvalSymlinks(next)
			if resolveErr != nil {
				return fmt.Errorf("resolve support-bundle root alias: %w", resolveErr)
			}
			resolvedInfo, resolveErr := os.Stat(resolved)
			if resolveErr != nil || !resolvedInfo.IsDir() {
				return fmt.Errorf("%w: invalid root alias", ErrUnsafeOutputPath)
			}
			current = resolved
			continue
		}
		current = next
	}
	return nil
}

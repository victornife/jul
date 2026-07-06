// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"jul/internal/atomicfile"
)

// pluginUploadResponse is the JSON returned on a successful .wasm upload.
type pluginUploadResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// wasmMagic is the WebAssembly binary format magic number: \x00asm.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// validPluginFilename reports whether name is a safe plugin filename: a single
// path component ending in ".wasm" whose base is non-empty and which contains
// only ASCII letters, digits, '.', '_' or '-'. It rejects path separators (of
// either OS), "..", leading dots, and over-long names. This keeps an uploaded
// module inside the upload directory (path-traversal defense) and blocks
// surprising or non-wasm filenames before anything is written to disk.
func validPluginFilename(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if !strings.HasSuffix(name, ".wasm") {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	if strings.TrimSuffix(name, ".wasm") == "" {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// handlePluginUpload serves POST /api/plugins/upload. It accepts a multipart
// form with a single file field named "wasm", validates the magic number,
// writes the file atomically to the configured upload directory, and returns
// the stored path. The upload endpoint is disabled when PluginUploadEnabled is
// explicitly false or when PluginUploadMaxSize is non-positive.
func (s *Server) handlePluginUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	if s.cfg.PluginUploadEnabled != nil && !*s.cfg.PluginUploadEnabled {
		http.Error(w, "plugin upload disabled", http.StatusForbidden)
		return
	}
	if s.cfg.PluginUploadMaxSize <= 0 {
		http.Error(w, "plugin upload disabled", http.StatusForbidden)
		return
	}

	maxBytes := int64(s.cfg.PluginUploadMaxSize) << 20 // MB -> bytes
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if err := r.ParseMultipartForm(maxBytes); err != nil {
		if err.Error() == "multipart: message too large" || err.Error() == "http: request body too large" {
			http.Error(w, fmt.Sprintf("file exceeds %d MB limit", s.cfg.PluginUploadMaxSize), http.StatusRequestEntityTooLarge)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form"})
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	file, header, err := r.FormFile("wasm")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing 'wasm' file field"})
		return
	}
	defer file.Close()

	// Read first 8 bytes to validate magic number and version.
	magic := make([]byte, 8)
	if _, err := io.ReadFull(file, magic); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too short to be a valid WASM module"})
		return
	}
	if len(magic) < 4 || string(magic[:4]) != string(wasmMagic) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid WASM module: magic number mismatch"})
		return
	}
	// magic[4:8] is the WASM version; version 1 is \x01\x00\x00\x00.
	// We accept version 1 only for now.
	if magic[4] != 0x01 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported WASM version: %d", magic[4])})
		return
	}

	// Seek back to the beginning so we can write the full file.
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "internal error: seek failed", http.StatusInternalServerError)
			return
		}
	} else {
		// Fallback: reopen from the multipart form. This should not happen for
		// *multipart.File implementations, but we handle it defensively.
		file.Close()
		file, _, err = r.FormFile("wasm")
		if err != nil {
			http.Error(w, "internal error: re-open failed", http.StatusInternalServerError)
			return
		}
		defer file.Close()
	}

	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		http.Error(w, "failed to read upload", http.StatusInternalServerError)
		return
	}

	// The magic check already read 8 bytes; the full read includes them.
	if len(data) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too short to be a valid WASM module"})
		return
	}

	// Ensure upload directory exists.
	dir := s.cfg.PluginUploadDir
	if dir == "" {
		dir = "./jul-data/plugins"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.log.Error("plugin upload: failed to create upload directory", "dir", dir, "error", err)
		http.Error(w, "failed to prepare upload directory", http.StatusInternalServerError)
		return
	}

	name := filepath.Base(header.Filename)
	if !validPluginFilename(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename: must be a simple <name>.wasm using letters, digits, '.', '_' or '-'"})
		return
	}
	dest := filepath.Join(dir, name)
	// Defense in depth: the resolved destination must sit directly inside the
	// upload directory. validPluginFilename already rejects separators and "..",
	// but this guards against any surprising Join/Clean interaction.
	if filepath.Dir(dest) != filepath.Clean(dir) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename"})
		return
	}

	if err := atomicfile.Write(dest, data, 0o600); err != nil {
		s.log.Error("plugin upload: atomic write failed", "path", dest, "error", err)
		http.Error(w, "failed to store upload", http.StatusInternalServerError)
		return
	}

	s.log.Info("plugin uploaded", "name", name, "path", dest, "size", len(data))

	s.hub.Broadcast(Event{
		Type: "plugin_uploaded",
		Data: json.RawMessage(fmt.Sprintf(`{"name":%q,"path":%q,"size":%d}`, name, dest, len(data))),
	})

	writeJSON(w, http.StatusOK, pluginUploadResponse{
		Name: name,
		Path: dest,
		Size: int64(len(data)),
	})
}

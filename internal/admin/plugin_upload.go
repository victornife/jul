// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

type pluginUploadResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

func validPluginFilename(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if !strings.HasSuffix(name, ".wasm") || strings.HasPrefix(name, ".") || strings.TrimSuffix(name, ".wasm") == "" {
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

func (s *Server) handlePluginUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	// The mux pinned this generation before authentication. Direct unit calls
	// still get exactly one live load here. Nil upload_enabled preserves the
	// established/default-enabled semantics from config normalization.
	snap := s.requestAdminSnapshot(r)
	cfg := snap.cfg
	if (cfg.PluginUploadEnabled != nil && !*cfg.PluginUploadEnabled) || cfg.PluginUploadMaxSize <= 0 {
		http.Error(w, "plugin upload disabled", http.StatusForbidden)
		return
	}
	maxMB := cfg.PluginUploadMaxSize
	maxBytes := int64(maxMB) << 20
	dir := normalizePluginUploadDir(cfg.PluginUploadDir)

	// Disabled requests returned above without touching the body. An admitted
	// request now owns this captured size/dir policy for its complete lifetime.
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		if err.Error() == "multipart: message too large" || err.Error() == "http: request body too large" {
			http.Error(w, fmt.Sprintf("file exceeds %d MB limit", maxMB), http.StatusRequestEntityTooLarge)
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

	name := filepath.Base(header.Filename)
	if !validPluginFilename(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename: must be a simple <name>.wasm using letters, digits, '.', '_' or '-'"})
		return
	}

	magic := make([]byte, 8)
	if _, err := io.ReadFull(file, magic); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too short to be a valid WASM module"})
		return
	}
	if string(magic[:4]) != string(wasmMagic) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid WASM module: magic number mismatch"})
		return
	}
	if magic[4] != 0x01 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported WASM version: %d", magic[4])})
		return
	}

	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "internal error: seek failed", http.StatusInternalServerError)
			return
		}
	} else {
		_ = file.Close()
		file, _, err = r.FormFile("wasm")
		if err != nil {
			http.Error(w, "internal error: re-open failed", http.StatusInternalServerError)
			return
		}
		defer file.Close()
	}

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		http.Error(w, "failed to read upload", http.StatusInternalServerError)
		return
	}
	if int64(len(data)) > maxBytes {
		http.Error(w, fmt.Sprintf("file exceeds %d MB limit", maxMB), http.StatusRequestEntityTooLarge)
		return
	}
	if len(data) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too short to be a valid WASM module"})
		return
	}

	root, err := openPluginUploadRoot(dir)
	if err != nil {
		s.log.Error("plugin upload: failed to open captured upload directory", "error", err)
		http.Error(w, "failed to prepare upload directory", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	if err := writePluginUploadFile(root, name, data); err != nil {
		s.log.Error("plugin upload: atomic confined write failed", "name", name, "error", err)
		http.Error(w, "failed to store upload", http.StatusInternalServerError)
		return
	}

	dest := filepath.Join(dir, name)
	s.log.Info("plugin uploaded", "name", name, "size", len(data))
	s.hub.Broadcast(Event{
		Type: "plugin_uploaded",
		Data: json.RawMessage(fmt.Sprintf(`{"name":%q,"path":%q,"size":%d}`, name, dest, len(data))),
	})
	writeJSON(w, http.StatusOK, pluginUploadResponse{Name: name, Path: dest, Size: int64(len(data))})
}

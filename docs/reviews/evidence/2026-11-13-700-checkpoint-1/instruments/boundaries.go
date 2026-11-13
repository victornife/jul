// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type files struct {
	BodyCount      int   `json:"body_count"`
	BodyBytes      int64 `json:"body_bytes"`
	MultipartCount int   `json:"multipart_count"`
	MultipartBytes int64 `json:"multipart_bytes"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.MkdirTemp(os.Getenv("JUL_PROBE_TMP"), "jul-700-checkpoint-1-")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(map[string]any{
		"kind": "environment", "go_version": runtime.Version(),
		"goos": runtime.GOOS, "goarch": runtime.GOARCH,
		"gomaxprocs": runtime.GOMAXPROCS(0),
	}); err != nil {
		return err
	}
	if err := guest(enc); err != nil {
		return fmt.Errorf("guest: %w (temporary root %s)", err, root)
	}
	for _, c := range []struct {
		name, filename string
		parts, size    int
	}{
		{"spill_and_file", "a.bin", 1, 256 << 10},
		{"empty_files", "a.bin", 32, 0},
	} {
		if err := storage(enc, root, c.name, c.filename, c.parts, c.size); err != nil {
			return fmt.Errorf("%s: %w (temporary root %s)", c.name, err, root)
		}
	}
	for _, parts := range []string{"ABFHZ", "ABCFHZ"} {
		for i := 1; i <= 3; i++ {
			if err := audit(enc, root, parts, i); err != nil {
				return fmt.Errorf("audit %s: %w (temporary root %s)", parts, err, root)
			}
		}
	}
	if err := os.Remove(root); err != nil {
		return fmt.Errorf("remove empty temporary root %s: %w", root, err)
	}
	return nil
}

func guest(enc *json.Encoder) error {
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(256))
	defer r.Close(ctx)
	// One exported memory with minimum one page and maximum 256 pages.
	module := []byte{
		0, 'a', 's', 'm', 1, 0, 0, 0,
		5, 5, 1, 1, 1, 0x80, 2,
		7, 10, 1, 6, 'm', 'e', 'm', 'o', 'r', 'y', 2, 0,
	}
	compiled, err := r.CompileModule(ctx, module)
	if err != nil {
		return err
	}
	runtime.GC()
	var before, live runtime.MemStats
	runtime.ReadMemStats(&before)
	modules := make([]api.Module, 0, 4)
	for i := 0; i < 4; i++ {
		m, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(strconv.Itoa(i)))
		if err != nil {
			return err
		}
		modules = append(modules, m)
		if m.Memory().Size() != 1<<16 {
			return fmt.Errorf("unexpected initial memory: %d", m.Memory().Size())
		}
		if previous, ok := m.Memory().Grow(255); !ok || previous != 1 {
			return fmt.Errorf("grow to configured maximum: previous=%d ok=%v", previous, ok)
		}
		if _, ok := m.Memory().Grow(1); ok {
			return fmt.Errorf("growth beyond configured maximum succeeded")
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&live)
	runtime.KeepAlive(modules)
	if err := enc.Encode(map[string]any{
		"kind": "guest", "instances": len(modules), "pages_per_instance": 256,
		"max_bytes_per_instance": 256 << 16, "initial_bytes_per_instance": 1 << 16,
		"grew_to_max": true, "growth_past_max_rejected": true,
		"heap_alloc_delta_bytes": int64(live.HeapAlloc) - int64(before.HeapAlloc),
	}); err != nil {
		return err
	}
	for _, m := range modules {
		if err := m.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}

func caseDir(root string) (string, error) {
	dir, err := os.MkdirTemp(root, "case-")
	if err != nil {
		return "", err
	}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func makeWAF(directives string) (coraza.WAF, io.Closer, error) {
	w, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(directives))
	if err != nil {
		return nil, nil, err
	}
	closer, ok := w.(io.Closer)
	if !ok {
		return nil, nil, fmt.Errorf("Coraza WAF does not expose Close")
	}
	return w, closer, nil
}

func writeBody(w coraza.WAF, contentType string, body []byte) (types.Transaction, error) {
	tx := w.NewTransaction()
	tx.ProcessConnection("127.0.0.1", 1234, "127.0.0.1", 8080)
	tx.ProcessURI("/synthetic", "POST", "HTTP/1.1")
	tx.AddRequestHeader("Content-Type", contentType)
	tx.AddRequestHeader("Content-Length", strconv.Itoa(len(body)))
	if interruption := tx.ProcessRequestHeaders(); interruption != nil {
		return nil, fmt.Errorf("request headers interrupted: %+v", interruption)
	}
	split := min(len(body), 64<<10)
	for _, chunk := range [][]byte{body[:split], body[split:]} {
		if len(chunk) == 0 {
			continue
		}
		interruption, n, err := tx.WriteRequestBody(chunk)
		if err != nil || interruption != nil || n != len(chunk) {
			return nil, fmt.Errorf("body write: n=%d want=%d interruption=%v error=%v", n, len(chunk), interruption, err)
		}
	}
	return tx, nil
}

func countFiles(dir string) (files, error) {
	var out files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return out, err
		}
		switch {
		case strings.HasPrefix(entry.Name(), "body"):
			out.BodyCount++
			out.BodyBytes += info.Size()
		case strings.HasPrefix(entry.Name(), "crzmp"):
			out.MultipartCount++
			out.MultipartBytes += info.Size()
		}
	}
	return out, nil
}

func storage(enc *json.Encoder, root, name, filename string, parts, size int) error {
	dir, err := caseDir(root)
	if err != nil {
		return err
	}
	ignored := filepath.Join(dir, "ignored")
	if err := os.Mkdir(ignored, 0700); err != nil {
		return err
	}
	directives := fmt.Sprintf("SecRuleEngine On\nSecRequestBodyAccess On\nSecRequestBodyLimit 1048576\nSecRequestBodyInMemoryLimit 131072\nSecAuditEngine Off\nSecTmpDir %s\n", ignored)
	w, closer, err := makeWAF(directives)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("jul-checkpoint-boundary"); err != nil {
		return err
	}
	payload := bytes.Repeat([]byte("x"), size)
	for i := 0; i < parts; i++ {
		part, err := writer.CreateFormFile(fmt.Sprintf("file%d", i), filename)
		if err != nil {
			return err
		}
		if _, err := part.Write(payload); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	tx, err := writeBody(w, writer.FormDataContentType(), body.Bytes())
	if err != nil {
		return err
	}
	before, err := countFiles(dir)
	if err != nil {
		return err
	}
	if interruption, err := tx.ProcessRequestBody(); err != nil || interruption != nil {
		return fmt.Errorf("process body: interruption=%v error=%v", interruption, err)
	}
	during, err := countFiles(dir)
	if err != nil {
		return err
	}
	ignoredFiles, err := os.ReadDir(ignored)
	if err != nil {
		return err
	}
	tx.ProcessLogging()
	if err := tx.Close(); err != nil {
		return err
	}
	after, err := countFiles(dir)
	if err != nil {
		return err
	}
	if err := closer.Close(); err != nil {
		return err
	}
	if during.MultipartCount != parts || during.MultipartBytes != int64(parts*size) ||
		len(ignoredFiles) != 0 || after != (files{}) {
		return fmt.Errorf("unexpected files: during=%+v ignored=%d after=%+v", during, len(ignoredFiles), after)
	}
	if name == "spill_and_file" && (before.BodyCount != 1 || before.BodyBytes != int64(body.Len())) {
		return fmt.Errorf("unexpected body spill: %+v, body length %d", before, body.Len())
	}
	if name == "empty_files" && before.BodyCount != 0 {
		return fmt.Errorf("unexpected spill for empty-file case: %+v", before)
	}
	if err := enc.Encode(map[string]any{
		"kind": "storage", "case": name, "request_bytes": body.Len(),
		"file_parts": parts, "payload_bytes_per_part": size,
		"after_write": before, "after_process": during, "after_close": after,
		"sectmpdir_entries": len(ignoredFiles),
	}); err != nil {
		return err
	}
	if err := os.Remove(ignored); err != nil {
		return err
	}
	return os.Remove(dir)
}

func audit(enc *json.Encoder, root, parts string, repetition int) error {
	dir, err := caseDir(root)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "audit.json")
	directives := fmt.Sprintf("SecRuleEngine On\nSecRequestBodyAccess On\nSecRequestBodyLimit 1048576\nSecRequestBodyInMemoryLimit 131072\nSecAuditEngine On\nSecAuditLogType Serial\nSecAuditLogFormat JSON\nSecAuditLogParts %s\nSecAuditLog %s\n", parts, path)
	w, closer, err := makeWAF(directives)
	if err != nil {
		return err
	}
	body := bytes.Repeat([]byte("x"), 256<<10)
	tx, err := writeBody(w, "application/octet-stream", body)
	if err != nil {
		return err
	}
	if interruption, err := tx.ProcessRequestBody(); err != nil || interruption != nil {
		return fmt.Errorf("process body: interruption=%v error=%v", interruption, err)
	}
	tx.ProcessResponseHeaders(204, "HTTP/1.1")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	tx.ProcessLogging()
	runtime.ReadMemStats(&after)
	if err := tx.Close(); err != nil {
		return err
	}
	if err := closer.Close(); err != nil {
		return err
	}
	log, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	containsBody := bytes.Contains(log, body)
	if len(log) == 0 || containsBody != strings.Contains(parts, "C") {
		return fmt.Errorf("unexpected audit output: length=%d containsBody=%v parts=%s", len(log), containsBody, parts)
	}
	if err := enc.Encode(map[string]any{
		"kind": "audit", "parts": parts, "repetition": repetition,
		"request_bytes": len(body), "audit_bytes": len(log),
		"contains_body": containsBody, "logging_total_alloc_bytes": after.TotalAlloc - before.TotalAlloc,
	}); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Remove(dir)
}

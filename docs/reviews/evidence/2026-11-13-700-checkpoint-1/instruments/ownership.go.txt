// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/experimental/plugins"
	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
	"jul/internal/config"
	"jul/internal/handler"
)

var retained []plugintypes.AuditLog

type retainingWriter struct{}

func (*retainingWriter) Init(plugintypes.AuditLogConfig) error { return nil }
func (*retainingWriter) Write(log plugintypes.AuditLog) error {
	retained = append(retained, log)
	return nil
}
func (*retainingWriter) Close() error { return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.MkdirTemp(os.Getenv("JUL_PROBE_TMP"), "jul-review-")
	if err != nil {
		return err
	}
	if err := parserIO(root); err != nil {
		return fmt.Errorf("parser I/O: %w (private directory %s)", err, root)
	}
	if err := staticExposure(root); err != nil {
		return fmt.Errorf("static exposure: %w (private directory %s)", err, root)
	}
	if err := auditAlias(); err != nil {
		return fmt.Errorf("audit alias: %w (private directory %s)", err, root)
	}
	if err := os.Remove(root); err != nil {
		return err
	}
	return nil
}

func staticExposure(root string) error {
	webroot := filepath.Join(root, "webroot")
	if err := os.Mkdir(webroot, 0700); err != nil {
		return err
	}
	spoolFile, err := os.CreateTemp(webroot, "body*")
	if err != nil {
		return err
	}
	if _, err := spoolFile.WriteString("synthetic-body"); err != nil {
		spoolFile.Close()
		return err
	}
	if err := spoolFile.Close(); err != nil {
		return err
	}
	h, err := handler.NewStatic(config.ServerConfig{}, config.LocationConfig{Root: webroot})
	if err != nil {
		return err
	}
	closer, ok := h.(interface{ Close() error })
	if !ok {
		return fmt.Errorf("static handler does not expose Close")
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/"+filepath.Base(spoolFile.Name()), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if err := closer.Close(); err != nil {
		return err
	}
	if err := os.Remove(spoolFile.Name()); err != nil {
		return err
	}
	if err := os.Remove(webroot); err != nil {
		return err
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "synthetic-body" {
		return fmt.Errorf("static response = %d %q", rec.Code, rec.Body.String())
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"kind": "static_spool_exposure", "status": rec.Code, "served": true,
	})
}

func parserIO(root string) error {
	goodDir := filepath.Join(root, "writable")
	badDir := filepath.Join(root, "unwritable")
	for _, dir := range []string{goodDir, badDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	if err := m.SetBoundary("jul-security-probe"); err != nil {
		return err
	}
	file, err := m.CreateFormFile("upload", "synthetic.txt")
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte("synthetic")); err != nil {
		return err
	}
	if err := m.WriteField("danger", "attack"); err != nil {
		return err
	}
	if err := m.Close(); err != nil {
		return err
	}
	type result struct {
		Interrupted bool   `json:"interrupted"`
		Status      int    `json:"status"`
		Error       bool   `json:"returned_error"`
		BodyError   string `json:"body_error"`
	}
	exercise := func(dir string) (result, error) {
		directives := fmt.Sprintf("SecRuleEngine On\nSecRequestBodyAccess On\nSecRequestBodyLimit 1048576\nSecRequestBodyInMemoryLimit 131072\nSecUploadDir %s\nSecAuditEngine Off\nSecRule ARGS:danger \"@streq attack\" \"id:990100,phase:2,deny,status:403\"\n", dir)
		w, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(directives))
		if err != nil {
			return result{}, err
		}
		if dir == badDir {
			// Coraza checks directory writability during WAF construction.
			// Revoke it only after that check, before transaction parsing.
			if err := os.Chmod(dir, 0500); err != nil {
				return result{}, err
			}
		}
		closer, ok := w.(io.Closer)
		if !ok {
			return result{}, fmt.Errorf("WAF has no Close")
		}
		tx := w.NewTransaction()
		tx.ProcessConnection("127.0.0.1", 1234, "127.0.0.1", 8080)
		tx.ProcessURI("/synthetic", "POST", "HTTP/1.1")
		tx.AddRequestHeader("Content-Type", m.FormDataContentType())
		tx.AddRequestHeader("Content-Length", strconv.Itoa(body.Len()))
		if interrupt := tx.ProcessRequestHeaders(); interrupt != nil {
			return result{}, fmt.Errorf("header interruption")
		}
		if interrupt, n, err := tx.WriteRequestBody(body.Bytes()); err != nil || interrupt != nil || n != body.Len() {
			return result{}, fmt.Errorf("body write: n=%d interrupt=%v error=%v", n, interrupt, err)
		}
		interrupt, processErr := tx.ProcessRequestBody()
		state, ok := tx.(plugintypes.TransactionState)
		if !ok {
			return result{}, fmt.Errorf("transaction state unavailable")
		}
		got := result{Interrupted: interrupt != nil, Error: processErr != nil, BodyError: state.Variables().RequestBodyError().Get()}
		if interrupt != nil {
			got.Status = interrupt.Status
		}
		tx.ProcessLogging()
		if err := tx.Close(); err != nil {
			return result{}, err
		}
		if err := closer.Close(); err != nil {
			return result{}, err
		}
		return got, nil
	}
	normal, err := exercise(goodDir)
	if err != nil {
		return err
	}
	failed, err := exercise(badDir)
	if err != nil {
		return err
	}
	if !normal.Interrupted || normal.Status != 403 || failed.Interrupted || failed.Error || failed.BodyError != "1" {
		return fmt.Errorf("unexpected parser results: normal=%+v failed=%+v", normal, failed)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": "multipart_io", "normal": normal, "create_failure": failed}); err != nil {
		return err
	}
	if err := os.Chmod(badDir, 0700); err != nil {
		return err
	}
	if err := os.Remove(goodDir); err != nil {
		return err
	}
	return os.Remove(badDir)
}

func auditAlias() error {
	plugins.RegisterAuditLogWriter("julreview-retain", func() plugintypes.AuditLogWriter { return &retainingWriter{} })
	w, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives("SecRuleEngine On\nSecAuditEngine On\nSecAuditLogType julreview-retain\nSecAuditLogParts ABFHZ\nSecAuditLog /dev/null\n"))
	if err != nil {
		return err
	}
	closer, ok := w.(io.Closer)
	if !ok {
		return fmt.Errorf("WAF has no Close")
	}
	first := w.NewTransaction()
	first.ProcessConnection("127.0.0.1", 1234, "127.0.0.1", 8080)
	first.ProcessURI("/synthetic", "GET", "HTTP/1.1")
	first.AddGetRequestArgument("sample", "alpha")
	first.ProcessRequestHeaders()
	first.ProcessResponseHeaders(204, "HTTP/1.1")
	first.ProcessLogging()
	if len(retained) != 1 {
		return fmt.Errorf("retained records: %d", len(retained))
	}
	hasValue := func(log plugintypes.AuditLog, value string) bool {
		args := log.Transaction().Request().Args()
		if args == nil {
			return false
		}
		for _, match := range args.FindAll() {
			if match.Key() == "sample" && match.Value() == value {
				return true
			}
		}
		return false
	}
	before := hasValue(retained[0], "alpha")
	if err := first.Close(); err != nil {
		return err
	}
	second := w.NewTransaction()
	reused := reflect.ValueOf(first).Pointer() == reflect.ValueOf(second).Pointer()
	second.AddGetRequestArgument("sample", "beta")
	after := hasValue(retained[0], "beta")
	if !before || !reused || !after {
		return fmt.Errorf("unexpected alias results: before=%v reused=%v after=%v", before, reused, after)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": "audit_alias", "first_value_before_close": before, "transaction_reused": reused, "second_value_visible_in_first_record": after}); err != nil {
		return err
	}
	if err := second.Close(); err != nil {
		return err
	}
	retained = nil
	return closer.Close()
}

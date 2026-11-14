// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"
)

// ADR 0032 minimum-budget rule: ctl:requestBodyLimit and ctl:responseBodyLimit
// raise only the transaction's limit check. Coraza sizes each body buffer from
// the compiled limits when the transaction is created, so the bytes it stores
// and parses stay within the compiled limits that inspectionFacts reports.
// This pins that upstream behaviour; a Coraza change that lets a raise grow a
// buffer must fail here before the static minimum budget can understate it.
func TestCtlBodyLimitRaiseCannotExceedCompiledBuffers(t *testing.T) {
	const compiled = 1024
	const raised = 1 << 20
	base := []string{
		"SecRuleEngine On",
		"SecRequestBodyAccess On",
		"SecResponseBodyAccess On",
		"SecResponseBodyMimeType text/plain",
		"SecRequestBodyLimit 1024",
		"SecRequestBodyLimitAction ProcessPartial",
		"SecResponseBodyLimit 1024",
		"SecResponseBodyLimitAction ProcessPartial",
	}
	raise := []string{
		`SecAction "id:1,phase:1,pass,nolog,ctl:requestBodyLimit=1048576,ctl:responseBodyLimit=1048576"`,
		// A chained rule's ctl is reached through the parent rule.
		`SecRule REQUEST_METHOD "@streq POST" "id:2,phase:1,pass,nolog,chain"`,
		`SecRule REQUEST_URI "@beginsWith /" "ctl:requestBodyLimit=1048576"`,
	}
	newWAF := func(t *testing.T, directives []string) coraza.WAF {
		t.Helper()
		w, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(strings.Join(directives, "\n")))
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		return w
	}
	raisedWAF := newWAF(t, append(append([]string{}, base...), raise...))
	plainWAF := newWAF(t, base)

	st, err := compiledEngineState(raisedWAF)
	if err != nil {
		t.Fatal(err)
	}
	facts := inspectionFacts(st)
	if facts.RequestBodyLimit != compiled || facts.ResponseBodyLimit != compiled {
		t.Fatalf("facts = %+v, want compiled limits %d", facts, compiled)
	}
	payload := bytes.Repeat([]byte("a"), 4*compiled)
	start := func(t *testing.T, w coraza.WAF) types.Transaction {
		t.Helper()
		tx := w.NewTransaction()
		t.Cleanup(func() { _ = tx.Close() })
		tx.ProcessConnection("192.0.2.1", 40000, "192.0.2.2", 80)
		tx.ProcessURI("/upload", "POST", "HTTP/1.1")
		tx.AddRequestHeader("Content-Type", "text/plain")
		if it := tx.ProcessRequestHeaders(); it != nil {
			t.Fatalf("unexpected interruption: %+v", it)
		}
		return tx
	}
	raisedTx := start(t, raisedWAF)
	if got := txResponseBodyLimit(raisedTx, 0); got != raised {
		t.Fatalf("response ctl limit = %d, want %d", got, raised)
	}
	ic := &wafResponseInterceptor{tx: raisedTx, responseBodyLimit: compiled}
	if got := ic.responseBufferLimit(); got != compiled || !ic.responseWriteReachesLimit(make([]byte, compiled)) {
		t.Fatalf("response admission limit = %d, reaches compiled limit=%v, want %d/true", got, ic.responseWriteReachesLimit(make([]byte, compiled)), compiled)
	}
	stored := func(r io.Reader, err error) int {
		if err != nil {
			return -1
		}
		b, err := io.ReadAll(r)
		if err != nil {
			return -1
		}
		return len(b)
	}

	// Control: without a raise the transaction check truncates silently.
	tx := start(t, plainWAF)
	if _, _, err := tx.WriteRequestBody(payload); err != nil {
		t.Fatalf("control write: %v", err)
	}
	if n := stored(tx.RequestBodyReader()); n != compiled {
		t.Fatalf("control stored %d, want %d", n, compiled)
	}

	requestWrites := map[string]func(types.Transaction) error{
		"WriteRequestBody whole": func(tx types.Transaction) error {
			_, _, err := tx.WriteRequestBody(payload)
			return err
		},
		"WriteRequestBody chunks": func(tx types.Transaction) error {
			for off := 0; off < len(payload); off += compiled / 2 {
				if _, _, err := tx.WriteRequestBody(payload[off : off+compiled/2]); err != nil {
					return err
				}
			}
			return nil
		},
		"ReadRequestBodyFrom sized": func(tx types.Transaction) error {
			_, _, err := tx.ReadRequestBodyFrom(bytes.NewReader(payload))
			return err
		},
		"ReadRequestBodyFrom stream": func(tx types.Transaction) error {
			_, _, err := tx.ReadRequestBodyFrom(io.MultiReader(bytes.NewReader(payload)))
			return err
		},
	}
	for name, write := range requestWrites {
		t.Run(name, func(t *testing.T) {
			tx := start(t, raisedWAF)
			err := write(tx)
			// The raise ran: the transaction check no longer truncates, so the
			// buffer's own compiled limit refuses the excess.
			if err == nil || !strings.Contains(err.Error(), "limit reached") {
				t.Fatalf("write error = %v, want buffer limit refusal", err)
			}
			if n := stored(tx.RequestBodyReader()); n < 0 || n > compiled {
				t.Fatalf("stored %d request bytes, compiled limit %d (raise to %d)", n, compiled, raised)
			}
		})
	}

	responseWrites := map[string]func(types.Transaction) error{
		"WriteResponseBody": func(tx types.Transaction) error {
			_, _, err := tx.WriteResponseBody(payload)
			return err
		},
		"ReadResponseBodyFrom": func(tx types.Transaction) error {
			_, _, err := tx.ReadResponseBodyFrom(io.MultiReader(bytes.NewReader(payload)))
			return err
		},
	}
	for name, write := range responseWrites {
		t.Run(name, func(t *testing.T) {
			tx := start(t, raisedWAF)
			if _, err := tx.ProcessRequestBody(); err != nil {
				t.Fatal(err)
			}
			tx.AddResponseHeader("Content-Type", "text/plain")
			if it := tx.ProcessResponseHeaders(200, "HTTP/1.1"); it != nil {
				t.Fatalf("unexpected interruption: %+v", it)
			}
			err := write(tx)
			if err == nil || !strings.Contains(err.Error(), "limit reached") {
				t.Fatalf("write error = %v, want buffer limit refusal", err)
			}
			if n := stored(tx.ResponseBodyReader()); n < 0 || n > compiled {
				t.Fatalf("stored %d response bytes, compiled limit %d (raise to %d)", n, compiled, raised)
			}
		})
	}
}

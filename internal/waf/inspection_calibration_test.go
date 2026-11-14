// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
)

// retained reports the live heap f leaves behind. Transient garbage (CRS
// transformations allocate heavily) is not reserved state.
func retained(f func()) uint64 {
	runtime.GC()
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&b)
	if b.HeapAlloc < a.HeapAlloc {
		return 0
	}
	return b.HeapAlloc - a.HeapAlloc
}

// allocated reports the bytes f allocates, transient ones included: an
// audit record and its formatted output are live together.
func allocated(f func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

type calibrationShape struct {
	name string
	req  func() *http.Request
}

func multipartRequest() *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i := 0; i < 50; i++ {
		_ = mw.WriteField(fmt.Sprintf("f%d", i), strings.Repeat("v", 512))
	}
	fw, _ := mw.CreateFormFile("upload", "a.bin")
	_, _ = fw.Write(bytes.Repeat([]byte{1, 2, 3}, 20<<10))
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/up", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func calibrationShapes(argLimit int) []calibrationShape {
	post := func(ct, body string) func() *http.Request {
		return func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/p?q=1", strings.NewReader(body))
			r.Header.Set("Content-Type", ct)
			return r
		}
	}
	var mixedQ, mixedB strings.Builder
	for i := 0; i < argLimit; i++ {
		fmt.Fprintf(&mixedQ, "q%d=%%3Cscript%%3E&", i)
		fmt.Fprintf(&mixedB, "b%d=union+select+%d&", i, i)
	}
	var js strings.Builder
	js.WriteString(`{"items":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			js.WriteByte(',')
		}
		fmt.Fprintf(&js, `{"k%d":"%s"}`, i, strings.Repeat("j", 64))
	}
	js.WriteString("]}")
	var xml strings.Builder
	xml.WriteString("<root>")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&xml, "<e a=\"%d\">%s</e>", i, strings.Repeat("x", 64))
	}
	xml.WriteString("</root>")
	return []calibrationShape{
		{"query", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/q?"+mixedQ.String(), nil)
		}},
		{"large-headers", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/h", nil)
			for i := 0; i < 200; i++ {
				r.Header.Set(fmt.Sprintf("X-H%d", i), strings.Repeat("h", 256))
			}
			r.Header.Set("X-Big", strings.Repeat("\x01", 32<<10))
			return r
		}},
		{"urlencoded", post("application/x-www-form-urlencoded", "a=1&b="+strings.Repeat("b", 64<<10))},
		{"mixed-args-at-limit", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/m?"+mixedQ.String(), strings.NewReader(mixedB.String()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		}},
		{"json", post("application/json", js.String())},
		{"xml", post("text/xml", xml.String())},
		{"multipart", multipartRequest},
		{"raw", post("application/octet-stream", strings.Repeat("\x00r", 32<<10))},
	}
}

// TestWAFReservationsCoverMeasuredAllocation calibrates ADR 0032's R1, R2 and
// R5 estimates against Coraza (with CRS) on representative request shapes:
// bodies spilled to disk, partial processing, both orders of audit parts H
// and K, and ARGS_PATH asserted empty so R1 needs no term for it.
func TestWAFReservationsCoverMeasuredAllocation(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("allocation calibration")
	}
	for _, c := range []struct {
		name, rules string
		limit       int64
	}{
		{"H-before-K", "SecAuditLogParts ABCIJDEFHKZ", 1 << 20},
		{"K-before-H", "SecAuditLogParts ABCDEFKHZ", 1 << 20},
		{"spill", "SecAuditLogParts ABCIJDEFHKZ\nSecRequestBodyInMemoryLimit 1024", 1 << 20},
		{"partial", "SecAuditLogParts ABCIJDEFHKZ\nSecRequestBodyLimitAction ProcessPartial", 4096},
	} {
		cfg := config.WAFConfig{Enabled: true, CRSEnabled: true, InlineRules: "SecAuditEngine On\nSecAuditLog " + os.DevNull + "\n" + c.rules}
		applyTestDefaults(&cfg)
		cfg.RequestBodyLimit = config.Size(c.limit)
		fw, err := New(context.Background(), cfg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if c.name == "partial" && (fw.state.requestBodyLimit != 4096 || fw.state.requestBodyLimitReject) {
			t.Fatalf("partial configuration compiled to limit %d (reject %v)", fw.state.requestBodyLimit, fw.state.requestBodyLimitReject)
		}
		if c.name == "spill" && fw.state.requestBodyInMemoryLimit != 1024 {
			t.Fatalf("spill configuration compiled to in-memory limit %d", fw.state.requestBodyInMemoryLimit)
		}
		for _, sh := range calibrationShapes(int(fw.argumentLimit())) {
			t.Run(c.name+"/"+sh.name, func(t *testing.T) {
				calibrateShape(t, fw, sh.req())
			})
		}
		_ = fw.Close()
	}
}

func TestWAFResponseReservationsCoverMeasuredAllocation(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("allocation calibration")
	}
	shapes := []struct {
		name, mime string
		headers    http.Header
		body       []byte
		limit      int
	}{
		{
			name: "repeated-auth-and-cookie-headers",
			headers: func() http.Header {
				h := make(http.Header)
				for i := 0; i < 128; i++ {
					h.Add("Set-Cookie", fmt.Sprintf("session-%d=%s; Path=/; HttpOnly", i, strings.Repeat("s", 192)))
				}
				for i := 0; i < 32; i++ {
					h.Add("WWW-Authenticate", fmt.Sprintf("Bearer realm=%q", strings.Repeat("r", 384)))
				}
				return h
			}(),
			body:  []byte(strings.Repeat("response ", 1024)),
			limit: 128 << 10,
		},
		{
			name: "urlencoded-parser",
			mime: "application/x-www-form-urlencoded",
			body: func() []byte {
				var b strings.Builder
				for i := 0; i < 1500; i++ {
					fmt.Fprintf(&b, "field%d=%s&", i, strings.Repeat("v", 24))
				}
				return []byte(b.String())
			}(),
			limit: 128 << 10,
		},
		{
			name:  "urlencoded-partial",
			mime:  "application/x-www-form-urlencoded",
			body:  []byte(strings.Repeat("field=1&", 1024)),
			limit: 4 << 10,
		},
		{
			name: "json-parser",
			mime: "application/json",
			body: func() []byte {
				var b strings.Builder
				b.WriteString(`{"items":[`)
				for i := 0; i < 500; i++ {
					if i > 0 {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, `{"field%d":"%s"}`, i, strings.Repeat("j", 64))
				}
				b.WriteString("]}")
				return []byte(b.String())
			}(),
			limit: 128 << 10,
		},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			rules := strings.Join([]string{
				"SecAuditEngine On",
				"SecAuditLog " + os.DevNull,
				"SecAuditLogParts ABCIJDEFHKZ",
				"SecResponseBodyAccess On",
				"SecResponseBodyMimeType application/json application/x-www-form-urlencoded text/plain",
				fmt.Sprintf("SecResponseBodyLimit %d", shape.limit),
				"SecResponseBodyLimitAction ProcessPartial",
			}, "\n")
			cfg := config.WAFConfig{
				Enabled:           true,
				CRSEnabled:        true,
				ResponseBodyCheck: true,
				InlineRules:       rules,
			}
			applyTestDefaults(&cfg)
			fw, err := New(context.Background(), cfg, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fw.Close() }()

			uri := "/response?mode=calibration"
			r := httptest.NewRequest(http.MethodGet, uri, nil)
			r.Header.Set("Accept", "text/plain")
			r.Header.Set("User-Agent", "jul-response-calibration")
			headers := make(http.Header, len(shape.headers)+2)
			for key, values := range shape.headers {
				headers[key] = append([]string(nil), values...)
			}
			headers.Set("Content-Type", shape.mime)
			headers.Set("Content-Length", fmt.Sprint(len(shape.body)))
			var (
				tx                        types.Transaction
				phase3, phase4, bodyBytes uint64
			)
			for range 3 {
				candidate, p3, p4, consumed := measureResponsePhases(t, fw, r, headers, shape.body)
				if tx == nil || p3+p4 < phase3+phase4 {
					if tx != nil {
						_ = tx.Close()
					}
					tx, phase3, phase4, bodyBytes = candidate, p3, p4, consumed
				} else {
					_ = candidate.Close()
				}
			}
			defer func() { _ = tx.Close() }()

			headerBytes := responseHeaderShape(headers)
			r3 := r3Estimate(headerBytes)
			args, ok := responseArgsHeld(tx)
			if !ok {
				t.Fatal("transaction response state unavailable")
			}
			r4 := r4Estimate(args, bodyBytes)
			if phase3 > r3 || phase4 > r4 || phase3+phase4 > r3+r4 {
				t.Errorf("response phases retained %d B (R3 %d B) + %d B (R4 %d B)", phase3, r3, phase4, r4)
			}
			if shape.limit < len(shape.body) && bodyBytes != uint64(shape.limit) {
				t.Errorf("partial response processed %d B, want %d B", bodyBytes, shape.limit)
			}
			calibrateResponseAudit(t, fw, tx, r, headers, bodyBytes)
		})
	}
}

func measureResponsePhases(t *testing.T, fw *Firewall, r *http.Request, headers http.Header, body []byte) (types.Transaction, uint64, uint64, uint64) {
	t.Helper()
	tx := fw.waf.NewTransaction()
	tx.ProcessConnection("192.0.2.1", 1234, "", 0)
	tx.ProcessURI(r.URL.RequestURI(), r.Method, r.Proto)
	for k, values := range r.Header {
		for _, value := range values {
			tx.AddRequestHeader(k, value)
		}
	}
	tx.AddRequestHeader("Host", r.Host)
	tx.ProcessRequestHeaders()
	tx.ProcessRequestBody()

	phase3 := retained(func() {
		for k, values := range headers {
			for _, value := range values {
				tx.AddResponseHeader(k, value)
			}
		}
		tx.ProcessResponseHeaders(http.StatusOK, r.Proto)
	})
	var (
		consumed int
		err      error
	)
	phase4 := retained(func() {
		_, consumed, err = tx.WriteResponseBody(body)
		if err != nil {
			t.Fatal(err)
		}
		if consumed == len(body) {
			_, err = tx.ProcessResponseBody()
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	return tx, phase3, phase4, uint64(consumed)
}

func calibrateResponseAudit(t *testing.T, fw *Firewall, tx types.Transaction, r *http.Request, headers http.Header, bodyBytes uint64) {
	t.Helper()
	st, err := readTxAuditState(tx)
	if err != nil {
		t.Fatal(err)
	}
	reqHeaderCount, reqHeaderBytes, _ := requestHeaderShape(r, r.URL.RequestURI())
	pre := auditPrecheck{
		parts:          st.parts,
		reqHeaderCount: reqHeaderCount,
		reqHeaderBytes: reqHeaderBytes,
		respBodyBytes:  bodyBytes,
		uri:            len(r.URL.RequestURI()),
		host:           len(r.Host),
		client:         len("192.0.2.1"),
		txID:           len(tx.ID()),
		matched:        tx.MatchedRules(),
	}
	for k, values := range headers {
		for _, value := range values {
			pre.respHeaderCount++
			pre.respHeaderBytes += uint64(len(k) + len(value))
		}
	}
	inner, ok := innerTx(tx)
	if !ok {
		t.Fatal("inner transaction unavailable")
	}
	var al plugintypes.AuditLog
	built := allocated(func() {
		al = inner.Addr().MethodByName("AuditLog").Call(nil)[0].Interface().(plugintypes.AuditLog)
	})
	shape := measureAuditRecord(al)
	if est := constructionEstimate(pre); built > est || built > shape.construction {
		t.Errorf("response audit construction allocated %d B; estimate %d B, measured bound %d B", built, est, shape.construction)
	}
	for kind, formatter := range map[formatterKind]plugintypes.AuditLogFormatter{
		formatterNative: fw.audit.writer.formatter,
		formatterJSON:   jsonFormatter(t),
	} {
		if formatter == nil || kind != classifyFormatter(formatter) {
			t.Fatalf("formatter %T not classified as %d", formatter, kind)
		}
		var out []byte
		used := allocated(func() { out, _ = formatter.Format(al) })
		if estimate := formatEstimate(shape, kind); used > estimate || uint64(len(out)) > estimate {
			t.Errorf("response formatter %d allocated %d B (output %d B); estimate %d B", kind, used, len(out), estimate)
		}
	}
}

func calibrateShape(t *testing.T, fw *Firewall, r *http.Request) {
	t.Helper()
	uri := r.URL.RequestURI()
	_, _, h := requestHeaderShape(r, uri)
	r1Worst := r1Estimate(min(fw.argumentLimit(), queryPairs(r.URL.RawQuery)), h)
	var body []byte
	if r.Body != nil {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		body = buf.Bytes()
	}
	// Retained heap is deterministic per transaction; other goroutines can
	// only add to a sample, so the smallest of a few fresh runs is used.
	var (
		tx             types.Transaction
		phase1, phase2 uint64
		bTotal         uint64
	)
	for range 3 {
		ctx, p1, p2, bt := measureRequestPhases(t, fw, r, uri, body)
		if tx == nil || p1+p2 < phase1+phase2 {
			if tx != nil {
				_ = tx.Close()
			}
			tx, phase1, phase2, bTotal = ctx, p1, p2, bt
		} else {
			_ = ctx.Close()
		}
	}
	defer func() { _ = tx.Close() }()
	n1, ok := argsHeld(tx, true)
	if !ok {
		t.Fatal("transaction state unavailable")
	}
	if r1 := r1Estimate(n1, h); phase1 > r1 || r1 > r1Worst {
		t.Errorf("phase 1 retained %d B; R1 = %d B (worst %d B)", phase1, r1, r1Worst)
	}
	r2Worst := r2Estimate(fw.argumentLimit(), uint64(len(body)))
	n, _ := argsHeld(tx, false)
	// Reservations are cumulative (ADR 0032 §4): phase-2 rules matching
	// ARGS_GET or headers retain state under R1's terms.
	r1 := r1Estimate(n1, h)
	if r2 := r2Estimate(n, bTotal); phase1+phase2 > r1+r2 || r2 > r2Worst {
		t.Errorf("phases 1-2 retained %d B; R1 + R2 = %d B (R2 worst %d B)", phase1+phase2, r1+r2, r2Worst)
	}
	calibrateAudit(t, fw, tx, r, uri, bTotal)
}

// measureRequestPhases runs phases 1 and 2 on a fresh transaction and
// returns it with the heap each phase retained. Phase-1 ARGS_GET counts are
// read later from the returned transaction.
func measureRequestPhases(t *testing.T, fw *Firewall, r *http.Request, uri string, body []byte) (types.Transaction, uint64, uint64, uint64) {
	t.Helper()
	tx := fw.waf.NewTransaction()
	phase1 := retained(func() {
		tx.ProcessConnection("192.0.2.1", 1234, "", 0)
		tx.ProcessURI(uri, r.Method, r.Proto)
		for k, vs := range r.Header {
			for _, v := range vs {
				tx.AddRequestHeader(k, v)
			}
		}
		tx.AddRequestHeader("Host", r.Host)
		tx.ProcessRequestHeaders()
	})
	bTotal := uint64(len(body))
	phase2 := retained(func() {
		if len(body) > 0 {
			if _, _, err := tx.WriteRequestBody(body); err != nil {
				t.Fatal(err)
			}
			bTotal = min(bTotal, uint64(fw.state.requestBodyLimit))
		}
		if _, err := tx.ProcessRequestBody(); err != nil {
			t.Fatal(err)
		}
	})
	return tx, phase1, phase2, bTotal
}

// calibrateAudit checks ARGS_PATH and the audit construction and format
// estimates on a transaction that has completed phases 1 and 2.
func calibrateAudit(t *testing.T, fw *Firewall, tx types.Transaction, r *http.Request, uri string, bTotal uint64) {
	t.Helper()
	s, _ := txState(tx)
	if p := s.Variables().ArgsPath().FindAll(); len(p) != 0 {
		t.Errorf("ARGS_PATH holds %d entries; R1 assumes none", len(p))
	}

	tx.ProcessResponseHeaders(http.StatusForbidden, "HTTP/1.1")
	st, err := readTxAuditState(tx)
	if err != nil {
		t.Fatal(err)
	}
	cnt, hb, _ := requestHeaderShape(r, uri)
	pre := auditPrecheck{
		parts: st.parts, reqHeaderCount: cnt + 1, reqHeaderBytes: hb + uint64(len("Host")+len(r.Host)),
		bodyBytes: bTotal, uri: len(uri), host: len(r.Host), client: len("192.0.2.1"),
		txID: len(tx.ID()), matched: tx.MatchedRules(),
	}
	pre.files = uint64(len(s.Variables().Files().FindAll()))
	est := constructionEstimate(pre)
	inner, ok := innerTx(tx)
	if !ok {
		t.Fatal("inner transaction unavailable")
	}
	var al plugintypes.AuditLog
	built := allocated(func() {
		al = inner.Addr().MethodByName("AuditLog").Call(nil)[0].Interface().(plugintypes.AuditLog)
	})
	shape := measureAuditRecord(al)
	if built > est || built > shape.construction {
		t.Errorf("audit construction allocated %d B; pre-logging estimate %d B, measured bound %d B", built, est, shape.construction)
	}
	for kind, f := range map[formatterKind]plugintypes.AuditLogFormatter{formatterNative: fw.audit.writer.formatter, formatterJSON: jsonFormatter(t)} {
		if f == nil || kind != classifyFormatter(f) {
			t.Fatalf("formatter %T not classified as %d", f, kind)
		}
		var out []byte
		used := allocated(func() { out, _ = f.Format(al) })
		if fe := formatEstimate(shape, kind); used > fe || uint64(len(out)) > fe {
			t.Errorf("formatter %d allocated %d B (output %d B); estimate %d B", kind, used, len(out), fe)
		}
	}
}

func jsonFormatter(t *testing.T) plugintypes.AuditLogFormatter {
	t.Helper()
	cfg := config.WAFConfig{Enabled: true, InlineRules: "SecAuditLog " + os.DevNull + "\nSecAuditLogFormat JSON"}
	applyTestDefaults(&cfg)
	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fw.Close() })
	return fw.audit.writer.formatter
}

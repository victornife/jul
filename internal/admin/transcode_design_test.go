package admin

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHandleTranscodeDescriptorUpload(t *testing.T) {
	// examples/grpc-gateway/api.pb is a real compiled FileDescriptorSet.
	pbPath := "../../examples/grpc-gateway/api.pb"
	pb, err := os.ReadFile(pbPath)
	if err != nil {
		t.Skipf("missing test fixture %s: %v", pbPath, err)
	}

	s := &Server{deps: Deps{Product: "Jul"}, health: &consoleHealth{}}

	buildRequest := func(body io.Reader, contentType string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/transcode/descriptor-upload", body)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return req
	}

	t.Run("valid descriptor", func(t *testing.T) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("descriptor", "api.pb")
		fw.Write(pb)
		mw.Close()

		rec := httptest.NewRecorder()
		s.handleTranscodeDescriptorUpload(rec, buildRequest(&b, mw.FormDataContentType()))
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !bytes.Contains([]byte(body), []byte(`"methods"`)) {
			t.Fatalf("expected methods array in response, got: %s", body)
		}
	})

	t.Run("missing field", func(t *testing.T) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		mw.WriteField("other", "value")
		mw.Close()

		rec := httptest.NewRecorder()
		s.handleTranscodeDescriptorUpload(rec, buildRequest(&b, mw.FormDataContentType()))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("invalid descriptor bytes", func(t *testing.T) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("descriptor", "bad.pb")
		fw.Write([]byte("not a descriptor"))
		mw.Close()

		rec := httptest.NewRecorder()
		s.handleTranscodeDescriptorUpload(rec, buildRequest(&b, mw.FormDataContentType()))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("no http annotations", func(t *testing.T) {
		// An empty FileDescriptorSet is valid protobuf but has no methods.
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("descriptor", "empty.pb")
		// A minimal FileDescriptorSet with one file but no services.
		fw.Write([]byte{
			0x0a, 0x00, // empty message (FileDescriptorSet with no files would be 0x0a 0x00?)
		})
		mw.Close()

		rec := httptest.NewRecorder()
		s.handleTranscodeDescriptorUpload(rec, buildRequest(&b, mw.FormDataContentType()))
		// empty descriptor set should fail with no methods found or invalid descriptor
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.handleTranscodeDescriptorUpload(rec, httptest.NewRequest(http.MethodGet, "/api/transcode/descriptor-upload", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("want 405, got %d", rec.Code)
		}
	})
}

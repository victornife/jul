// Package handler implements the content actions a location can dispatch to:
// static file serving, reverse proxying, FastCGI, and custom error pages.
package handler

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// ErrorPages renders custom error responses based on a status-code -> target
// mapping. A target is either a URL (http/https), which triggers a redirect, or
// a filesystem path whose contents are served with the error status. When no
// mapping exists (or the file cannot be read) a plain default page is used.
type ErrorPages struct {
	pages map[int]string
}

// NewErrorPages builds an ErrorPages from a config map keyed by status code
// strings (e.g. "404").
func NewErrorPages(m map[string]string) (*ErrorPages, error) {
	pages := make(map[int]string, len(m))
	for k, v := range m {
		code, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("error_pages: invalid status code %q", k)
		}
		pages[code] = v
	}
	return &ErrorPages{pages: pages}, nil
}

// Render writes an error response for code, using a custom page when configured.
func (e *ErrorPages) Render(w http.ResponseWriter, r *http.Request, code int) {
	if e != nil {
		if target, ok := e.pages[code]; ok {
			if isURL(target) {
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
			if body, err := os.ReadFile(target); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(code)
				_, _ = w.Write(body)
				return
			}
		}
	}
	http.Error(w, fmt.Sprintf("%d %s", code, http.StatusText(code)), code)
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

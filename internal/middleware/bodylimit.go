package middleware

import "net/http"

// BodyLimit returns middleware that caps the request body at maxBytes using
// http.MaxBytesReader, which makes reads fail and triggers a 413 once the limit
// is exceeded. A non-positive limit disables the cap.
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		if maxBytes <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

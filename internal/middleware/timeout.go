package middleware

import (
	"net/http"
	"time"
)

// Timeout returns middleware that enforces an overall handler timeout. When the
// deadline elapses the client receives 503 with a plain-text message. A
// non-positive duration disables the timeout.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.TimeoutHandler(next, d, "request timed out\n")
	}
}

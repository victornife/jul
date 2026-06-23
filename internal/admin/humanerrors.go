package admin

// validationErrorResponse is the v2 structured error shape for config
// validation failures. The UI renders these directly as human-readable
// guidance.
type validationErrorResponse struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	Errors  []validationError `json:"errors,omitempty"`
}

type validationError struct {
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	Severity string `json:"severity"` // error | warning
}

// humanizeErr maps a raw backend validation error into one or more structured
// validationError entries. The "code" field lets the UI theme the message
// (e.g. red for unknown_upstream, yellow for likely-misconfiguration).
//
// This is intentionally heuristic: the backend (config/validate.go) is the
// source of truth for correctness; this layer is for presentation only.
func humanizeErr(raw string) []validationError {
	// TODO: expand this table as new validation messages are added.
	// Each entry should produce a clear, actionable human sentence.
	mappings := []struct {
		token string
		err   validationError
	}{
		{
			token: "no upstream named",
			err: validationError{
				Code:     "unknown_upstream",
				Summary:  "Upstream reference points to a pool that does not exist.",
				Detail:   "Create the upstream in the config or choose an existing one.",
				Severity: "error",
			},
		},
		{
			token: "listen address",
			err: validationError{
				Code:     "bad_listen",
				Summary:  "A server listen address is malformed or conflicts with another.",
				Detail:   "Use host:port syntax. Check that no two server blocks bind to the same address.",
				Severity: "error",
			},
		},
		{
			token: "tls is enabled but",
			err: validationError{
				Code:     "tls_misconfig",
				Summary:  "TLS is enabled but required files or settings are missing.",
				Detail:   "Provide both cert and key, or enable ACME, and ensure the files exist.",
				Severity: "error",
			},
		},
		{
			token: "client_max_body_size",
			err: validationError{
				Code:     "bad_size",
				Summary:  "A byte-size value is malformed.",
				Detail:   "Use a number followed by b, k, kb, m, mb, g, or gb (e.g. 64m).",
				Severity: "error",
			},
		},
		{
			token: "duration",
			err: validationError{
				Code:     "bad_duration",
				Summary:  "A duration value is malformed.",
				Detail:   "Use Go duration syntax: 30s, 5m, 1h, 1h30m.",
				Severity: "error",
			},
		},
		{
			token: "rate limit rate",
			err: validationError{
				Code:     "bad_rate",
				Summary:  "Rate-limit parameters are invalid.",
				Detail:   "Rate must be > 0. Burst must be >= rate.",
				Severity: "error",
			},
		},
		{
			token: "acme account",
			err: validationError{
				Code:     "acme_misconfig",
				Summary:  "ACME settings are incomplete or invalid.",
				Detail:   "Check the email, provider URL, and accepted terms.",
				Severity: "error",
			},
		},
	}

	var out []validationError
	for _, m := range mappings {
		if containsFold(raw, m.token) {
			out = append(out, m.err)
		}
	}
	if len(out) == 0 {
		// Unknown error: preserve the raw text as a fallback so the UI still
		// shows *something* rather than swallowing it.
		out = append(out, validationError{
			Code:     "unknown",
			Summary:  raw,
			Severity: "error",
		})
	}
	return out
}

func containsFold(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexFold(s, substr) >= 0)
}

func indexFold(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			if lower(s[i+j]) != lower(substr[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

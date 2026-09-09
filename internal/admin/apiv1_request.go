// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"jul/internal/adminapi"
)

// v1MaxBodyBytes is ADR 0019 §24a's published cap for every body-bearing v1
// request. Keep the cap in this one shared layer so previews and mutations
// cannot drift from each other.
const v1MaxBodyBytes int64 = 1 << 20

var errV1TrailingJSON = errors.New("request body must contain exactly one JSON value")

// readV1Body validates Content-Type and reads at most the published body cap.
// The extra byte distinguishes an exact-boundary body from an over-boundary one
// without allocating an unbounded request.
func readV1Body(w http.ResponseWriter, r *http.Request, accepted ...string) ([]byte, *adminapi.Error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		return nil, unsupportedMediaType(accepted)
	}
	allowed := false
	for _, v := range accepted {
		if mediaType == v {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, unsupportedMediaType(accepted)
	}

	limited := http.MaxBytesReader(w, r.Body, v1MaxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, adminapi.Errorf(adminapi.CodePayloadTooLarge,
				"request body exceeds the published %d byte limit", v1MaxBodyBytes).
				WithDetails(adminapi.Details{Limit: int(v1MaxBodyBytes)})
		}
		return nil, adminapi.Errorf(adminapi.CodeInvalidRequest, "request body could not be read")
	}
	return body, nil
}

func unsupportedMediaType(accepted []string) *adminapi.Error {
	return adminapi.Errorf(adminapi.CodeUnsupportedMediaType,
		"Content-Type must be one of: %s", strings.Join(accepted, ", "))
}

func readV1TOML(w http.ResponseWriter, r *http.Request) ([]byte, *adminapi.Error) {
	return readV1Body(w, r, "application/toml", "text/plain")
}

func readV1JSON(w http.ResponseWriter, r *http.Request, dst any) ([]byte, *adminapi.Error) {
	body, apiErr := readV1Body(w, r, "application/json")
	if apiErr != nil {
		return nil, apiErr
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return nil, adminapi.Errorf(adminapi.CodeInvalidRequest, "invalid JSON request: %v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errV1TrailingJSON
		}
		return nil, adminapi.Errorf(adminapi.CodeInvalidRequest, "invalid JSON request: %v", err)
	}
	return body, nil
}

// restoreRequestBody lets a v1 adapter perform the common bounded/media-type
// admission once and then call the canonical internal operation with the exact
// same bytes. The canonical handler remains the sole owner of business logic.
func restoreRequestBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
}

func requiredBaseVersion(r *http.Request) (string, *adminapi.Error) {
	base := strings.TrimSpace(r.URL.Query().Get("base_version"))
	if base == "" {
		return "", adminapi.Errorf(adminapi.CodeInvalidRequest,
			"base_version is required for this mutation").WithDetails(adminapi.Details{Field: "base_version"})
	}
	return base, nil
}

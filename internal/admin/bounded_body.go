// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"io"
	"net/http"
)

// readBoundedBody checks the whole request before handing bytes to a decoder.
// A valid JSON prefix followed by data beyond the limit must not be accepted.
func readBoundedBody(r *http.Request, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("request body exceeds the size limit")
	}
	return data, nil
}

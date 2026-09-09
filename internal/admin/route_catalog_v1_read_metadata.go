// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "net/http"

// Close metadata that the original read-only generator could not express.
var v1ReadMetadataRegistered = func() bool {
	_ = v1CatalogNormalized
	for i := range Catalog {
		spec := &Catalog[i]
		switch spec.Pattern {
		case "/api/v1/config/history":
			op := spec.Operations[http.MethodGet]
			op.Parameters = []ExternalParameter{
				{
					Name: "limit", In: "query", Type: "integer", Default: 50,
					Description: "Requested page size. Omitted means 50. Syntactically valid integers below 1 are normalized to 1; values above 200 are normalized to 200. The response reports the effective limit and limit_clamped. No OpenAPI minimum/maximum is imposed because out-of-range integers are valid normalized input.",
				},
				{
					Name: "cursor", In: "query", Type: "string",
					Description: "Opaque continuation cursor returned by the previous page. Do not construct or inspect it.",
				},
			}
			spec.Operations[http.MethodGet] = op
		case "/api/v1/config/applies/{apply_id}":
			op := spec.Operations[http.MethodGet]
			op.AdditionalSuccessStatuses = []int{http.StatusAccepted}
			spec.Operations[http.MethodGet] = op
		}
	}
	return true
}()

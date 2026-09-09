// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/adminapi"
	"jul/internal/rbac"
)

var v1CatalogNormalized = func() bool {
	_ = v1WriteCatalogRegistered
	const pattern = "/api/v1/listeners/{addr}/client_address"
	first, second := -1, -1
	for i := range Catalog {
		if Catalog[i].Pattern != pattern {
			continue
		}
		if first < 0 {
			first = i
		} else {
			second = i
			break
		}
	}
	if first < 0 || second < 0 {
		return true
	}

	read := Catalog[first]
	write := Catalog[second]
	read.Methods = []string{http.MethodGet, http.MethodPatch}
	read.Permission = ""
	read.Permissions = map[string]rbac.Permission{
		http.MethodGet:   rbac.ConfigRead,
		http.MethodPatch: rbac.ConfigTrust,
	}
	if read.Operations == nil {
		read.Operations = map[string]ExternalOperation{}
	}
	read.Operations[http.MethodPatch] = write.Operations[http.MethodPatch]
	read.Handler = func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				s.handleV1ClientAddress(w, r)
			case http.MethodPatch:
				s.handleV1ClientAddressWrite(w, r)
			default:
				w.Header().Set("Allow", "GET, PATCH")
				writeAPIError(w, r, adminapiMethodError(r.Method, "GET, PATCH"))
			}
		})
	}
	Catalog[first] = read
	Catalog = append(Catalog[:second], Catalog[second+1:]...)
	return true
}()

func adminapiMethodError(got, allowed string) *adminapi.Error {
	return adminapi.Errorf(adminapi.CodeInvalidRequest, "this operation accepts %s, not %s", allowed, got)
}

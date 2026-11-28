// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"jul/internal/auth"
	"jul/internal/clientaddr"
	"jul/internal/middleware"
	"jul/internal/quota"
)

func quotaMiddleware(binding *quotaBinding, operation string, logger *slog.Logger, observe func(string, string, string)) middleware.Middleware {
	if binding == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			class, caller := binding.classify(r)
			coordinator := binding.state.enforce
			if binding.mode == "observe" {
				coordinator = binding.state.observe
			}
			decision, err := coordinator.AdmitOperationForAuthorizationVersion(class, operation, caller, binding.version.Load())
			if err != nil {
				if logger != nil {
					logger.Error("quota admission failed", "error", err)
				}
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			if decision.Allowed {
				binding.state.decisions.record(binding.mode, quotaAdmitted)
				if observe != nil {
					observe(binding.name, binding.mode, quotaAdmitted)
				}
				next.ServeHTTP(w, r)
				return
			}
			reason := string(decision.Reason)
			binding.state.decisions.record(binding.mode, reason)
			if observe != nil {
				observe(binding.name, binding.mode, reason)
			}
			switch decision.Reason {
			case quota.ReasonStaleAuthorization:
				w.Header().Set("Retry-After", "1")
				http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
			default:
				if binding.mode == "observe" {
					next.ServeHTTP(w, r)
					return
				}
				if decision.RetryAfter > 0 {
					seconds := int((decision.RetryAfter + time.Second - 1) / time.Second)
					if seconds < 1 {
						seconds = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(seconds))
				}
				http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			}
		})
	}
}

func (b *quotaBinding) classify(r *http.Request) (string, string) {
	if principal, ok := auth.PrincipalFromContext(r.Context()); ok {
		key := quotaPrincipalKey{
			method:  principal.Method,
			domain:  principal.Domain,
			issuer:  principal.Issuer,
			subject: principal.Subject,
		}
		if class, authorized := b.principals[key]; authorized {
			caller := fmt.Sprintf("principal:%s\x00%s\x00%s\x00%s", key.method, key.domain, key.issuer, key.subject)
			return class, caller
		}
	}

	if id, ok := clientaddr.FromContext(r.Context()); ok {
		if id.Attributed() && id.Client.IsValid() {
			if class, matched := b.classForAddress(id.Client); matched {
				return class, "client:" + id.Client.String()
			}
			return "public", "client:" + id.Client.String()
		}
		if id.Peer.IsValid() {
			return "public", "unattributed:" + id.Peer.String()
		}
		return "public", "unattributed:" + r.RemoteAddr
	}

	if peer := clientaddr.PeerFromRemoteAddr(r.RemoteAddr); peer.IsValid() {
		return "public", "unattributed:" + peer.String()
	}
	return "public", "unknown:" + r.RemoteAddr
}

func (b *quotaBinding) classForAddress(address netip.Addr) (string, bool) {
	for _, pool := range b.pools {
		if pool.prefix.Contains(address) {
			return pool.class, true
		}
	}
	return "", false
}

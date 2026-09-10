// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/rbac"
)

// adminLimitPolicy is immutable request-generation policy. It is always derived
// from the AdminConfig carried by the request's captured #157 admin snapshot;
// mutable abuse/accounting state deliberately lives in adminLimiter instead.
type adminLimitPolicy struct {
	readPerMin  int
	writePerMin int
	applyPerMin int
	maxConns    int
}

func adminLimitPolicyFromConfig(cfg config.AdminConfig) adminLimitPolicy {
	return adminLimitPolicy{
		readPerMin:  cfg.RateLimitReadPerMin,
		writePerMin: cfg.RateLimitWritePerMin,
		applyPerMin: cfg.RateLimitApplyPerMin,
		maxConns:    cfg.MaxEventConns,
	}
}

func (p adminLimitPolicy) perMinute(kind limitKind) int {
	switch kind {
	case limitWrite:
		return p.writePerMin
	case limitApply:
		return p.applyPerMin
	default:
		return p.readPerMin
	}
}

// adminLimiter is the process-lifetime mutable admission-state manager. Its
// identity does not change when admin policy reloads: per-client token history,
// SSE leases and idle bookkeeping therefore survive a snapshot publication.
type adminLimiter struct {
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	buckets  map[string]*adminClient
	lastSeen map[string]time.Time
	nextGC   time.Time

	lastRejectLog [3]time.Time
	rejections    [3]uint64
	sseRejected   uint64
}

// adminRateBucket keeps the limiter object even while its class is disabled.
// That matters for finite -> disabled -> finite: reload is not quota forgiveness.
type adminRateBucket struct {
	limiter       *rate.Limiter
	appliedPerMin int
	initialized   bool
}

// adminClient holds one transport peer's independent request buckets and live
// SSE lease count.
type adminClient struct {
	read  adminRateBucket
	write adminRateBucket
	apply adminRateBucket
	conns int
}

// newAdminLimiter always returns one stable manager. The variadic compatibility
// argument intentionally carries no policy authority; production policy is read
// only from the immutable request snapshot. Keeping the shape avoids forcing
// callers/tests compiled against the old constructor through a transition shim.
func newAdminLimiter(log *slog.Logger, _ ...int) *adminLimiter {
	if log == nil {
		log = slog.Default()
	}
	return &adminLimiter{
		log:      log,
		now:      time.Now,
		buckets:  make(map[string]*adminClient),
		lastSeen: make(map[string]time.Time),
	}
}

// limitKind is a closed, bounded request admission class.
type limitKind uint8

const (
	limitRead limitKind = iota
	limitWrite
	limitApply
)

func (k limitKind) String() string {
	switch k {
	case limitWrite:
		return "write"
	case limitApply:
		return "apply"
	default:
		return "read"
	}
}

func finiteLimiter(perMinute int) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(float64(perMinute)/60.0), perMinute)
}

func (c *adminClient) bucket(kind limitKind) *adminRateBucket {
	switch kind {
	case limitWrite:
		return &c.write
	case limitApply:
		return &c.apply
	default:
		return &c.read
	}
}

func (l *adminLimiter) clientLocked(ip string, now time.Time) *adminClient {
	c := l.buckets[ip]
	if c == nil {
		c = &adminClient{}
		l.buckets[ip] = c
	}
	l.lastSeen[ip] = now
	return c
}

// reserveLocked performs policy retune and admission as one synchronized
// transaction. x/time/rate's SetLimitAt/SetBurstAt update the bucket parameters;
// ReserveN at the same timestamp advances using the new burst, which clamps any
// previously accumulated excess before the first new-policy admission.
func (b *adminRateBucket) reserveLocked(now time.Time, perMinute int) (bool, int) {
	if perMinute <= 0 {
		// Disabled request classes bypass admission but retain any finite limiter
		// and its timeline for a later re-enable.
		b.appliedPerMin = perMinute
		return true, 0
	}

	if !b.initialized || b.limiter == nil {
		b.limiter = finiteLimiter(perMinute)
		b.appliedPerMin = perMinute
		b.initialized = true
	} else if b.appliedPerMin != perMinute {
		b.limiter.SetLimitAt(now, rate.Limit(float64(perMinute)/60.0))
		b.limiter.SetBurstAt(now, perMinute)
		b.appliedPerMin = perMinute
	}

	reservation := b.limiter.ReserveN(now, 1)
	if !reservation.OK() {
		return false, 1
	}
	if delay := reservation.DelayFrom(now); delay > 0 {
		reservation.CancelAt(now)
		secs := int((delay + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		return false, secs
	}
	return true, 0
}

// allow admits one request under the policy captured with that request. One
// timestamp covers lazy retune, reservation, cancellation and Retry-After.
func (l *adminLimiter) allow(ip string, kind limitKind, policy adminLimitPolicy) (bool, int) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(now)

	c := l.clientLocked(ip, now)
	ok, retryAfter := c.bucket(kind).reserveLocked(now, policy.perMinute(kind))
	if !ok {
		l.rejections[int(kind)]++
	}
	return ok, retryAfter
}

// acquireConn registers an SSE lease under the cap from the same captured admin
// generation as the request. Lowering the cap never revokes existing leases;
// it only blocks new admissions until the peer drops below the new cap.
func (l *adminLimiter) acquireConn(ip string, policy adminLimitPolicy) (release func(), ok bool) {
	now := l.now()
	l.mu.Lock()
	l.gcLocked(now)
	c := l.clientLocked(ip, now)
	if policy.maxConns > 0 && c.conns >= policy.maxConns {
		l.sseRejected++
		l.mu.Unlock()
		return nil, false
	}
	c.conns++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			now := l.now()
			l.mu.Lock()
			defer l.mu.Unlock()
			if cc := l.buckets[ip]; cc != nil {
				if cc.conns > 0 {
					cc.conns--
				}
				l.lastSeen[ip] = now
			}
		})
	}, true
}

type adminLimiterStats struct {
	TrackedClients    int
	SSEActiveTotal    int
	SSEActiveClients  int
	SSEOverCapClients int
	SSEMaxPerClient   int
	SSERejected       uint64
	ReadRejected      uint64
	WriteRejected     uint64
	ApplyRejected     uint64
}

// stats samples mutable process-lifetime accounting against one captured policy.
// It deliberately exposes no transport peer identities.
func (l *adminLimiter) stats(policy adminLimitPolicy) adminLimiterStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := adminLimiterStats{
		TrackedClients: len(l.buckets),
		SSERejected:    l.sseRejected,
		ReadRejected:   l.rejections[int(limitRead)],
		WriteRejected:  l.rejections[int(limitWrite)],
		ApplyRejected:  l.rejections[int(limitApply)],
	}
	for _, c := range l.buckets {
		if c.conns <= 0 {
			continue
		}
		out.SSEActiveClients++
		out.SSEActiveTotal += c.conns
		if c.conns > out.SSEMaxPerClient {
			out.SSEMaxPerClient = c.conns
		}
		if policy.maxConns > 0 && c.conns > policy.maxConns {
			out.SSEOverCapClients++
		}
	}
	return out
}

func (l *adminLimiter) eventConnCount() int {
	return l.stats(adminLimitPolicy{}).SSEActiveTotal
}

// gcLocked amortizes the historical O(number of clients) scan. Active SSE
// peers are retained regardless of request-idle age.
func (l *adminLimiter) gcLocked(now time.Time) {
	const (
		idle       = 15 * time.Minute
		gcInterval = time.Minute
	)
	if !l.nextGC.IsZero() && now.Before(l.nextGC) {
		return
	}
	l.nextGC = now.Add(gcInterval)
	cutoff := now.Add(-idle)
	for ip, seen := range l.lastSeen {
		if seen.Before(cutoff) {
			if c := l.buckets[ip]; c == nil || c.conns == 0 {
				delete(l.buckets, ip)
				delete(l.lastSeen, ip)
			}
		}
	}
}

func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func permissionForMethod(spec RouteSpec, method string) rbac.Permission {
	if spec.Permissions != nil {
		return spec.Permissions[method]
	}
	return spec.Permission
}

func hasPermission(perms []rbac.Permission, target rbac.Permission) bool {
	for _, p := range perms {
		if p == target {
			return true
		}
	}
	return false
}

// limitClassForSpec derives the admission decision from the authoritative route
// catalogue rather than a parallel path switch. Safe methods are reads. Config
// assessment/mutation permissions use the stricter apply budget; other
// mutations use write. Unknown methods are conservatively write.
func limitClassForSpec(spec RouteSpec, method string) limitKind {
	if explicit, ok := spec.LimitClasses[method]; ok {
		return explicit
	}
	if safeMethod(method) {
		return limitRead
	}

	perm := permissionForMethod(spec, method)
	if perm == rbac.ConfigApply || perm == rbac.HistoryRollback || perm == rbac.ConfigAdopt || hasPermission(spec.AnyPermissions, rbac.ConfigApply) || hasPermission(spec.AnyPermissions, rbac.HistoryRollback) || hasPermission(spec.AnyPermissions, rbac.ConfigAdopt) {
		return limitApply
	}
	if op, ok := spec.Operations[method]; ok {
		switch op.ID {
		case "validateConfig", "planConfig", "previewConfigPatch", "applyConfig", "applyConfigPatch", "rollbackConfig", "previewAdoptExternal", "adoptExternal", "discardPendingRestart":
			return limitApply
		}
	}
	return limitWrite
}

func (l *adminLimiter) maybeLogRejection(kind limitKind, retryAfter int) {
	now := l.now()
	l.mu.Lock()
	last := l.lastRejectLog[int(kind)]
	if !last.IsZero() && now.Sub(last) < time.Second {
		l.mu.Unlock()
		return
	}
	l.lastRejectLog[int(kind)] = now
	l.mu.Unlock()
	l.log.Warn("admin rate limit exceeded", "class", kind.String(), "retry_after_s", retryAfter)
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, retryAfter int) {
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	if _, external := externalContract(r.Context()); external {
		v := retryAfter
		writeAPIError(w, r, adminapi.New(adminapi.CodeRateLimited).WithDetails(adminapi.Details{RetryAfterSeconds: &v}))
		return
	}
	http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
}

// limitRoute is installed exactly once while the stable mux is built. Policy is
// loaded only through requestAdminSnapshot, which reuses the snapshot captured
// at outer mux entry. It therefore cannot observe a different reload generation
// from auth/Console/upload policy.
func (s *Server) limitRoute(spec RouteSpec, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := adminLimitPolicyFromConfig(s.requestAdminSnapshot(r).cfg)
		kind := limitClassForSpec(spec, r.Method)
		ok, retryAfter := s.limiter.allow(adminClientIP(r), kind, policy)
		if !ok {
			s.limiter.maybeLogRejection(kind, retryAfter)
			writeRateLimited(w, r, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminClientIP extracts the transport peer IP and deliberately ignores
// untrusted forwarding headers so callers cannot choose their own bucket key.
func adminClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

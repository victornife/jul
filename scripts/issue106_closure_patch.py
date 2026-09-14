#!/usr/bin/env python3
from pathlib import Path


def read(path):
    return Path(path).read_text()


def write(path, text):
    Path(path).write_text(text)


def replace_once(path, old, new):
    text = read(path)
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one match, found {count}: {old[:120]!r}")
    write(path, text.replace(old, new, 1))

# ---------------------------------------------------------------------------
# rate_limit.max_conns: stable listener-owned dynamic admission limiter.
# ---------------------------------------------------------------------------
write("internal/server/dynamic_conn_limit.go", r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"net"
	"sync"
)

// dynamicConnLimiter is a stable listener-lifetime admission wrapper whose
// cap can change without rebinding the socket. The limit applies when Accept
// returns a connection to net/http: connections already admitted remain alive
// when the cap is lowered, while a pending accept waits until the active count
// falls below the current cap. A limit of zero is unlimited.
//
// The underlying socket may have accepted at most one connection that is
// waiting for admission in net/http's single accept loop. That connection has
// not entered TLS or HTTP processing yet and is closed if the listener shuts
// down before admission.
type dynamicConnLimiter struct {
	net.Listener

	mu     sync.Mutex
	cond   *sync.Cond
	limit  int
	active int
	closed bool

	closeOnce sync.Once
	closeErr  error
}

func newDynamicConnLimiter(ln net.Listener, limit int) *dynamicConnLimiter {
	if limit < 0 {
		limit = 0
	}
	l := &dynamicConnLimiter{Listener: ln, limit: limit}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// SetLimit publishes the cap for subsequent admissions. Lowering the cap never
// closes an admitted connection; raising it (or setting zero/unlimited) wakes a
// waiter immediately.
func (l *dynamicConnLimiter) SetLimit(limit int) {
	if l == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	l.mu.Lock()
	l.limit = limit
	l.cond.Broadcast()
	l.mu.Unlock()
}

func (l *dynamicConnLimiter) Limit() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

func (l *dynamicConnLimiter) Active() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

func (l *dynamicConnLimiter) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	for !l.closed && l.limit > 0 && l.active >= l.limit {
		l.cond.Wait()
	}
	if l.closed {
		l.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	l.active++
	l.mu.Unlock()

	return &countedConn{Conn: conn, release: l.release}, nil
}

func (l *dynamicConnLimiter) release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.cond.Broadcast()
	l.mu.Unlock()
}

func (l *dynamicConnLimiter) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.cond.Broadcast()
		l.mu.Unlock()
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
	return err
}

// effectiveConnectionCap preserves the existing master-switch semantics while
// making them live: disabling rate_limit makes the listener cap unlimited;
// enabling it activates max_conns immediately. max_conns == 0 is unlimited.
func effectiveConnectionCap(cfg *config.Config) int {
	if cfg == nil || !cfg.RateLimit.Enabled || cfg.RateLimit.MaxConns <= 0 {
		return 0
	}
	return cfg.RateLimit.MaxConns
}

// updateConnectionLimits is a no-fail Publish operation. Retained listeners are
// updated before the candidate config/runtime snapshot is published; newly
// staged listeners were already built with the candidate cap.
func (s *Server) updateConnectionLimits(cfg *config.Config) {
	limit := effectiveConnectionCap(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.listeners {
		if entry != nil && entry.connLimiter != nil {
			entry.connLimiter.SetLimit(limit)
		}
	}
}
''')
# Add config import to the new file.
replace_once("internal/server/dynamic_conn_limit.go", '"sync"\n)', '"sync"\n\n\t"jul/internal/config"\n)')

replace_once("internal/server/server.go", '\n\t"golang.org/x/net/netutil"\n', '\n')
replace_once(
    "internal/server/server.go",
    'type listenerEntry struct {\n\taddr             string\n\thttpd            *http.Server\n\tln               net.Listener\n',
    'type listenerEntry struct {\n\taddr             string\n\thttpd            *http.Server\n\tln               net.Listener\n\tconnLimiter      *dynamicConnLimiter // stable listener-lifetime admission cap\n',
)
replace_once(
    "internal/server/server.go",
    '''\t// Cap concurrent connections per listener before the optional TLS wrap so
\t// the limit counts raw accepts and TLS handshakes happen only for admitted
\t// connections. Gated by the [rate_limit] master switch; the cap is fixed at
\t// bind time, so changing max_conns applies to newly bound listeners.
\tif rl := cfg.RateLimit; rl.Enabled && rl.MaxConns > 0 {
\t\tln = netutil.LimitListener(ln, rl.MaxConns)
\t}
''',
    '''\t// Keep one Jul-owned admission wrapper for the listener lifetime. The
\t// effective cap is published in place on reload, before TLS, so TLS/HTTP work
\t// begins only after admission and no socket rebind is required (#106).
\tconnLimiter := newDynamicConnLimiter(ln, effectiveConnectionCap(cfg))
\tln = connLimiter
''',
)
replace_once(
    "internal/server/server.go",
    '\tentry := &listenerEntry{addr: addr}\n',
    '\tentry := &listenerEntry{addr: addr, connLimiter: connLimiter}\n',
)
replace_once(
    "internal/server/server.go",
    '"listener %s has bind-time settings (timeouts, header limits, h2c, HTTP/3, TLS, mutual TLS, or connection cap) that changed; these are fixed when the listener binds and take effect on restart",',
    '"listener %s has bind-time settings (timeouts, header limits, h2c, HTTP/3, TLS, or mutual TLS) that changed; these are fixed when the listener binds and take effect on restart",',
)

replace_once(
    "internal/server/listener_fingerprint.go",
    '''// CRL file), and the per-listener connection cap (rate_limit.max_conns). Only
''',
    '''// CRL file). The connection cap is deliberately excluded: #106 owns it in a
// stable dynamic admission wrapper. Only
''',
)
replace_once(
    "internal/server/listener_fingerprint.go",
    '"listener %s has bind-time settings (timeouts, header limits, h2c, HTTP/3, TLS, mutual TLS, or connection cap) that changed; these are fixed when the listener binds and take effect on restart",',
    '"listener %s has bind-time settings (timeouts, header limits, h2c, HTTP/3, TLS, or mutual TLS) that changed; these are fixed when the listener binds and take effect on restart",',
)
replace_once(
    "internal/server/listener_fingerprint.go",
    '''\t// The connection cap is global (rate_limit.max_conns) yet applied to every
\t// listener at bind time, so a change forces a rebind of each kept listener.
\tmaxConns := 0
\tif rl := cfg.RateLimit; rl.Enabled && rl.MaxConns > 0 {
\t\tmaxConns = rl.MaxConns
\t}
\tfmt.Fprintf(&b, "maxconns=%d;", maxConns)

''',
    '''\t// rate_limit.max_conns is intentionally absent: a stable admission
\t// wrapper applies the current cap to retained listeners at Publish (#106).

''',
)

replace_once(
    "internal/server/reload_plan.go",
    '''\t// Alt-Svc max-age hot reload (#161): unlike certificate rotation, building
''',
    '''\t// #106: publish the effective concurrent-connection admission cap in
\t// place on every retained listener. This is no-fail and happens before the
\t// candidate config/runtime snapshot becomes visible; staged listeners were
\t// already built with the candidate cap.
\tp.s.updateConnectionLimits(p.Candidate.Effective)

\t// Optional final-tranche ACME policy: the process-lifetime ACME manager owns
\t// a stable OCSP wrapper whose atomic policy is safe to switch at Publish.
\tp.s.updateACMEOCSPPolicy(p.Candidate.Effective.Servers)

\t// Alt-Svc max-age hot reload (#161): unlike certificate rotation, building
''',
)

# ---------------------------------------------------------------------------
# admin.history_keep: atomic policy on the existing history backend.
# ---------------------------------------------------------------------------
replace_once(
    "internal/admin/history.go",
    'import (\n\t"encoding/json"',
    'import (\n\t"encoding/json"\n\t"errors"',
)
replace_once(
    "internal/admin/history.go",
    '\t"strings"\n\t"time"',
    '\t"strings"\n\t"sync"\n\t"sync/atomic"\n\t"time"',
)
replace_once(
    "internal/admin/history.go",
    '''type history struct {
\tdir  string
\tkeep int
}

// newHistory builds a history rooted at dir, retaining at most keep snapshots.
// A blank dir disables snapshotting (all methods become no-ops returning empty
// results), which keeps callers branch-free.
func newHistory(dir string, keep int) *history {
\treturn &history{dir: strings.TrimSpace(dir), keep: keep}
}
''',
    '''type history struct {
\tdir string

\t// keep is policy, not backend identity. It changes atomically at Publish
\t// while dir remains immutable for the process lifetime (#106/#159).
\tkeep atomic.Int64

\t// pruneMu serializes destructive retention passes. Listing/reading remain
\t// concurrent and tolerate a snapshot disappearing between directory read
\t// and stat, as before.
\tpruneMu sync.Mutex
\tremove  func(string) error // test seam; nil means os.Remove
}

// newHistory builds a history rooted at dir, retaining at most keep snapshots.
// A blank dir disables snapshotting (all methods become no-ops returning empty
// results), which keeps callers branch-free.
func newHistory(dir string, keep int) *history {
\th := &history{dir: strings.TrimSpace(dir)}
\th.keep.Store(int64(keep))
\treturn h
}

func (h *history) retention() int {
\tif h == nil {
\t\treturn 0
\t}
\treturn int(h.keep.Load())
}

// setRetention publishes keep and reports whether the new value requires a
// post-Publish prune. Moving from unlimited (<=0) to a finite bound is a
// tightening; raising a finite bound or moving to unlimited deletes nothing.
func (h *history) setRetention(keep int) (needsPrune bool) {
\tif h == nil {
\t\treturn false
\t}
\told := int(h.keep.Swap(int64(keep)))
\treturn keep > 0 && (old <= 0 || keep < old)
}
''',
)
replace_once(
    "internal/admin/history.go",
    '''// prune deletes the oldest snapshots beyond the retention bound. A keep of zero
// or less is treated as "no pruning" so an unbounded history is still possible.
func (h *history) prune() {
\tif h.keep <= 0 {
\t\treturn
\t}
\tnames, err := h.snapshotFiles()
\tif err != nil {
\t\treturn
\t}
\tfor _, name := range names[min(len(names), h.keep):] {
\t\t_ = os.Remove(filepath.Join(h.dir, name))
\t\t// AC-05: remove the metadata sidecar alongside the raw snapshot so the
\t\t// two never drift. Absent sidecars (older snapshots) are ignored.
\t\tid := strings.TrimSuffix(name, historyExt)
\t\t_ = os.Remove(filepath.Join(h.dir, id+historyMetaExt))
\t}
}
''',
    '''// prune deletes the oldest snapshots beyond the current retention bound.
// Snapshot writes retain the historical best-effort behavior; explicit
// post-Publish retention tightening uses pruneCurrent so failures can be
// surfaced as advisory/degraded state without rolling back the applied config.
func (h *history) prune() { _ = h.pruneCurrent() }

func (h *history) pruneCurrent() error {
\tif h == nil {
\t\treturn nil
\t}
\tkeep := h.retention()
\tif keep <= 0 {
\t\treturn nil
\t}
\th.pruneMu.Lock()
\tdefer h.pruneMu.Unlock()

\tnames, err := h.snapshotFiles()
\tif err != nil {
\t\treturn err
\t}
\tvar errs []error
\tfor _, name := range names[min(len(names), keep):] {
\t\t// AC-05: remove the metadata sidecar alongside the raw snapshot. Remove
\t\t// the sidecar first so a partial failure can at worst leave a raw-only
\t\t// snapshot, which is an explicitly supported backward-compatible form.
\t\tid := strings.TrimSuffix(name, historyExt)
\t\tif err := h.removeFile(filepath.Join(h.dir, id+historyMetaExt)); err != nil && !errors.Is(err, os.ErrNotExist) {
\t\t\terrs = append(errs, fmt.Errorf("remove history metadata: %w", err))
\t\t}
\t\tif err := h.removeFile(filepath.Join(h.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
\t\t\terrs = append(errs, fmt.Errorf("remove history snapshot: %w", err))
\t\t}
\t}
\treturn errors.Join(errs...)
}

func (h *history) removeFile(path string) error {
\tif h.remove != nil {
\t\treturn h.remove(path)
\t}
\treturn os.Remove(path)
}
''',
)

replace_once(
    "internal/admin/rbac.go",
    '''type PreparedAuth struct {
\tsnapshot *authSnapshot
\taudit    *preparedAuditSink
}
''',
    '''type PreparedAuth struct {
\tsnapshot *authSnapshot
\taudit    *preparedAuditSink

\t// history_keep is staged without touching the live backend. Commit swaps
\t// the scalar policy before publishing snapshot; Retire performs any
\t// tightening prune after Publish and reports failures as advisory only.
\thistoryKeep      int
\thistoryKeepSet   bool
\thistoryNeedsPrune bool
}
''',
)

replace_once(
    "internal/admin/runtime_snapshot.go",
    '''\tresult := &PreparedAuth{snapshot: s.completeAdminRuntimeSnapshot(&out)}
\tif err := s.prepareAuditRuntime(cfg, result); err != nil {
\t\treturn nil, err
\t}
\ts.clearAdminPrepareFailure()
''',
    '''\tresult := &PreparedAuth{snapshot: s.completeAdminRuntimeSnapshot(&out)}
\tif s.hist != nil {
\t\t// Stage only the scalar policy. No prune, directory creation or other
\t\t// history side effect is permitted before Publish (#106/#159).
\t\tresult.historyKeep = cfg.HistoryKeep
\t\tresult.historyKeepSet = true
\t}
\tif err := s.prepareAuditRuntime(cfg, result); err != nil {
\t\treturn nil, err
\t}
\ts.clearAdminPrepareFailure()
''',
)

replace_once(
    "internal/admin/audit_admin_runtime.go",
    '''func (s *Server) CommitPreparedAdminRuntime(prepared *PreparedAuth) {
\tif prepared == nil {
\t\treturn
\t}
\tif prepared.audit == nil {
\t\ts.CommitPreparedAuth(prepared)
\t\treturn
\t}
\tprepared.audit.commitWith(func() { s.CommitPreparedAuth(prepared) })
}
''',
    '''func (s *Server) CommitPreparedAdminRuntime(prepared *PreparedAuth) {
\tif prepared == nil {
\t\treturn
\t}
\tcommitRuntime := func() {
\t\t// Publish history retention before the immutable admin snapshot that
\t\t// advertises it. This operation cannot fail and performs no deletion.
\t\tif prepared.historyKeepSet && s.hist != nil {
\t\t\tprepared.historyNeedsPrune = s.hist.setRetention(prepared.historyKeep)
\t\t}
\t\ts.CommitPreparedAuth(prepared)
\t}
\tif prepared.audit == nil {
\t\tcommitRuntime()
\t\treturn
\t}
\tprepared.audit.commitWith(commitRuntime)
}
''',
)
replace_once(
    "internal/admin/audit_admin_runtime.go",
    '''func (s *Server) RetirePreparedAdminRuntime(ctx context.Context, prepared *PreparedAuth) {
\tif prepared == nil || prepared.audit == nil {
\t\treturn
\t}
\tprepared.audit.retire(ctx)
}
''',
    '''func (s *Server) RetirePreparedAdminRuntime(ctx context.Context, prepared *PreparedAuth) {
\tif prepared == nil {
\t\treturn
\t}
\tif prepared.historyNeedsPrune && s.hist != nil {
\t\tif err := s.hist.pruneCurrent(); err != nil {
\t\t\ts.recordHistoryRetentionFailure()
\t\t\tif s.log != nil {
\t\t\t\ts.log.Warn("configuration history retention prune degraded after publish", "error", err)
\t\t\t}
\t\t} else {
\t\t\ts.clearHistoryRetentionFailure()
\t\t}
\t}
\tif prepared.audit != nil {
\t\tprepared.audit.retire(ctx)
\t}
}
''',
)

replace_once(
    "internal/admin/server.go",
    '''\tadminPrepareFailure   atomic.Pointer[string]
\tpluginUploadRejection atomic.Pointer[string]
''',
    '''\tadminPrepareFailure    atomic.Pointer[string]
\tpluginUploadRejection  atomic.Pointer[string]
\thistoryRetentionStatus atomic.Pointer[string] // nil healthy; otherwise bounded advisory code
''',
)

replace_once(
    "internal/admin/admin_runtime_status.go",
    '''\tLastUploadRejection   string `json:"last_upload_rejection,omitempty"`

\tRateLimitReadPerMin''',
    '''\tLastUploadRejection   string `json:"last_upload_rejection,omitempty"`
\tHistoryKeep           int    `json:"history_keep"`
\tHistoryRetentionHealth string `json:"history_retention_health"`

\tRateLimitReadPerMin''',
)
replace_once(
    "internal/admin/admin_runtime_status.go",
    '''\tAuditLogRotateKeep    int                                 `json:"audit_log_rotate_keep"`
\tAuditSink             *AuditSinkStatus''',
    '''\tAuditLogRotateKeep    int                                 `json:"audit_log_rotate_keep"`
\tHistoryKeep           int                                 `json:"history_keep"`
\tAuditSink             *AuditSinkStatus''',
)
replace_once(
    "internal/admin/admin_runtime_status.go",
    '''\tpolicy := adminLimitPolicyFromConfig(snap.cfg)
\tstats := s.limiter.stats(policy)
\tout := &AdminRuntimeStatus{''',
    '''\tpolicy := adminLimitPolicyFromConfig(snap.cfg)
\tstats := s.limiter.stats(policy)
\thistoryHealth := "ok"
\tif s.hist == nil || !s.hist.enabled() {
\t\thistoryHealth = "disabled"
\t} else if p := s.historyRetentionStatus.Load(); p != nil {
\t\thistoryHealth = *p
\t}
\tout := &AdminRuntimeStatus{''',
)
replace_once(
    "internal/admin/admin_runtime_status.go",
    '''\t\tUploadDirectoryHealth: health,
\t\tRateLimitReadPerMin:   policy.readPerMin,''',
    '''\t\tUploadDirectoryHealth: health,
\t\tHistoryKeep:           snap.cfg.HistoryKeep,
\t\tHistoryRetentionHealth: historyHealth,
\t\tRateLimitReadPerMin:   policy.readPerMin,''',
)
replace_once(
    "internal/admin/admin_runtime_status.go",
    '''\t\tAuditLogRotateKeep:    cfg.AuditLogRotateKeep,
\t\tAuditSink:             s.audit.statusReport(),''',
    '''\t\tAuditLogRotateKeep:    cfg.AuditLogRotateKeep,
\t\tHistoryKeep:           cfg.HistoryKeep,
\t\tAuditSink:             s.audit.statusReport(),''',
)
replace_once(
    "internal/admin/admin_runtime_status.go",
    '''\t\t\t"audit_log_rotate_keep":    lifecycleFieldProjection("admin.audit_log_rotate_keep"),
''',
    '''\t\t\t"audit_log_rotate_keep":    lifecycleFieldProjection("admin.audit_log_rotate_keep"),
\t\t\t"history_keep":             lifecycleFieldProjection("admin.history_keep"),
''',
)
replace_once(
    "internal/admin/admin_runtime_status.go",
    '''func (s *Server) recordPluginUploadRejection(reason string) {
\tv := reason
\ts.pluginUploadRejection.Store(&v)
}
''',
    '''func (s *Server) recordPluginUploadRejection(reason string) {
\tv := reason
\ts.pluginUploadRejection.Store(&v)
}

func (s *Server) recordHistoryRetentionFailure() {
\tv := "prune_failed"
\ts.historyRetentionStatus.Store(&v)
}

func (s *Server) clearHistoryRetentionFailure() { s.historyRetentionStatus.Store(nil) }
''',
)

# ---------------------------------------------------------------------------
# OCSP stapling: stable provider wrapper + atomic policy on the existing manager.
# ---------------------------------------------------------------------------
replace_once(
    "internal/server/acme.go",
    '\t"net/http"\n\t"time"',
    '\t"net/http"\n\t"sync/atomic"\n\t"time"',
)
replace_once(
    "internal/server/acme.go",
    '''\tocsp       bool         // staple OCSP responses onto issued certificates
\tocspClient *http.Client // guarded OCSP responder client; nil = default
''',
    '''\tocsp       atomic.Bool  // live policy; providers keep one stable wrapper
\tocspClient *http.Client // guarded OCSP responder client; nil = default
''',
)
replace_once(
    "internal/server/acme.go",
    '''\treturn &acmeManager{
\t\tmgr:        m,
\t\tchallenge:  challenge,
\t\tonIssue:    onIssue,
\t\tocsp:       ocsp,
\t\tocspClient: ocspClient,
\t}, nil
''',
    '''\tmanager := &acmeManager{
\t\tmgr:        m,
\t\tchallenge:  challenge,
\t\tonIssue:    onIssue,
\t\tocspClient: ocspClient,
\t}
\tmanager.ocsp.Store(ocsp)
\treturn manager, nil
''',
)
replace_once(
    "internal/server/acme.go",
    '''func (a *acmeManager) Provider(domains []string) CertProvider {
\tbase := &acmeProvider{mgr: a.mgr, onIssue: a.onIssue}
\tif !a.ocsp {
\t\treturn base
\t}
\treturn newOCSPStapler(base, a.ocspClient)
}
''',
    '''func (a *acmeManager) Provider(domains []string) CertProvider {
\tbase := &acmeProvider{mgr: a.mgr, onIssue: a.onIssue}
\treturn &dynamicOCSPProvider{
\t\tenabled: &a.ocsp,
\t\tbase:    base,
\t\tstapler: newOCSPStapler(base, a.ocspClient),
\t}
}

// SetOCSPStapling is a no-fail policy-only update used at reload Publish. It
// does not replace the autocert manager, account/cache identity, HostPolicy,
// challenge mode, certificate provider, or listener TLS configuration.
func (a *acmeManager) SetOCSPStapling(enabled bool) { a.ocsp.Store(enabled) }

// dynamicOCSPProvider keeps one stapler/cache for the provider lifetime while
// selecting it only when the current policy enables stapling. Disabling stops
// new refresh initiation immediately; work already started by the stapler may
// finish. Re-enabling can reuse a still-valid cached response.
type dynamicOCSPProvider struct {
\tenabled *atomic.Bool
\tbase    CertProvider
\tstapler *ocspStapler
}

func (p *dynamicOCSPProvider) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
\tif p.enabled == nil || !p.enabled.Load() {
\t\treturn p.base.GetCertificate(hello)
\t}
\treturn p.stapler.GetCertificate(hello)
}
''',
)

replace_once(
    "internal/server/tls.go",
    '''// certProviderFor selects the certificate provider for a TLS listen address.
''',
    '''type acmeOCSPPolicy interface {
\tSetOCSPStapling(bool)
}

func acmeOCSPStaplingForServers(servers []config.ServerConfig) (bool, bool) {
\tfor i := range servers {
\t\tsrv := &servers[i]
\t\tif srv.TLS != nil && srv.TLS.Enabled && srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
\t\t\treturn srv.TLS.ACME.OCSPStaplingEnabled(), true
\t\t}
\t}
\treturn false, false
}

// updateACMEOCSPPolicy changes only the stable manager's stapling policy. Lean
// builds and alternate ACMEManager implementations simply do not expose this
// optional seam.
func (s *Server) updateACMEOCSPPolicy(servers []config.ServerConfig) {
\tpolicy, ok := s.ACME.(acmeOCSPPolicy)
\tif !ok {
\t\treturn
\t}
\tenabled, configured := acmeOCSPStaplingForServers(servers)
\tif configured {
\t\tpolicy.SetOCSPStapling(enabled)
\t}
}

// certProviderFor selects the certificate provider for a TLS listen address.
''',
)

# ---------------------------------------------------------------------------
# Lifecycle authority.
# ---------------------------------------------------------------------------
replace_once(
    "internal/lifecycle/registry.go",
    '''\t\t"admin.history_dir",
\t\t"admin.history_keep",
\t\t"admin.listen",
''',
    '''\t\t"admin.history_dir",
\t\t"admin.listen",
''',
)
replace_once(
    "internal/lifecycle/registry.go",
    '''\t\thot("admin.max_event_conns", SubAdmin, "new SSE admissions use the captured per-client connection cap while existing leases and connection counts survive policy reload"),
\t)
''',
    '''\t\thot("admin.max_event_conns", SubAdmin, "new SSE admissions use the captured per-client connection cap while existing leases and connection counts survive policy reload"),
\t\thot("admin.history_keep", SubAdmin, "the existing history backend keeps an atomic retention policy published with the admin runtime; tightening prunes only after Publish and prune failure is advisory (#106/#159)"),
\t)
''',
)
replace_once(
    "internal/lifecycle/registry.go",
    '''func rateLimitEntries() []Entry {
\tout := hotGroup(SubRateLimit, reasonRateLimitPolicy,
\t\t"rate_limit.burst",
\t\t"rate_limit.enabled",
\t\t"rate_limit.key",
\t\t"rate_limit.rate",
\t)
\tout = append(out, newListener("rate_limit.max_conns", SubRateLimit,
\t\t"the concurrent-connection cap is installed on each listener when it binds, so a kept address keeps the cap it bound with"))
\treturn out
}
''',
    '''func rateLimitEntries() []Entry {
\treturn hotGroup(SubRateLimit, reasonRateLimitPolicy,
\t\t"rate_limit.burst",
\t\t"rate_limit.enabled",
\t\t"rate_limit.key",
\t\t"rate_limit.rate",
\t\t"rate_limit.max_conns",
\t)
}
''',
)
replace_once(
    "internal/lifecycle/registry.go",
    '''\t\t"servers.*.tls.acme.email",
\t\t"servers.*.tls.acme.enabled",
\t\t"servers.*.tls.acme.ocsp_stapling")...)
\tout = append(out, reserved("servers.*.tls.acme.dns_provider", SubACME,
''',
    '''\t\t"servers.*.tls.acme.email",
\t\t"servers.*.tls.acme.enabled")...)
\tout = append(out, hot("servers.*.tls.acme.ocsp_stapling", SubACME,
\t\t"the process-lifetime ACME manager keeps a stable stapling wrapper whose atomic policy changes at Publish; no manager, account, cache, HostPolicy or listener is replaced (#106)"))
\tout = append(out, reserved("servers.*.tls.acme.dns_provider", SubACME,
''',
)

# ---------------------------------------------------------------------------
# Focused regression tests.
# ---------------------------------------------------------------------------
write("internal/server/dynamic_conn_limit_test.go", r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"errors"
	"net"
	"testing"
	"time"

	"jul/internal/config"
)

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c
}

func acceptAsync(l net.Listener) <-chan struct {
	conn net.Conn
	err  error
} {
	ch := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		c, err := l.Accept()
		ch <- struct {
			conn net.Conn
			err  error
		}{c, err}
	}()
	return ch
}

func mustAccept(t *testing.T, ch <-chan struct {
	conn net.Conn
	err  error
}) net.Conn {
	t.Helper()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("accept: %v", got.err)
		}
		return got.conn
	case <-time.After(2 * time.Second):
		t.Fatal("accept timed out")
		return nil
	}
}

func assertBlocked(t *testing.T, ch <-chan struct {
	conn net.Conn
	err  error
}) {
	t.Helper()
	select {
	case got := <-ch:
		if got.conn != nil {
			_ = got.conn.Close()
		}
		t.Fatalf("accept completed while admission should be blocked: err=%v", got.err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDynamicConnLimiterLiveTransitions(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newDynamicConnLimiter(raw, 2)
	defer l.Close()

	client1 := dialTCP(t, raw.Addr().String())
	server1 := mustAccept(t, acceptAsync(l))
	defer client1.Close()
	defer server1.Close()
	client2 := dialTCP(t, raw.Addr().String())
	server2 := mustAccept(t, acceptAsync(l))
	defer client2.Close()
	defer server2.Close()

	client3 := dialTCP(t, raw.Addr().String())
	third := acceptAsync(l)
	assertBlocked(t, third)

	l.SetLimit(3)
	server3 := mustAccept(t, third)
	defer client3.Close()
	defer server3.Close()
	if got := l.Active(); got != 3 {
		t.Fatalf("active=%d want 3", got)
	}

	// Lowering below the current active count never terminates admitted conns.
	l.SetLimit(1)
	client4 := dialTCP(t, raw.Addr().String())
	fourth := acceptAsync(l)
	assertBlocked(t, fourth)
	_ = server1.Close()
	_ = server2.Close()
	assertBlocked(t, fourth) // active == 1 is still at the cap
	_ = server3.Close()
	server4 := mustAccept(t, fourth)
	defer client4.Close()
	defer server4.Close()

	// Unlimited becomes effective immediately.
	l.SetLimit(0)
	client5 := dialTCP(t, raw.Addr().String())
	server5 := mustAccept(t, acceptAsync(l))
	defer client5.Close()
	defer server5.Close()
}

func TestDynamicConnLimiterCloseReleasesExactlyOnceAndUnblocksWaiter(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newDynamicConnLimiter(raw, 1)

	client1 := dialTCP(t, raw.Addr().String())
	server1 := mustAccept(t, acceptAsync(l))
	defer client1.Close()
	if err := server1.Close(); err != nil {
		t.Fatal(err)
	}
	_ = server1.Close()
	if got := l.Active(); got != 0 {
		t.Fatalf("active after double close=%d want 0", got)
	}

	client2 := dialTCP(t, raw.Addr().String())
	server2 := mustAccept(t, acceptAsync(l))
	defer client2.Close()
	client3 := dialTCP(t, raw.Addr().String())
	blocked := acceptAsync(l)
	assertBlocked(t, blocked)
	if err := l.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	select {
	case got := <-blocked:
		if !errors.Is(got.err, net.ErrClosed) {
			t.Fatalf("blocked accept after close err=%v want net.ErrClosed", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener close did not wake blocked admission")
	}
	_ = server2.Close()
}

func TestEffectiveConnectionCapMasterSwitch(t *testing.T) {
	cfg := &config.Config{}
	cfg.RateLimit.MaxConns = 7
	if got := effectiveConnectionCap(cfg); got != 0 {
		t.Fatalf("disabled cap=%d want unlimited", got)
	}
	cfg.RateLimit.Enabled = true
	if got := effectiveConnectionCap(cfg); got != 7 {
		t.Fatalf("enabled cap=%d want 7", got)
	}
	cfg.RateLimit.Enabled = false
	if got := effectiveConnectionCap(cfg); got != 0 {
		t.Fatalf("disabled-again cap=%d want unlimited", got)
	}
}

func TestMaxConnsNoLongerChangesListenerFingerprint(t *testing.T) {
	base := &config.Config{RateLimit: config.RateLimitConfig{Enabled: true, MaxConns: 2}}
	base.Servers = []config.ServerConfig{{Listen: "127.0.0.1:8080"}}
	next := *base
	next.RateLimit = base.RateLimit
	next.RateLimit.MaxConns = 20
	if before, after := listenerBindFingerprint(base, base.Servers[0].Listen), listenerBindFingerprint(&next, next.Servers[0].Listen); before != after {
		t.Fatalf("max_conns still changes bind fingerprint: before=%q after=%q", before, after)
	}
}
''')

write("internal/admin/history_retention_hot_test.go", r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"jul/internal/config"
)

func countHistorySnapshots(t *testing.T, h *history) int {
	t.Helper()
	names, err := h.snapshotFiles()
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

func seedHistoryWithMeta(t *testing.T, h *history, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, _, err := h.snapshotWithMeta([]byte("value = 1\n"), &HistoryMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryRetentionStagesThenPrunesOnlyAfterPublish(t *testing.T) {
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	s := &Server{hist: h, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}

	candidate := config.AdminConfig{HistoryKeep: 1}
	prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.retention(); got != 3 {
		t.Fatalf("Prepare mutated live retention: got %d want 3", got)
	}
	if got := countHistorySnapshots(t, h); got != 3 {
		t.Fatalf("Prepare pruned files: got %d want 3", got)
	}

	s.CommitPreparedAdminRuntime(prepared)
	if got := h.retention(); got != 1 {
		t.Fatalf("Publish retention=%d want 1", got)
	}
	if got := countHistorySnapshots(t, h); got != 3 {
		t.Fatalf("Publish performed destructive prune: got %d want 3", got)
	}

	s.RetirePreparedAdminRuntime(context.Background(), prepared)
	if got := countHistorySnapshots(t, h); got != 1 {
		t.Fatalf("PostCommit prune count=%d want 1", got)
	}
}

func TestHistoryRetentionAbortAndIncreaseDeleteNothing(t *testing.T) {
	h := newHistory(t.TempDir(), 2)
	seedHistoryWithMeta(t, h, 2)
	s := &Server{hist: h}

	candidate := config.AdminConfig{HistoryKeep: 1}
	prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	s.AbortPreparedAdminRuntime(prepared)
	if got := h.retention(); got != 2 {
		t.Fatalf("Abort retention=%d want 2", got)
	}
	if got := countHistorySnapshots(t, h); got != 2 {
		t.Fatalf("Abort deleted snapshots: %d", got)
	}

	candidate.HistoryKeep = 5
	prepared, err = s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	s.RetirePreparedAdminRuntime(context.Background(), prepared)
	if got := countHistorySnapshots(t, h); got != 2 {
		t.Fatalf("retention increase deleted snapshots: %d", got)
	}
}

func TestHistoryRetentionPruneFailureIsAdvisory(t *testing.T) {
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	h.remove = func(string) error { return errors.New("injected remove failure") }
	s := &Server{hist: h, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	candidate := config.AdminConfig{HistoryKeep: 1}
	prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	s.RetirePreparedAdminRuntime(context.Background(), prepared)
	if got := h.retention(); got != 1 {
		t.Fatalf("applied retention rolled back after prune failure: %d", got)
	}
	status := s.adminRuntimeStatus(nil)
	if status.HistoryRetentionHealth != "prune_failed" {
		t.Fatalf("history health=%q want prune_failed", status.HistoryRetentionHealth)
	}
}

func TestHistoryRetentionConcurrentOperationsRaceClean(t *testing.T) {
	h := newHistory(t.TempDir(), 20)
	seedHistoryWithMeta(t, h, 5)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				h.setRetention((i+j)%7 + 1)
				_, _ = h.list()
				_ = h.pruneCurrent()
			}
		}(i)
	}
	wg.Wait()
}

func TestHistoryPruneLeavesForeignFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 1)
	seedHistoryWithMeta(t, h, 3)
	foreign := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.pruneCurrent(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
		t.Fatalf("foreign file changed: data=%q err=%v", got, err)
	}
}
''')

write("internal/lifecycle/final_tranche_test.go", r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import "testing"

func TestFinalTrancheLifecycleDecisions(t *testing.T) {
	for _, path := range []string{
		"rate_limit.max_conns",
		"admin.history_keep",
		"servers.*.tls.acme.ocsp_stapling",
	} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("missing lifecycle entry %s", path)
		}
		if e.Class != HotReloadClass || e.StartupConsumed {
			t.Fatalf("%s = class %s startup=%t, want hot_reload/startup=false", path, e.Class, e.StartupConsumed)
		}
	}
	for _, path := range []string{
		"admin.history_dir",
		"servers.*.http3.enabled",
		"servers.*.tls.min_version",
		"servers.*.tls.client_auth.mode",
	} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("missing lifecycle entry %s", path)
		}
		if e.Class != RestartRequiredClass {
			t.Fatalf("%s = %s, want restart_required", path, e.Class)
		}
	}
}
''')

# Update existing OCSP wrapping test to the new stable wrapper contract and add
# a live policy behavior test using the existing self-contained PKI fixtures.
replace_once(
    "internal/server/acme_ocsp_test.go",
    '\t"net/http/httptest"\n\t"testing"',
    '\t"net/http/httptest"\n\t"sync/atomic"\n\t"testing"',
)
start = read("internal/server/acme_ocsp_test.go")
old_start = start.index("func TestProviderOCSPWrapping")
old_end = start.index("// TestOCSPFetchRespectsEgressGuard", old_start)
new_test = r'''func TestProviderOCSPWrapping(t *testing.T) {
	on, off := true, false

	cfg := acmeServerCfg()
	cfg.Servers[0].TLS.ACME.OCSPStapling = &on
	manager, err := NewACMEManager(cfg.Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewACMEManager: %v", err)
	}
	provider := manager.Provider(nil)
	if _, ok := provider.(*dynamicOCSPProvider); !ok {
		t.Fatalf("expected stable *dynamicOCSPProvider, got %T", provider)
	}
	concrete := manager.(*acmeManager)
	if !concrete.ocsp.Load() {
		t.Fatal("initial OCSP policy should be enabled")
	}
	concrete.SetOCSPStapling(false)
	if concrete.ocsp.Load() {
		t.Fatal("OCSP policy did not disable in place")
	}
	concrete.SetOCSPStapling(true)
	if !concrete.ocsp.Load() {
		t.Fatal("OCSP policy did not re-enable in place")
	}

	cfgOff := acmeServerCfg()
	cfgOff.Servers[0].TLS.ACME.OCSPStapling = &off
	managerOff, err := NewACMEManager(cfgOff.Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewACMEManager (off): %v", err)
	}
	if managerOff.(*acmeManager).ocsp.Load() {
		t.Fatal("initial OCSP policy should be disabled")
	}
}

func TestDynamicOCSPProviderStopsNewRefreshesWhenDisabled(t *testing.T) {
	ca := newOCSPTestCA(t)
	cert := ca.leaf(t, true)
	base := certProviderFunc(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil })
	fetched := make(chan struct{}, 2)
	st := newTestStapler(cert, func(context.Context, []byte, string) ([]byte, error) {
		fetched <- struct{}{}
		return nil, errors.New("test fetch")
	})
	var enabled atomic.Bool
	p := &dynamicOCSPProvider{enabled: &enabled, base: base, stapler: st}

	if _, err := p.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetched:
		t.Fatal("disabled policy initiated an OCSP fetch")
	case <-time.After(75 * time.Millisecond):
	}

	enabled.Store(true)
	if _, err := p.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetched:
	case <-time.After(2 * time.Second):
		t.Fatal("enabled policy did not initiate OCSP refresh")
	}

	// Let the failed refresh clear its refreshing flag, then prove a disabled
	// lookup does not start another one. Work already started before disable is
	// allowed to finish.
	time.Sleep(25 * time.Millisecond)
	enabled.Store(false)
	if _, err := p.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetched:
		t.Fatal("disabled policy initiated a new OCSP refresh")
	case <-time.After(75 * time.Millisecond):
	}
}

'''
write("internal/server/acme_ocsp_test.go", start[:old_start] + new_test + start[old_end:])

print("issue106 production/test patch applied")

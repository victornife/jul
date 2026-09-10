from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one match, found {count}: {old[:80]!r}")
    p.write_text(text.replace(old, new, 1))


# Process-wide weak physical-owner registry + warning throttle state.
replace_once(
    "internal/admin/audit.go",
    """\tcurrentSink        *auditSinkGeneration
\tsinkCfg            auditSinkConfig
\tsinkConfigured     bool
\tnextSinkGeneration uint64

\tactiveFailure""",
    """\tcurrentSink        *auditSinkGeneration
\tsinkCfg            auditSinkConfig
\tsinkConfigured     bool
\tnextSinkGeneration uint64
\t// sinkOwners keeps a weak normalized-path -> physical-owner identity while
\t// any live or retiring generation can still use that owner. A rapid A -> B
\t// -> A therefore reuses the still-draining A writer instead of opening a
\t// second writer/rotator for the same path.
\tsinkOwners map[string]*auditFileOwner

\tactiveFailure""",
)
replace_once(
    "internal/admin/audit.go",
    """\tretirementFailures uint64
\tlog                *slog.Logger
}""",
    """\tretirementFailures uint64
\tlog                *slog.Logger

\t// warnMu bounds repetitive operator-log amplification from a broken
\t// durable sink. Failure counters and active health are never suppressed.
\twarnMu                 sync.Mutex
\tlastSinkWarning        time.Time
\tsuppressedSinkWarnings uint64
}""",
)

# Runtime constants and physical-owner health.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tauditDefaultRotateMaxMB = 100
\tauditDefaultRotateKeep  = 14
\tauditBackupTimeFormat   = \"2006-01-02T15-04-05.000\"
)""",
    """\tauditDefaultRotateMaxMB = 100
\tauditDefaultRotateKeep  = 14
\tauditBackupTimeFormat   = \"2006-01-02T15-04-05.000\"
\tauditSinkWarnInterval   = 30 * time.Second
)""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tlifeMu sync.Mutex
\trefs   int
\tlive   bool
\tclosed bool

\t// mu serializes""",
    """\tlifeMu sync.Mutex
\trefs   int
\tlive   bool
\tclosed bool

\t// healthMu is separate from the physical writer mutex so Publish/status
\t// never waits on disk I/O. The health state belongs to the physical owner,
\t// not a logical generation: same-path and rapid A -> B -> A reuse must keep
\t// an existing degradation until an ordered successful write proves recovery.
\thealthMu      sync.Mutex
\thealthFailure auditFailureCategory
\thealthAt      time.Time

\t// mu serializes""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\treturn &auditLog{buf: make([]AuditEvent, capacity)}
}""",
    """\treturn &auditLog{
\t\tbuf:        make([]AuditEvent, capacity),
\t\tsinkOwners: make(map[string]*auditFileOwner),
\t}
}""",
)

# Preparation reuses any still-live owner for the target path, not only current.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tcurrent := a.currentSink
\tif !cfg.enabled() {
\t\ta.mu.Unlock()
\t\treturn &preparedAuditSink{log: a, cfg: cfg}, nil
\t}
\tif current != nil && current.cfg.path == cfg.path {
\t\tcurrent.owner.retain()
\t\tcandidate := &auditSinkGeneration{cfg: cfg, owner: current.owner, drained: make(chan struct{})}
\t\ta.mu.Unlock()
\t\treturn &preparedAuditSink{log: a, candidate: candidate, cfg: cfg}, nil
\t}
\ta.mu.Unlock()
""",
    """\tif !cfg.enabled() {
\t\ta.mu.Unlock()
\t\treturn &preparedAuditSink{log: a, cfg: cfg}, nil
\t}
\tif owner := a.sinkOwners[cfg.path]; owner != nil {
\t\tif owner.tryRetain() {
\t\t\tcandidate := &auditSinkGeneration{cfg: cfg, owner: owner, drained: make(chan struct{})}
\t\t\ta.mu.Unlock()
\t\t\treturn &preparedAuditSink{log: a, candidate: candidate, cfg: cfg}, nil
\t\t}
\t\t// refs==0 owners no longer write or rotate. Remove only this stale weak
\t\t// identity; a later prepared owner will be registered at Publish.
\t\tdelete(a.sinkOwners, cfg.path)
\t}
\ta.mu.Unlock()
""",
)

# Publish registers the candidate physical owner and adopts its current health.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\t\tp.old = a.currentSink
\t\ta.nextSinkGeneration++
\t\tif p.candidate != nil {
\t\t\tp.candidate.id = a.nextSinkGeneration
\t\t\tp.candidate.owner.activate()
\t\t}
\t\ta.currentSink = p.candidate
\t\ta.sinkCfg = p.cfg
\t\ta.sinkConfigured = p.cfg.enabled()
\t\ta.activeFailure = \"\"
\t\ta.activeFailureAt = time.Time{}
""",
    """\t\tp.old = a.currentSink
\t\ta.nextSinkGeneration++
\t\tif p.candidate != nil {
\t\t\tp.candidate.id = a.nextSinkGeneration
\t\t\tp.candidate.owner.activate()
\t\t\tif a.sinkOwners == nil {
\t\t\t\ta.sinkOwners = make(map[string]*auditFileOwner)
\t\t\t}
\t\t\ta.sinkOwners[p.candidate.cfg.path] = p.candidate.owner
\t\t\ta.activeFailure, a.activeFailureAt = p.candidate.owner.healthSnapshot()
\t\t} else {
\t\t\ta.activeFailure = \"\"
\t\t\ta.activeFailureAt = time.Time{}
\t\t}
\t\ta.currentSink = p.candidate
\t\ta.sinkCfg = p.cfg
\t\ta.sinkConfigured = p.cfg.enabled()
""",
)

# Abort/release remove only a closed exact owner from the weak registry.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\t\tif p.candidate != nil {
\t\t\t_ = p.candidate.owner.release(context.Background(), false)
\t\t}
""",
    """\t\tif p.candidate != nil {
\t\t\t_ = p.candidate.owner.release(context.Background(), false)
\t\t\tp.log.forgetClosedAuditOwner(p.candidate.cfg.path, p.candidate.owner)
\t\t}
""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """func (a *auditLog) releaseGeneration(gen *auditSinkGeneration, ctx context.Context) error {
\tif gen == nil {
\t\treturn nil
\t}
\tvar releaseErr error
\tgen.releaseOnce.Do(func() {
\t\treleaseErr = gen.owner.release(ctx, true)
\t})
\treturn releaseErr
}
""",
    """func (a *auditLog) releaseGeneration(gen *auditSinkGeneration, ctx context.Context) error {
\tif gen == nil {
\t\treturn nil
\t}
\tvar releaseErr error
\tgen.releaseOnce.Do(func() {
\t\treleaseErr = gen.owner.release(ctx, true)
\t\ta.forgetClosedAuditOwner(gen.cfg.path, gen.owner)
\t})
\treturn releaseErr
}

// forgetClosedAuditOwner removes only the exact weak registry entry after an
// owner has reached zero references. Lock ordering is auditLog.mu -> lifeMu,
// the same order as prepareTransition; release never acquires auditLog.mu while
// holding lifeMu.
func (a *auditLog) forgetClosedAuditOwner(path string, owner *auditFileOwner) {
\tif a == nil || owner == nil {
\t\treturn
\t}
\ta.mu.Lock()
\tdefer a.mu.Unlock()
\tif a.sinkOwners[path] != owner {
\t\treturn
\t}
\towner.lifeMu.Lock()
\tclosed := owner.closed
\towner.lifeMu.Unlock()
\tif closed {
\t\tdelete(a.sinkOwners, path)
\t}
}
""",
)

# Status derives active truth from the current physical owner. Historical last
# failure remains process-cumulative and can therefore describe an old owner.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tst := &AuditSinkStatus{
\t\tConfigured:      true,
\t\tActive:          a.currentSink != nil,
\t\tHealthy:         a.currentSink != nil && a.activeFailure == \"\",
\t\tWriteFailures:   a.writeFailures,
""",
    """\tactiveFailure := a.activeFailure
\tif a.currentSink != nil {
\t\tactiveFailure, _ = a.currentSink.owner.healthSnapshot()
\t}
\tst := &AuditSinkStatus{
\t\tConfigured:      true,
\t\tActive:          a.currentSink != nil,
\t\tHealthy:         a.currentSink != nil && activeFailure == \"\",
\t\tWriteFailures:   a.writeFailures,
""",
)

# Rate-limited persistence warning path.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """func auditLogWarn(log *slog.Logger, msg, path string, err error) {
\tif log != nil {
\t\tlog.Warn(msg, \"path\", path, \"err\", err)
\t}
}

func (a *auditLog) record""",
    """func auditLogWarn(log *slog.Logger, msg, path string, err error) {
\tif log != nil {
\t\tlog.Warn(msg, \"path\", path, \"err\", err)
\t}
}

// allowSinkWarning rate-limits only repetitive operator-log emission. It never
// suppresses failure accounting or a health transition.
func (a *auditLog) allowSinkWarning(now time.Time) (bool, uint64) {
\ta.warnMu.Lock()
\tdefer a.warnMu.Unlock()
\tif !a.lastSinkWarning.IsZero() && now.Before(a.lastSinkWarning.Add(auditSinkWarnInterval)) {
\t\ta.suppressedSinkWarnings++
\t\treturn false, 0
\t}
\tsuppressed := a.suppressedSinkWarnings
\ta.suppressedSinkWarnings = 0
\ta.lastSinkWarning = now
\treturn true, suppressed
}

func (a *auditLog) warnPersistence(gen *auditSinkGeneration, result auditWriteResult) {
\tif a == nil || a.log == nil || gen == nil || result.err == nil {
\t\treturn
\t}
\tallowed, suppressed := a.allowSinkWarning(time.Now())
\tif !allowed {
\t\treturn
\t}
\targs := []any{\"category\", result.category, \"path\", gen.cfg.publicPath, \"err\", result.err}
\tif suppressed > 0 {
\t\targs = append(args, \"suppressed\", suppressed)
\t}
\ta.log.Warn(\"audit sink persistence failed\", args...)
}

func (a *auditLog) record""",
)

# A marshal failure owns a ticket too. Advance that ticket in order so later
# durable events cannot deadlock behind an encode failure.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tline, err := json.Marshal(ev)
\tif err != nil {
\t\ta.completeWrite(gen, auditWriteResult{category: auditFailureEncode, err: err})
\t\treturn
\t}
""",
    """\tline, err := json.Marshal(ev)
\tif err != nil {
\t\tresult := auditWriteResult{category: auditFailureEncode, err: err}
\t\tgen.owner.skip(ticket, result)
\t\ta.completeWrite(gen, result)
\t\treturn
\t}
""",
)

# Completion treats any logical generation sharing the currently active
# physical owner as relevant to active health. Physical owner health itself is
# updated in ticket order inside write/skip.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tshouldRelease := gen.retired && gen.releaseRequested && gen.inflight == 0
\tif result.err != nil {
\t\tnow := time.Now().UTC()
""",
    """\tshouldRelease := gen.retired && gen.releaseRequested && gen.inflight == 0
\tactiveOwner := a.currentSink != nil && a.currentSink.owner == gen.owner
\tif result.err != nil {
\t\tnow := time.Now().UTC()
""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\t\tif a.currentSink == gen {
\t\t\ta.activeFailure = result.category
\t\t\ta.activeFailureAt = now
\t\t}
\t} else if a.currentSink == gen {
\t\ta.activeFailure = \"\"
\t\ta.activeFailureAt = time.Time{}
""",
    """\t\tif activeOwner {
\t\t\ta.activeFailure, a.activeFailureAt = gen.owner.healthSnapshot()
\t\t}
\t} else if activeOwner {
\t\ta.activeFailure, a.activeFailureAt = gen.owner.healthSnapshot()
""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tif result.err != nil {
\t\tauditLogWarn(a.log, \"audit sink persistence failed\", gen.cfg.publicPath, result.err)
\t}
""",
    """\tif result.err != nil {
\t\ta.warnPersistence(gen, result)
\t}
""",
)

# Owner lifecycle and health helpers. tryRetain refuses an owner that has
# already reached zero refs/closed. Health snapshots never take the writer lock.
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """func (o *auditFileOwner) retain() {
\to.lifeMu.Lock()
\to.refs++
\to.lifeMu.Unlock()
}

func (o *auditFileOwner) activate""",
    """func (o *auditFileOwner) tryRetain() bool {
\to.lifeMu.Lock()
\tdefer o.lifeMu.Unlock()
\tif o.closed {
\t\treturn false
\t}
\to.refs++
\treturn true
}

func (o *auditFileOwner) healthSnapshot() (auditFailureCategory, time.Time) {
\to.healthMu.Lock()
\tdefer o.healthMu.Unlock()
\treturn o.healthFailure, o.healthAt
}

func (o *auditFileOwner) noteOrderedHealth(result auditWriteResult) {
\to.healthMu.Lock()
\tdefer o.healthMu.Unlock()
\tif result.err == nil {
\t\to.healthFailure = \"\"
\t\to.healthAt = time.Time{}
\t\treturn
\t}
\to.healthFailure = result.category
\to.healthAt = time.Now().UTC()
}

func (o *auditFileOwner) activate""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tresult := o.writeLocked(cfg, p)
\to.turn++
\to.cond.Broadcast()
\to.mu.Unlock()
\treturn result
}

func (o *auditFileOwner) writeLocked""",
    """\tresult := o.writeLocked(cfg, p)
\t// Update physical-owner health before advancing the turn. This makes health
\t// follow durable ticket order even if goroutines call completeWrite later in
\t// a different scheduler order.
\to.noteOrderedHealth(result)
\to.turn++
\to.cond.Broadcast()
\to.mu.Unlock()
\treturn result
}

func (o *auditFileOwner) skip(ticket uint64, result auditWriteResult) {
\to.mu.Lock()
\tfor ticket != o.turn {
\t\to.cond.Wait()
\t}
\to.noteOrderedHealth(result)
\to.turn++
\to.cond.Broadcast()
\to.mu.Unlock()
}

func (o *auditFileOwner) writeLocked""",
)

# Reject symlinks/non-directories anywhere in the existing ancestor chain and
# verify root identities around OpenRoot. Rooted MkdirAll then protects creation
# of the missing suffix.
marker = "func prepareAuditParent(parent string) (auditRootHandle, []createdAuditDir, error) {"
helper = """// validateAuditDirChain rejects a symlink or non-directory in every existing
// component of an absolute parent path. The caller also compares directory
// identity around OpenRoot so a replacement race cannot silently redirect the
// prepared root.
func validateAuditDirChain(path string) (fs.FileInfo, error) {
\tpath = filepath.Clean(path)
\tif !filepath.IsAbs(path) {
\t\tabs, err := filepath.Abs(path)
\t\tif err != nil {
\t\t\treturn nil, err
\t\t}
\t\tpath = abs
\t}
\tvolume := filepath.VolumeName(path)
\trest := strings.TrimPrefix(path, volume)
\trest = strings.TrimLeft(rest, string(filepath.Separator))
\tcurrent := volume
\tif filepath.IsAbs(path) {
\t\tcurrent = volume + string(filepath.Separator)
\t}
\tfor _, part := range strings.Split(rest, string(filepath.Separator)) {
\t\tif part == \"\" || part == \".\" {
\t\t\tcontinue
\t\t}
\t\tcurrent = filepath.Join(current, part)
\t\tinfo, err := os.Lstat(current)
\t\tif err != nil {
\t\t\treturn nil, err
\t\t}
\t\tif info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
\t\t\treturn nil, fmt.Errorf(\"audit parent component %q is not a regular directory\", current)
\t\t}
\t}
\treturn os.Lstat(path)
}

"""
p = Path("internal/admin/audit_sink_runtime.go")
text = p.read_text()
if text.count(marker) != 1:
    raise SystemExit("prepareAuditParent marker mismatch")
p.write_text(text.replace(marker, helper + marker, 1))
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\troot, err := os.OpenRoot(ancestor)
\tif err != nil {
\t\treturn nil, nil, err
\t}
\trel, err := filepath.Rel(ancestor, parent)
""",
    """\tancestorInfo, err := validateAuditDirChain(ancestor)
\tif err != nil {
\t\treturn nil, nil, err
\t}
\troot, err := os.OpenRoot(ancestor)
\tif err != nil {
\t\treturn nil, nil, err
\t}
\topenedAncestor, err := root.Lstat(\".\")
\tif err != nil || !os.SameFile(ancestorInfo, openedAncestor) {
\t\t_ = root.Close()
\t\treturn nil, nil, fmt.Errorf(\"audit parent identity changed while opening\")
\t}
\trel, err := filepath.Rel(ancestor, parent)
""",
)
replace_once(
    "internal/admin/audit_sink_runtime.go",
    """\tparentRoot, err := root.OpenRoot(rel)
\t_ = root.Close()
\tif err != nil {
\t\treturn nil, nil, err
\t}
\treturn &osAuditRootHandle{root: parentRoot}, created, nil
""",
    """\tparentInfo, err := root.Lstat(rel)
\tif err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
\t\t_ = root.Close()
\t\treturn nil, nil, fmt.Errorf(\"prepared audit parent identity is unsafe\")
\t}
\tparentRoot, err := root.OpenRoot(rel)
\t_ = root.Close()
\tif err != nil {
\t\treturn nil, nil, err
\t}
\topenedParent, err := parentRoot.Lstat(\".\")
\tif err != nil || !os.SameFile(parentInfo, openedParent) {
\t\t_ = parentRoot.Close()
\t\treturn nil, nil, fmt.Errorf(\"prepared audit parent changed while opening\")
\t}
\treturn &osAuditRootHandle{root: parentRoot}, created, nil
""",
)

# Avoid reporting a historical retired-resource category as the active health
# reason. Readiness already has a bounded reason token; authenticated detail can
# remain generic while AuditSinkStatus separately exposes historical last-failure.
replace_once(
    "internal/admin/admin_health.go",
    """\t\tif st := s.audit.statusReport(); st != nil && !st.Healthy {
\t\t\tdetail := \"durable audit sink is degraded\"
\t\t\tif st.LastFailureCategory != \"\" {
\t\t\t\tdetail += \" (\" + st.LastFailureCategory + \")\"
\t\t\t}
\t\t\treturn &AdminHealthStatus{Healthy: false, Reason: \"audit_sink\", Detail: detail}
\t\t}
""",
    """\t\tif st := s.audit.statusReport(); st != nil && !st.Healthy {
\t\t\treturn &AdminHealthStatus{Healthy: false, Reason: \"audit_sink\", Detail: \"durable audit sink is degraded\"}
\t\t}
""",
)

# Tests: rapid path reuse while old A is still draining; same-owner health;
# warning throttle; encode-ticket progression; and ENOSPC classification.
with Path("internal/admin/issue160_writer_matrix_test.go").open("a") as f:
    f.write(r'''

func TestAuditSinkRapidAtoBtoAReusesStillDrainingPhysicalOwner(t *testing.T) {
\td := t.TempDir()
\taPath := filepath.Join(d, "a.jsonl")
\tbPath := filepath.Join(d, "b.jsonl")
\ta := newAuditLogWithSink(16, aPath, 10, 4, nil)
\toldA := a.currentSink.owner
\tbase := oldA.file
\tstarted := make(chan struct{})
\trelease := make(chan struct{})
\tfirstDone := make(chan struct{})
\tvar once sync.Once
\toldA.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) {
\t\tonce.Do(func() { close(started) })
\t\t<-release
\t\treturn base.Write(p)
\t}}
\tgo func() {
\t\ta.record(AuditEvent{Operation: "a-before-switch", Result: "success"})
\t\tclose(firstDone)
\t}()
\t<-started

\ttoB, err := a.prepareTransition(mustAuditCfg(t, bPath, 10, 4))
\tif err != nil {
\t\tt.Fatal(err)
\t}
\ttoB.commit()
\tretiredA := make(chan struct{})
\tgo func() {
\t\ttoB.retire(context.Background())
\t\tclose(retiredA)
\t}()

\tbackToA, err := a.prepareTransition(mustAuditCfg(t, aPath, 5, 2))
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif backToA == nil || backToA.candidate == nil || backToA.candidate.owner != oldA {
\t\tt.Fatal("rapid A->B->A opened a second physical owner for A")
\t}
\tbackToA.commit()
\tbackToA.retire(context.Background())
\tsecondDone := make(chan struct{})
\tgo func() {
\t\ta.record(AuditEvent{Operation: "a-after-return", Result: "success"})
\t\tclose(secondDone)
\t}()

\tclose(release)
\t<-firstDone
\t<-secondDone
\t<-retiredA
\tif err := a.Close(); err != nil {
\t\tt.Fatal(err)
\t}
\tif ids := readAuditIDs(t, aPath); len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
\t\tt.Fatalf("rapid A->B->A durable IDs=%v want [1 2]", ids)
\t}
}

func TestAuditSinkReusedOwnerCarriesLateFailureIntoCurrentGeneration(t *testing.T) {
\td := t.TempDir()
\taPath := filepath.Join(d, "a.jsonl")
\tbPath := filepath.Join(d, "b.jsonl")
\ta := newAuditLogWithSink(16, aPath, 10, 4, nil)
\toldA := a.currentSink.owner
\tbase := oldA.file
\tstarted := make(chan struct{})
\trelease := make(chan struct{})
\tdone := make(chan struct{})
\tvar once sync.Once
\toldA.file = &auditFaultFile{base: base, writeFn: func([]byte) (int, error) {
\t\tonce.Do(func() { close(started) })
\t\t<-release
\t\treturn 0, errors.New("late A failure")
\t}}
\tgo func() {
\t\ta.record(AuditEvent{Operation: "late-fail", Result: "success"})
\t\tclose(done)
\t}()
\t<-started

\ttoB, err := a.prepareTransition(mustAuditCfg(t, bPath, 10, 4))
\tif err != nil {
\t\tt.Fatal(err)
\t}
\ttoB.commit()
\tgo toB.retire(context.Background())
\tbackToA, err := a.prepareTransition(mustAuditCfg(t, aPath, 5, 2))
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif backToA.candidate.owner != oldA {
\t\tt.Fatal("A owner not reused")
\t}
\tbackToA.commit()
\tbackToA.retire(context.Background())
\tclose(release)
\t<-done
\tif st := a.statusReport(); st == nil || st.Healthy || st.LastFailureCategory != string(auditFailureWrite) {
\t\tt.Fatalf("late old-generation failure did not degrade reused current owner: %+v", st)
\t}
\toldA.mu.Lock()
\toldA.file = base
\toldA.mu.Unlock()
\ta.record(AuditEvent{Operation: "recover-reused-a", Result: "success"})
\tif st := a.statusReport(); st == nil || !st.Healthy || st.LastFailureCategory != string(auditFailureWrite) {
\t\tt.Fatalf("ordered successful write did not recover active owner health: %+v", st)
\t}
\t_ = a.Close()
}

func TestAuditSinkSameOwnerPolicyPublishPreservesDegradationUntilSuccessfulWrite(t *testing.T) {
\tpath := filepath.Join(t.TempDir(), "audit.jsonl")
\ta := newAuditLogWithSink(8, path, 10, 4, nil)
\towner := a.currentSink.owner
\tbase := owner.file
\towner.file = &auditFaultFile{base: base, writeFn: func([]byte) (int, error) { return 0, errors.New("injected write") }}
\ta.record(AuditEvent{Operation: "fail-before-policy", Result: "success"})
\towner.mu.Lock()
\towner.file = base
\towner.mu.Unlock()

\tp, err := a.prepareTransition(mustAuditCfg(t, path, 5, 2))
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tp.commit()
\tp.retire(context.Background())
\tif st := a.statusReport(); st == nil || st.Healthy || st.LastFailureCategory != string(auditFailureWrite) {
\t\tt.Fatalf("same-owner publish hid active degradation: %+v", st)
\t}
\ta.record(AuditEvent{Operation: "health-proof", Result: "success"})
\tif st := a.statusReport(); st == nil || !st.Healthy || st.LastFailureCategory != string(auditFailureWrite) || st.WriteFailures != 1 {
\t\tt.Fatalf("successful write did not recover active health while preserving failure history: %+v", st)
\t}
\t_ = a.Close()
}

func TestAuditSinkWarningThrottleAccounting(t *testing.T) {
\ta := newAuditLog(8)
\tbase := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
\tif ok, suppressed := a.allowSinkWarning(base); !ok || suppressed != 0 {
\t\tt.Fatalf("first warning ok=%v suppressed=%d", ok, suppressed)
\t}
\tif ok, _ := a.allowSinkWarning(base.Add(time.Second)); ok {
\t\tt.Fatal("second warning inside throttle window was emitted")
\t}
\tif ok, _ := a.allowSinkWarning(base.Add(2 * time.Second)); ok {
\t\tt.Fatal("third warning inside throttle window was emitted")
\t}
\tif ok, suppressed := a.allowSinkWarning(base.Add(auditSinkWarnInterval)); !ok || suppressed != 2 {
\t\tt.Fatalf("warning after window ok=%v suppressed=%d want true/2", ok, suppressed)
\t}
}

func TestAuditSinkEncodeFailureAdvancesTicketAndRingContinues(t *testing.T) {
\tpath := filepath.Join(t.TempDir(), "audit.jsonl")
\ta := newAuditLogWithSink(8, path, 10, 4, nil)
\ta.record(AuditEvent{Time: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Operation: "encode-fail", Result: "success"})
\ta.record(AuditEvent{Operation: "after-encode-fail", Result: "success"})
\tif got := a.snapshot("", "", 0); len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
\t\tt.Fatalf("ring after encode failure=%+v", got)
\t}
\tif ids := readAuditIDs(t, path); len(ids) != 1 || ids[0] != 2 {
\t\tt.Fatalf("durable IDs after encode failure=%v want [2]", ids)
\t}
\tif st := a.statusReport(); st == nil || !st.Healthy || st.WriteFailures != 1 || st.LastFailureCategory != string(auditFailureEncode) {
\t\tt.Fatalf("status after encode recovery=%+v", st)
\t}
\t_ = a.Close()
}
''')

# Linux-specific intermediate symlink component regression.
with Path("internal/admin/issue160_filesystem_linux_test.go").open("a") as f:
    f.write(r'''

func TestIssue160FilesystemRejectsIntermediateSymlinkParent(t *testing.T) {
\troot := t.TempDir()
\trealDir := filepath.Join(root, "real")
\tif err := os.MkdirAll(filepath.Join(realDir, "nested"), 0o750); err != nil {
\t\tt.Fatal(err)
\t}
\tlink := filepath.Join(root, "link")
\tif err := os.Symlink(realDir, link); err != nil {
\t\tt.Fatal(err)
\t}
\tpath := filepath.Join(link, "nested", "audit.jsonl")
\tif _, err := prepareAuditFileOwner(path); err == nil {
\t\tt.Fatal("intermediate symlink component accepted")
\t}
\tif _, err := os.Stat(path); !os.IsNotExist(err) {
\t\tt.Fatalf("rejected symlink path created an artifact: %v", err)
\t}
}
''')

# Documentation must match the actual owner/health/logging protocol.
replace_once(
    "docs/audit-sink-hot-reload.md",
    "A same-path rotation-policy update shares one physical file owner across the old and new logical generations. This is the single-maintenance-owner handoff: there is never an old and new rotator concurrently operating on the same path. A path A→B transition leaves all A files/backups in place and B does not import them. B→A later appends to the still-valid A history.",
    "A same-path rotation-policy update shares one physical file owner across the old and new logical generations. Physical owners remain weakly registered by normalized path until their final generation releases them, so a rapid A→B→A while the first A is still draining reuses that owner and cannot create a second rotator for A. This is the single-maintenance-owner handoff: there is never an old and new rotator concurrently operating on the same path. A path A→B transition leaves all A files/backups in place and B does not import them. B→A later appends to the still-valid A history.",
)
replace_once(
    "docs/audit-sink-hot-reload.md",
    "A configured active sink write/rotation failure degrades admin health and readiness while the ring continues. A candidate Prepare failure does not poison the currently live generation's health. Publishing a healthy B after degraded A makes B the active health truth; disabling a degraded sink clears active durable-sink readiness degradation. Successful later writes recover transient active health while cumulative counters do not decrease. A retention-cleanup failure after a successful event write records retention degradation rather than claiming the event was lost.",
    "A configured active sink write/rotation failure degrades admin health and readiness while the ring continues. Physical-owner health is advanced in durable ticket order, so same-path handoff or rapid path reuse cannot hide a late old-generation failure. Repetitive persistence warnings are process-locally rate-limited to one emission per 30 seconds, with the next emitted warning reporting how many log lines were suppressed; failure counters and health transitions are never suppressed. A candidate Prepare failure does not poison the currently live generation's health. Publishing a healthy B after degraded A makes B the active health truth; reusing A restores A's physical-owner health until a successful ordered write proves recovery; disabling a degraded sink clears active durable-sink readiness degradation. Successful later writes recover transient active health while cumulative counters and last-failure history do not decrease. A retention-cleanup failure after a successful event write records retention degradation rather than claiming the event was lost.",
)

print("HR-07C audited correctness patch staged")

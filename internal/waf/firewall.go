// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/experimental"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/jcchavezs/mergefs"
	mergefsio "github.com/jcchavezs/mergefs/io"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/redact"
	"jul/internal/versionid"
)

// Compiled reports whether this binary includes WAF support. It is true in
// builds with the "waf" tag, which link the Coraza engine and the embedded
// OWASP Core Rule Set.
const Compiled = true

// argumentLimitGuardRule is the generated CRS-policy rule placed after the
// embedded CRS and before operator rules (#697). It acts on the engine's own
// ARGUMENTS_LIMIT_REACHED flag, so it follows any SecArgumentsLimit an operator
// sets. Block mode denies with 413; the final SecRuleEngine DetectionOnly turns
// it into a recorded detect-mode finding. Operators remove it deliberately with
// SecRuleRemoveById 900001.
var argumentLimitGuardRule = fmt.Sprintf(
	`SecRule ARGUMENTS_LIMIT_REACHED "@eq 1" "id:%d,phase:2,deny,status:413,log,t:none,msg:'Request argument limit reached; arguments beyond SecArgumentsLimit were not inspected',tag:'jul/argument-limit'"`,
	config.WAFArgumentLimitGuardRuleID)

// Firewall wraps a configured Coraza engine and exposes it as a middleware.
type Firewall struct {
	waf         coraza.WAF
	info        PolicyInfo
	opts        Options
	hasExternal bool
	sourceSum   [32]byte
	policySum   [32]byte
	assets      []ExternalAssetRecord
	facts       inspection.WAFFacts
	closed      *atomic.Bool
	// state is the compiled engine configuration the request path sizes
	// reservations and its read loop from.
	state engineState
	audit auditConfig
}

// New builds a Firewall from a WAF policy. It assembles the SecLang directive
// program (optional embedded CRS, user directive files, inline rules, and the
// enforcement-mode override), compiles the engine, and wires the per-rule error
// callback to the supplied metrics/log hooks. It returns an error if the rules
// fail to compile so a reload surfaces the problem instead of silently serving
// without protection. ctx bounds the compilation and is checked before and
// after the most expensive steps.
func New(ctx context.Context, cfg config.WAFConfig, opts Options) (*Firewall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directives, err := buildDirectives(cfg)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rec := newSourceRecorder(mergefsio.OSFS)
	var counter ruleCounter
	wcfg := coraza.NewWAFConfig().
		WithRootFS(rootFS(cfg, rec)).
		WithDirectives(directives).
		WithRequestBodyAccess().
		WithErrorCallback(errorCallback(cfg, opts))
	wcfg = experimental.WAFConfigWithRuleObserver(wcfg, func(r types.RuleMetadata) { counter.observeRule(r.ID(), r.File()) })
	if limit := cfg.RequestBodyLimit.Bytes(); limit > 0 {
		wcfg = wcfg.WithRequestBodyLimit(int(limit))
		if limit < 131072 {
			wcfg = wcfg.WithRequestBodyInMemoryLimit(int(limit))
		}
	}

	w, err := coraza.NewWAF(wcfg)
	if err != nil {
		return nil, fmt.Errorf("waf: compiling rules: %w", err)
	}
	if counter.collisionID != 0 {
		_ = closeWAF(w)
		return nil, fmt.Errorf("waf: compiling rules: external rule id %d collides with Jul reserved generated range (%d–%d)",
			counter.collisionID, config.WAFGeneratedExceptionIDBase-1, config.WAFGeneratedRuleIDMax)
	}
	activeRuleIDs, err := compiledRuleIDs(w)
	if err != nil {
		_ = closeWAF(w)
		return nil, err
	}
	state, err := compiledEngineState(w)
	if err != nil {
		_ = closeWAF(w)
		return nil, err
	}
	for i, exc := range cfg.Exceptions {
		if !activeRuleIDs[exc.RuleID] {
			_ = closeWAF(w)
			return nil, fmt.Errorf("waf: compiling rules: exception[%d] references rule_id %d which does not exist in the compiled ruleset",
				i, exc.RuleID)
		}
	}
	if err := ctx.Err(); err != nil {
		_ = closeWAF(w)
		return nil, err
	}
	// Audit part J resolves MIME types; load Go's process-wide table now
	// rather than inside a governed record's construction.
	_ = mime.TypeByExtension(".txt")
	audit, err := compiledAuditConfig(w)
	if err != nil && opts.Governor != nil {
		_ = closeWAF(w)
		return nil, err
	}
	probe := w.NewTransaction()
	if _, ok := innerTx(probe); !ok && opts.Governor != nil {
		_ = probe.Close()
		_ = closeWAF(w)
		return nil, fmt.Errorf("waf: incompatible coraza transaction layout (%T)", probe)
	}
	if _, err := readTxAuditState(probe); err != nil && opts.Governor != nil {
		_ = probe.Close()
		_ = closeWAF(w)
		return nil, err
	}
	_ = probe.Close()
	// Wrap the engine so rules see Jul's canonical client address rather than
	// Coraza's own parse of RemoteAddr (see clientaddr.go).
	state.argumentLimitGuard = cfg.CRSEnabled && activeRuleIDs[config.WAFArgumentLimitGuardRuleID]
	info, rawSum, polSum := policyInfo(cfg, counter.counts, rec, opts.Versions, state)
	return &Firewall{
		waf:         &clientAddrWAF{WAF: w},
		info:        info,
		facts:       inspectionFacts(state),
		opts:        opts,
		hasExternal: info.ExternalFiles > 0,
		sourceSum:   rawSum,
		policySum:   polSum,
		assets:      rec.externalAssets(),
		closed:      &atomic.Bool{},
		state:       state,
		audit:       audit,
	}, nil
}

// Info describes the compiled policy this Firewall enforces.
func (f *Firewall) Info() PolicyInfo { return f.info }

// InspectionFacts returns the compiled engine limits that size this WAF's
// worst-case inspection charge (ADR 0032 §7).
func (f *Firewall) InspectionFacts() inspection.WAFFacts { return f.facts }

func inspectionFacts(st engineState) inspection.WAFFacts {
	nonNegative := func(v int64) uint64 {
		if v < 0 {
			return 0
		}
		return uint64(v)
	}
	return inspection.WAFFacts{
		RequestBodyLimit:         nonNegative(st.requestBodyLimit),
		RequestBodyInMemoryLimit: nonNegative(st.requestBodyInMemoryLimit),
		ResponseBodyLimit:        nonNegative(st.responseBodyLimit),
		ArgumentLimit:            nonNegative(int64(st.argumentLimit)),
	}
}

// SourceChanged reports whether two compiled Firewalls read different external
// file bytes, using the private in-memory digest chain rather than any public
// identifier (#649).
func (f *Firewall) SourceChanged(other *Firewall) bool {
	if f == nil || other == nil {
		return f != other
	}
	return f.hasExternal != other.hasExternal || f.sourceSum != other.sourceSum
}

// SourceDigest returns the hex-encoded private in-memory digest of the exact
// external files read during compilation, or "" when no external files were
// read (#642, #563).
func (f *Firewall) SourceDigest() string {
	if f == nil || !f.hasExternal {
		return ""
	}
	return hex.EncodeToString(f.sourceSum[:])
}

// RawPolicyDigest returns the hex-encoded private in-memory SHA-256 digest of
// the complete compiled policy inputs (#630, ADR 0031 §3.1).
func (f *Firewall) RawPolicyDigest() string {
	if f == nil {
		return ""
	}
	return hex.EncodeToString(f.policySum[:])
}

// ExternalAssets returns a copy of the private per-file external asset records
// read during compilation (#630, #584). It is used only by privileged local
// manifest/recovery verification and never exposed on status endpoints.
func (f *Firewall) ExternalAssets() []ExternalAssetRecord {
	if f == nil || len(f.assets) == 0 {
		return nil
	}
	return append([]ExternalAssetRecord(nil), f.assets...)
}

func policyInfo(cfg config.WAFConfig, counts RuleCounts, rec *sourceRecorder, versions *versionid.Context, state engineState) (PolicyInfo, [32]byte, [32]byte) {
	info := PolicyInfo{
		Mode:                   "block",
		BlockStatus:            cfg.BlockStatus,
		CRSEnabled:             cfg.CRSEnabled,
		RequestBodyLimitBytes:  cfg.RequestBodyLimit.Bytes(),
		ResponseBodyInspection: state.responseBodyAccess,
		ArgumentLimit:          state.argumentLimit,
		ArgumentLimitGuard:     state.argumentLimitGuard,
		InlineRules:            strings.TrimSpace(cfg.InlineRules) != "",
		Rules:                  counts,
	}
	if state.responseBodyAccess {
		info.ResponseBodyMimeTypes = boundedMimeTypes(state.responseBodyMimeTypes)
	}
	info.ResponseBodyOperatorOverride = responseBodyOverridden(cfg.ResponseBodyCheck, state)
	if cfg.Mode == "detect" {
		info.Mode = "detect"
	}
	if info.BlockStatus == 0 {
		info.BlockStatus = 403
	}
	if info.RequestBodyLimitBytes == 0 {
		info.RequestBodyLimitBytes = 128 << 10
	}
	for _, p := range cfg.DirectivesFiles {
		if strings.TrimSpace(p) != "" {
			info.RuleFilesConfigured++
		}
	}
	if cfg.CRSEnabled {
		info.CRSVersion = EmbeddedCRSVersion()
		blocking, detection, isDefault := effectiveCRSParanoia(cfg)
		info.Paranoia = blocking
		info.BlockingParanoia = blocking
		info.DetectionParanoia = detection
		info.ParanoiaDefault = isDefault
		if !isDefault && info.Rules.Inline > 0 {
			// buildDirectives emitted the paranoia SecAction (id 900000).
			info.Rules.Inline--
			info.Rules.Generated++
		}
		if info.Rules.Inline > 0 {
			// buildDirectives emitted the argument-limit guard (id 900001); it is
			// counted as generated even when an operator later removes it, which
			// ArgumentLimitGuard reports separately.
			info.Rules.Inline--
			info.Rules.Generated++
		}
	}
	if n := len(cfg.Exceptions); n > 0 {
		info.ExceptionsCount, info.ExceptionsReviewDue = SummarizeExceptions(cfg.Exceptions, time.Now())
		for i := 0; i < n && info.Rules.Inline > 0; i++ {
			info.Rules.Inline--
			info.Rules.Generated++
		}
	}
	info.HasCustomTuningDirectives = rec.hasCustomTuning() || DetectCustomTuningDirectives(cfg.InlineRules)
	var rawSum [32]byte
	info.ExternalFiles, info.ExternalDigest, rawSum = rec.identity(versions)
	polBytes := canonicalPolicyBytes(cfg, info, rawSum)
	polSum := sha256.Sum256(polBytes)
	info.PolicyDigest = versions.Digest("waf-compiled-policy-v1", polBytes)
	return info, rawSum, polSum
}

func effectiveCRSParanoia(cfg config.WAFConfig) (blocking int, detection int, isDefault bool) {
	blocking = cfg.EffectiveBlockingParanoia()
	detection = cfg.EffectiveDetectionParanoia()
	if blocking <= 0 && detection <= 0 {
		return 1, 1, true
	}
	if blocking <= 0 {
		blocking = 1
	}
	if detection <= 0 {
		detection = blocking
	}
	return blocking, detection, false
}

func canonicalPolicyBytes(cfg config.WAFConfig, info PolicyInfo, rawExternalSum [32]byte) []byte {
	var b bytes.Buffer
	writeFramed := func(k, v string) {
		fmt.Fprintf(&b, "%s=%d:%s\n", k, len(v), v)
	}
	writeFramed("engine", EngineVersion())
	writeFramed("crs_version", info.CRSVersion)
	writeFramed("mode", info.Mode)
	writeFramed("block_status", strconv.Itoa(info.BlockStatus))
	writeFramed("crs_enabled", strconv.FormatBool(info.CRSEnabled))
	writeFramed("paranoia", strconv.Itoa(info.Paranoia))
	writeFramed("blocking_paranoia", strconv.Itoa(info.BlockingParanoia))
	writeFramed("detection_paranoia", strconv.Itoa(info.DetectionParanoia))
	writeFramed("request_body_limit", strconv.FormatInt(info.RequestBodyLimitBytes, 10))
	writeFramed("response_body_inspection", strconv.FormatBool(info.ResponseBodyInspection))
	// Version 2 of Jul's generated directives adds the argument-limit guard and
	// the response-inspection default (#697, #698), so an evaluation of a
	// version-1 policy is stale even when the TOML and engine are unchanged.
	writeFramed("generated_directives", "2")
	writeFramed("response_body_mime_types", strings.Join(info.ResponseBodyMimeTypes, " "))
	writeFramed("argument_limit", strconv.Itoa(info.ArgumentLimit))
	writeFramed("argument_limit_guard", strconv.FormatBool(info.ArgumentLimitGuard))
	writeFramed("external_raw_sha256", hex.EncodeToString(rawExternalSum[:]))
	inlineSum := sha256.Sum256([]byte(strings.TrimSpace(cfg.InlineRules)))
	writeFramed("inline_sha256", hex.EncodeToString(inlineSum[:]))
	if excLines, _, err := BuildExceptionDirectives(cfg.Exceptions); err == nil && len(excLines) > 0 {
		excSum := sha256.Sum256([]byte(strings.Join(excLines, "\n")))
		writeFramed("exceptions_sha256", hex.EncodeToString(excSum[:]))
	}
	return b.Bytes()
}

// Middleware returns the per-location middleware that runs each request (and,
// when response_body_check is set, each response) through the engine. A blocked
// request is short-circuited by Coraza with the status configured in the
// SecLang program.
func (f *Firewall) Middleware() middleware.Middleware {
	return f.RouteMiddleware(RouteAttribution{}, nil)
}

// RouteMiddleware returns a per-location middleware bound to route and genFn
// while sharing the compiled Coraza engine (#632-A).
func (f *Firewall) RouteMiddleware(route RouteAttribution, genFn func() uint64) middleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.serveHTTP(w, r, next, route, genFn)
		})
	}
}

// Close releases cached memoizer resources owned by the Coraza engine. It is
// idempotent so verification, evaluation, and generational teardown paths can
// close safely.
func (f *Firewall) Close() error {
	if f == nil {
		return nil
	}
	if f.closed != nil && !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	return closeWAF(f.waf)
}

// IsClosed reports whether Close has been called on f.
func (f *Firewall) IsClosed() bool {
	if f == nil || f.closed == nil {
		return true
	}
	return f.closed.Load()
}

func closeWAF(w coraza.WAF) error {
	if w == nil {
		return nil
	}
	if cw, ok := w.(*clientAddrWAF); ok {
		w = cw.WAF
	}
	if closer, ok := w.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// rootFS selects the filesystem the SecLang parser resolves Include directives
// against. With the embedded CRS it merges the rule-set assets with the OS
// filesystem so both "@owasp_crs/..." includes and user files on disk resolve;
// otherwise it is the OS filesystem alone. OS reads go through rec so the
// exact compiled bytes are identified.
func rootFS(cfg config.WAFConfig, rec *sourceRecorder) fs.FS {
	var root fs.FS
	if cfg.CRSEnabled {
		root = mergefs.Merge(coreruleset.FS, rec)
	} else {
		root = rec
	}
	return &normalizeFS{inner: root}
}

// buildDirectives assembles the SecLang program in a deterministic order:
//
//  1. SecDefaultAction (all phases) with the configured block_status — emitted
//     ONLY when crs_enabled is false — so that any rule using the generic
//     "block" action or no explicit disruptive action inherits
//     "deny,status:<block_status>" instead of Coraza's hardcoded 403 fallback.
//     When CRS is enabled this step is skipped: @crs-setup.conf.example already
//     defines its own SecDefaultAction for phases 1–4 and Coraza rejects a
//     duplicate, so CRS-scored anomaly blocks use the CRS setup's default
//     status (403) rather than block_status. See docs/waf.md for the overrides
//     (inline_rule / directives_files) that recover block_status under CRS.
//  2. the embedded CRS when crs_enabled, followed by the generated
//     argument-limit guard (id 900001, #697);
//  3. Jul's response-body inspection default from response_body_check (#698);
//  4. each user directive file;
//  5. the inline rules snippet;
//  6. the enforcement-mode override last, so "block"/"detect" always wins.
func buildDirectives(cfg config.WAFConfig) (string, error) {
	var b strings.Builder

	bs := cfg.BlockStatus
	if bs == 0 {
		bs = 403
	}
	// SecDefaultAction sets the default deny status for rules without an
	// explicit status action.  We emit it before user rules so inline rules
	// inherit the configured block_status.
	//
	// When CRS is enabled we skip our SecDefaultAction because the embedded
	// @crs-setup.conf.example already defines its own for phases 1–4 and
	// Coraza rejects duplicates.  Consequently CRS-scored anomaly blocks use
	// the CRS setup's default status (403) rather than block_status.
	// See docs/waf.md for work-arounds (inline_rule overrides, directives_files).
	if !cfg.CRSEnabled {
		for phase := 1; phase <= 4; phase++ {
			fmt.Fprintf(&b, "SecDefaultAction \"phase:%d,deny,status:%d,log\"\n", phase, bs)
		}
	}

	excLines, _, err := BuildExceptionDirectives(cfg.Exceptions)
	if err != nil {
		return "", err
	}

	if cfg.CRSEnabled {
		b.WriteString("Include @coraza.conf-recommended\n")
		b.WriteString("Include @crs-setup.conf.example\n")
		blocking, detection, isDefault := effectiveCRSParanoia(cfg)
		if !isDefault {
			fmt.Fprintf(&b,
				"SecAction \"id:900000,phase:1,nolog,pass,t:none,setvar:tx.blocking_paranoia_level=%d,setvar:tx.detection_paranoia_level=%d\"\n",
				blocking, detection)
		}
		for _, line := range excLines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteString("Include @owasp_crs/*.conf\n")
		fmt.Fprintf(&b, "%s\n", argumentLimitGuardRule)
	} else {
		for _, line := range excLines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}

	// Jul's response-inspection default follows the embedded configuration and
	// precedes operator files and inline rules, so a deliberate operator
	// SecResponseBodyAccess / SecResponseBodyMimeType still wins (#698). It is
	// a directive rather than a coraza.WAFConfig option because Coraza applies
	// those options after every directive, overriding the operator.
	// The MIME selection is set either way, so an operator who only turns
	// access On gets the same bounded set with and without CRS.
	if cfg.ResponseBodyCheck {
		b.WriteString("SecResponseBodyAccess On\n")
	} else {
		b.WriteString("SecResponseBodyAccess Off\n")
	}
	fmt.Fprintf(&b, "SecResponseBodyMimeType %s\n", strings.Join(DefaultResponseBodyMimeTypes, " "))

	for _, path := range cfg.DirectivesFiles {
		p := strings.TrimSpace(path)
		if p == "" {
			continue
		}
		fmt.Fprintf(&b, "Include %s\n", p)
	}

	if r := strings.TrimSpace(cfg.InlineRules); r != "" {
		b.WriteString(r)
		b.WriteByte('\n')
	}

	// Enforce the configured mode last so it overrides any engine state set by
	// the included rule files.
	switch cfg.Mode {
	case "detect":
		b.WriteString("SecRuleEngine DetectionOnly\n")
	default:
		b.WriteString("SecRuleEngine On\n")
	}

	return b.String(), nil
}

// errorCallback reports each matched rule to the metrics and log hooks. Coraza
// invokes it per relevant rule match (in both block and detect modes); the
// configured mode labels the event so dashboards can tell enforced blocks from
// detection-only signals.
func errorCallback(cfg config.WAFConfig, opts Options) func(types.MatchedRule) {
	return func(mr types.MatchedRule) {
		ruleID := strconv.Itoa(mr.Rule().ID())
		if opts.Hooks.OnEvent != nil {
			opts.Hooks.OnEvent(cfg.Mode, ruleID)
		}
		if opts.Logger != nil {
			rawURI := mr.URI()
			// Coraza exposes the full, unparsed request URI and its Message value
			// may contain macro-expanded request data. Keep the structured event
			// useful without copying attacker-controlled query values or matched
			// data into long-lived logs. The shared path sanitizer strips query and
			// fragment text without decoding, redacts token-like path segments, and
			// applies a hard length bound. Global secret redaction remains a final
			// defense for configured secrets that appear in an otherwise-safe path.
			path := redact.Apply(observability.SanitizePath(rawURI))
			opts.Logger.Warn("waf rule matched",
				"rule_id", ruleID,
				"path", path,
				"query_omitted", strings.ContainsAny(rawURI, "?#"),
				"mode", cfg.Mode,
				"phase", int(mr.Rule().Phase()),
				"severity", mr.Rule().Severity().String())
		}
	}
}

// compiledRuleIDs is the isolated compatibility adapter that extracts the
// authoritative final set of active rule IDs from the compiled Coraza WAF's
// RuleGroup after Coraza's SecLang parser has processed all includes,
// backslash continuations, and sequential rule additions/removals (#631,
// ADR 0031 §4.2). If an upstream Coraza version changes its internal struct
// layout, it fails closed with an explicit compatibility error distinct from a
// missing rule_id.
func compiledRuleIDs(w coraza.WAF) (map[int]bool, error) {
	engine, err := compiledEngine(w)
	if err != nil {
		return nil, err
	}
	rulesGroup := engine.FieldByName("Rules")
	if !rulesGroup.IsValid() || rulesGroup.Kind() != reflect.Struct {
		return nil, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing Rules struct field on %T", w)
	}
	rulesSlice := rulesGroup.FieldByName("rules")
	if !rulesSlice.IsValid() || rulesSlice.Kind() != reflect.Slice {
		return nil, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing Rules.rules slice field on %T", w)
	}
	elemType := rulesSlice.Type().Elem()
	if elemType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("waf: incompatible coraza.WAF internal layout: Rules.rules element is %s, want struct", elemType.Kind())
	}
	idStructField, ok := elemType.FieldByName("ID_")
	if !ok || (idStructField.Type.Kind() != reflect.Int && idStructField.Type.Kind() != reflect.Int64) {
		return nil, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing Rule.ID_ int field on %s", elemType.String())
	}
	out := make(map[int]bool, rulesSlice.Len())
	for i := 0; i < rulesSlice.Len(); i++ {
		idField := rulesSlice.Index(i).FieldByName("ID_")
		if id := int(idField.Int()); id > 0 {
			out[id] = true
		}
	}
	return out, nil
}

// compiledEngine returns the *corazawaf.WAF struct behind a coraza.WAF wrapper,
// failing closed with an explicit compatibility error if the layout changed.
func compiledEngine(w coraza.WAF) (reflect.Value, error) {
	v := reflect.ValueOf(w)
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: nil wrapper (%T)", w)
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: expected wrapper struct, got %T", w)
	}
	wafField := v.FieldByName("waf")
	if !wafField.IsValid() || wafField.Kind() != reflect.Pointer || wafField.IsNil() || wafField.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing *corazawaf.WAF field on %T", w)
	}
	return wafField.Elem(), nil
}

// engineState is the compiled, request-independent engine configuration
// after every directive (embedded, generated and operator) was applied.
// Request-specific ctl: actions can still change a single transaction, but
// body buffers keep the compiled limits as their capacity.
type engineState struct {
	responseBodyAccess    bool
	responseBodyMimeTypes []string
	argumentLimit         int
	argumentLimitGuard    bool
	// Compiled body limits for the inspection minimum budget (ADR 0032 §7).
	// requestBodyInMemoryLimit is 0 when Coraza leaves it unset, which means
	// the whole request body limit may be held in memory.
	requestBodyLimit         int64
	requestBodyInMemoryLimit int64
	responseBodyLimit        int64
	// requestBodyLimitReject: SecRequestBodyLimitAction Reject, so a write
	// that reaches the limit is refused without evaluating phase 2.
	requestBodyLimitReject bool
}

// compiledEngineState reads the authoritative compiled response-body and
// argument-limit settings through the same isolated adapter as
// compiledRuleIDs (#697, #698).
func compiledEngineState(w coraza.WAF) (engineState, error) {
	engine, err := compiledEngine(w)
	if err != nil {
		return engineState{}, err
	}
	access := engine.FieldByName("ResponseBodyAccess")
	mimes := engine.FieldByName("ResponseBodyMimeTypes")
	limit := engine.FieldByName("ArgumentLimit")
	if !access.IsValid() || access.Kind() != reflect.Bool ||
		!mimes.IsValid() || mimes.Kind() != reflect.Slice || mimes.Type().Elem().Kind() != reflect.String ||
		!limit.IsValid() || limit.Kind() != reflect.Int {
		return engineState{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing response-body or argument-limit settings on %T", w)
	}
	st := engineState{responseBodyAccess: access.Bool(), argumentLimit: int(limit.Int())}
	reqLimit := engine.FieldByName("RequestBodyLimit")
	respLimit := engine.FieldByName("ResponseBodyLimit")
	inMemory := engine.FieldByName("requestBodyInMemoryLimit")
	if !reqLimit.IsValid() || reqLimit.Kind() != reflect.Int64 ||
		!respLimit.IsValid() || respLimit.Kind() != reflect.Int64 ||
		!inMemory.IsValid() || inMemory.Kind() != reflect.Pointer || inMemory.Type().Elem().Kind() != reflect.Int64 {
		return engineState{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing body-limit settings on %T", w)
	}
	st.requestBodyLimit = reqLimit.Int()
	action := engine.FieldByName("RequestBodyLimitAction")
	if !action.IsValid() || action.Type() != reflect.TypeOf(types.BodyLimitActionReject) {
		return engineState{}, fmt.Errorf("waf: incompatible coraza.WAF internal layout: missing request body limit action on %T", w)
	}
	st.requestBodyLimitReject = types.BodyLimitAction(action.Int()) == types.BodyLimitActionReject
	st.responseBodyLimit = respLimit.Int()
	if !inMemory.IsNil() {
		st.requestBodyInMemoryLimit = inMemory.Elem().Int()
	}
	for i := 0; i < mimes.Len(); i++ {
		if m := strings.TrimSpace(mimes.Index(i).String()); m != "" {
			st.responseBodyMimeTypes = append(st.responseBodyMimeTypes, m)
		}
	}
	return st, nil
}

// maxReportedMimeTypes bounds the compiled MIME selection copied into status
// surfaces; operator directives can list arbitrarily many.
const (
	maxReportedMimeTypes   = 16
	maxReportedMimeTypeLen = 100
)

func boundedMimeTypes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	n := min(len(in), maxReportedMimeTypes)
	out := make([]string, 0, n)
	for _, m := range in[:n] {
		if len(m) > maxReportedMimeTypeLen {
			m = m[:maxReportedMimeTypeLen]
		}
		out = append(out, m)
	}
	return out
}

// responseBodyOverridden reports whether operator directives changed the
// compiled response-body access or MIME selection away from the default that
// response_body_check produces.
func responseBodyOverridden(check bool, st engineState) bool {
	if st.responseBodyAccess != check {
		return true
	}
	return check && strings.Join(st.responseBodyMimeTypes, " ") != strings.Join(DefaultResponseBodyMimeTypes, " ")
}

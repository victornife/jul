//go:build waf

package waf

import (
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
	"github.com/corazawaf/coraza/v3"
	corazahttp "github.com/corazawaf/coraza/v3/http"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/jcchavezs/mergefs"
	mergefsio "github.com/jcchavezs/mergefs/io"

	"jul/internal/config"
	"jul/internal/middleware"
)

// Compiled reports whether this binary includes WAF support. It is true in
// builds with the "waf" tag, which link the Coraza engine and the embedded
// OWASP Core Rule Set.
const Compiled = true

// Firewall wraps a configured Coraza engine and exposes it as a middleware.
type Firewall struct {
	waf         coraza.WAF
	blockStatus int
}

// New builds a Firewall from a WAF policy. It assembles the SecLang directive
// program (optional embedded CRS, user directive files, inline rules, and the
// enforcement-mode override), compiles the engine, and wires the per-rule error
// callback to the supplied metrics/log hooks. It returns an error if the rules
// fail to compile so a reload surfaces the problem instead of silently serving
// without protection.
func New(cfg config.WAFConfig, opts Options) (*Firewall, error) {
	directives, err := buildDirectives(cfg)
	if err != nil {
		return nil, err
	}

	wcfg := coraza.NewWAFConfig().
		WithRootFS(rootFS(cfg)).
		WithDirectives(directives).
		WithRequestBodyAccess().
		WithErrorCallback(errorCallback(cfg, opts))
	if limit := cfg.RequestBodyLimit.Bytes(); limit > 0 {
		wcfg = wcfg.WithRequestBodyLimit(int(limit))
	}
	if cfg.ResponseBodyCheck {
		wcfg = wcfg.WithResponseBodyAccess()
	}

	w, err := coraza.NewWAF(wcfg)
	if err != nil {
		return nil, fmt.Errorf("waf: compiling rules: %w", err)
	}
	bs := cfg.BlockStatus
	if bs == 0 {
		bs = 403
	}
	return &Firewall{waf: w, blockStatus: bs}, nil
}

// Middleware returns the per-location middleware that runs each request (and,
// when response_body_check is set, each response) through the engine. A blocked
// request is short-circuited by Coraza with the block_status configured in the
// policy (default 403). Because Coraza v3 hardcodes 403 when a rule does not
// carry an explicit status action, we intercept the WriteHeader call to apply
// the configured status.
func (f *Firewall) Middleware() middleware.Middleware {
	return func(next http.Handler) http.Handler {
		h := corazahttp.WrapHandler(f.waf, next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bw := &blockStatusWriter{ResponseWriter: w, status: f.blockStatus}
			h.ServeHTTP(bw, r)
		})
	}
}

// blockStatusWriter intercepts a 403 status written by Coraza so that the
// configured block_status is applied instead. It passes through everything else
// unchanged.
type blockStatusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *blockStatusWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	w.written = true
	if code == http.StatusForbidden {
		code = w.status
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *blockStatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close releases engine resources. The Coraza engine holds no resources that
// require explicit release, so this is a no-op kept for interface symmetry with
// the stub.
func (f *Firewall) Close() error { return nil }

// rootFS selects the filesystem the SecLang parser resolves Include directives
// against. With the embedded CRS it merges the rule-set assets with the OS
// filesystem so both "@owasp_crs/..." includes and user files on disk resolve;
// otherwise it is the OS filesystem alone.
func rootFS(cfg config.WAFConfig) fs.FS {
	if cfg.CRSEnabled {
		return mergefs.Merge(coreruleset.FS, mergefsio.OSFS)
	}
	return mergefsio.OSFS
}

// buildDirectives assembles the SecLang program in a deterministic order:
//
//  1. the embedded CRS (recommended base, setup, optional paranoia override,
//     then the rules) when crs_enabled;
//  2. each user directive file, included so its own relative includes resolve;
//  3. the inline rules snippet;
//  4. the enforcement-mode override last, so "block"/"detect" always wins over
//     any SecRuleEngine set by an included file.
func buildDirectives(cfg config.WAFConfig) (string, error) {
	var b strings.Builder

	if cfg.CRSEnabled {
		b.WriteString("Include @coraza.conf-recommended\n")
		b.WriteString("Include @crs-setup.conf.example\n")
		if cfg.Paranoia > 0 {
			fmt.Fprintf(&b,
				"SecAction \"id:900000,phase:1,nolog,pass,t:none,setvar:tx.blocking_paranoia_level=%d,setvar:tx.detection_paranoia_level=%d\"\n",
				cfg.Paranoia, cfg.Paranoia)
		}
		b.WriteString("Include @owasp_crs/*.conf\n")
	}

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
			opts.Logger.Warn("waf rule matched",
				"rule_id", ruleID,
				"uri", mr.URI(),
				"mode", cfg.Mode,
				"message", mr.Message())
		}
	}
}

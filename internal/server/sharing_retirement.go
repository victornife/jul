// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"jul/internal/config"
	"jul/internal/respwriter"
	"jul/internal/waf"
)

// LoadedAccessFiles holds the content snapshots of file-backed access inputs
// loaded by a serving generation so Publish can classify content-only changes
// (htpasswd, CRL, CA bundle, WAF rule files, and plugin modules; spec §9.5,
// ADR 0026 #642 amendment).
type LoadedAccessFiles struct {
	htpasswdByPath     map[string]htpasswdSnapshot
	htpasswdByLoc      map[[2]int]htpasswdSnapshot
	caDigestByAddr     map[string]string
	crlsByAddr         map[string][]crlSet
	wafDigestByLoc     map[[2]int]string
	globalWAFDigest    string
	pluginDigestByName map[string]string
}

type htpasswdSnapshot struct {
	rawDigest string
	users     map[string]string
	valid     bool
}

func captureLoadedAccessFiles(effCfg *config.Config) *LoadedAccessFiles {
	return captureLoadedAccessFilesOpt(effCfg, false)
}

func captureLoadedAccessFilesOpt(effCfg *config.Config, skipMTLS bool) *LoadedAccessFiles {
	out := &LoadedAccessFiles{
		htpasswdByPath:     make(map[string]htpasswdSnapshot),
		htpasswdByLoc:      make(map[[2]int]htpasswdSnapshot),
		caDigestByAddr:     make(map[string]string),
		crlsByAddr:         make(map[string][]crlSet),
		wafDigestByLoc:     make(map[[2]int]string),
		pluginDigestByName: make(map[string]string),
	}
	if effCfg == nil {
		return out
	}

	for name, p := range effCfg.Plugins {
		if path := strings.TrimSpace(p.Path); path != "" {
			if data, err := readBoundedMTLSFile(path, 64<<20); err == nil {
				sum := sha256.Sum256(data)
				out.pluginDigestByName[name] = hex.EncodeToString(sum[:])
			}
		}
	}

	out.globalWAFDigest = digestWAFDirectiveFiles(effCfg.WAF)

	if !skipMTLS {
		for _, addr := range uniqueListenAddrs(effCfg.Servers) {
			var caHashParts []string
			for _, srv := range effCfg.Servers {
				if srv.Listen != addr || srv.TLS == nil || !srv.TLS.Enabled || srv.TLS.ClientAuth == nil || !srv.TLS.ClientAuth.Active() {
					continue
				}
				if caPath := strings.TrimSpace(srv.TLS.ClientAuth.CAFile); caPath != "" {
					if data, err := readBoundedMTLSFile(caPath, 4<<20); err == nil {
						sum := sha256.Sum256(data)
						caHashParts = append(caHashParts, hex.EncodeToString(sum[:]))
					}
				}
			}
			if len(caHashParts) > 0 {
				sort.Strings(caHashParts)
				out.caDigestByAddr[addr] = strings.Join(caHashParts, "|")
			}
			if bundle, err := clientAuthForAddr(effCfg.Servers, addr, nil); err == nil && bundle != nil {
				out.crlsByAddr[addr] = bundle.crls
				if bundle.caDigest != "" {
					out.caDigestByAddr[addr] = bundle.caDigest
				}
			}
		}
	}

	for i, srv := range effCfg.Servers {
		for j, loc := range srv.Locations {
			if loc.Auth != nil && loc.Auth.Basic != nil {
				if p := strings.TrimSpace(loc.Auth.Basic.File); p != "" {
					snap, seen := out.htpasswdByPath[p]
					if !seen {
						snap = parseHtpasswdSnapshot(p)
						out.htpasswdByPath[p] = snap
					}
					out.htpasswdByLoc[[2]int{i, j}] = snap
				}
			}
			if loc.WAF != nil {
				out.wafDigestByLoc[[2]int{i, j}] = digestWAFDirectiveFiles(*loc.WAF)
			}
		}
	}
	return out
}

func (f *LoadedAccessFiles) applyMTLSRotation(c *clientAuthRotationComponent) {
	if f == nil || c == nil {
		return
	}
	for _, sw := range c.swaps {
		if sw.entry == nil || sw.bundle == nil {
			continue
		}
		if sw.bundle.caDigest != "" {
			f.caDigestByAddr[sw.entry.addr] = sw.bundle.caDigest
		}
		f.crlsByAddr[sw.entry.addr] = sw.bundle.crls
	}
}

func (f *LoadedAccessFiles) applyLiveMTLSListeners(retained map[string]*listenerEntry, staged map[string]*listenerEntry) {
	if f == nil || (len(retained) == 0 && len(staged) == 0) {
		return
	}
	clear(f.caDigestByAddr)
	clear(f.crlsByAddr)
	applyEntry := func(addr string, entry *listenerEntry) {
		if entry == nil || entry.clientAuth == nil {
			return
		}
		if b := entry.clientAuth.current.Load(); b != nil {
			if b.caDigest != "" {
				f.caDigestByAddr[addr] = b.caDigest
			}
			f.crlsByAddr[addr] = b.crls
		}
	}
	for addr, entry := range retained {
		applyEntry(addr, entry)
	}
	for addr, entry := range staged {
		applyEntry(addr, entry)
	}
}

func (f *LoadedAccessFiles) applyWAFDigests(global string, byLoc map[[2]int]string) {
	if f == nil {
		return
	}
	if global != "" {
		f.globalWAFDigest = global
	}
	for k, v := range byLoc {
		if v != "" {
			f.wafDigestByLoc[k] = v
		}
	}
}

func (f *LoadedAccessFiles) applyBasicAuthByLoc(byLoc map[[2]int][]byte) {
	if f == nil || len(byLoc) == 0 {
		return
	}
	if f.htpasswdByLoc == nil {
		f.htpasswdByLoc = make(map[[2]int]htpasswdSnapshot, len(byLoc))
	}
	for coord, raw := range byLoc {
		if len(raw) > 0 {
			f.htpasswdByLoc[coord] = parseHtpasswdBytes(raw)
		}
	}
}

func (f *LoadedAccessFiles) htpasswdSnapshotFor(srvIdx, locIdx int, path string) htpasswdSnapshot {
	if f == nil {
		return htpasswdSnapshot{}
	}
	if snap, ok := f.htpasswdByLoc[[2]int{srvIdx, locIdx}]; ok && snap.rawDigest != "" {
		return snap
	}
	return f.htpasswdByPath[path]
}

type loadedAccessHandler struct {
	http.Handler
	basicByLoc    map[[2]int][]byte
	wafGlobal     string
	wafByLoc      map[[2]int]string
	pluginDigests map[string]string
}

// WrapHandlersWithLoadedAccess attaches the generation-owned loaded access
// inputs (per-location Basic auth htpasswd bytes, compiled WAF source digests,
// and compiled WASM plugin digests) directly to the handler map returned by
// HandlerFactory so Server.Run and ReloadPlan.Prepare consume the exact
// prepared artifacts without re-reading them from disk (#642, #563).
func WrapHandlersWithLoadedAccess(
	handlers map[string]http.Handler,
	basicByLoc map[[2]int][]byte,
	wafGlobal string,
	wafByLoc map[[2]int]string,
	pluginDigests map[string]string,
) map[string]http.Handler {
	if len(handlers) == 0 {
		return handlers
	}
	out := make(map[string]http.Handler, len(handlers))
	for addr, h := range handlers {
		if h == nil {
			out[addr] = nil
			continue
		}
		out[addr] = &loadedAccessHandler{
			Handler:       h,
			basicByLoc:    basicByLoc,
			wafGlobal:     wafGlobal,
			wafByLoc:      wafByLoc,
			pluginDigests: pluginDigests,
		}
	}
	return out
}

func captureLoadedAccessFilesFromHandlers(effCfg *config.Config, handlers map[string]http.Handler) *LoadedAccessFiles {
	files := captureLoadedAccessFilesOpt(effCfg, true)
	for _, h := range handlers {
		if lah, ok := h.(*loadedAccessHandler); ok && lah != nil {
			files.applyBasicAuthByLoc(lah.basicByLoc)
			files.applyWAFDigests(lah.wafGlobal, lah.wafByLoc)
			for name, dig := range lah.pluginDigests {
				if dig != "" {
					files.pluginDigestByName[name] = dig
				}
			}
			break
		}
	}
	return files
}

// WithCompiledWAF binds engine-owned external source digests from compiled
// *waf.Firewall instances onto f so retirement comparison uses the exact bytes
// read during WAF compilation rather than re-reading files from disk (#642, #563).
func (f *LoadedAccessFiles) WithCompiledWAF(global *waf.Firewall, byLoc map[[2]int]*waf.Firewall) *LoadedAccessFiles {
	if f == nil {
		return nil
	}
	if global != nil {
		f.globalWAFDigest = global.SourceDigest()
	}
	for k, fw := range byLoc {
		if fw != nil {
			f.wafDigestByLoc[k] = fw.SourceDigest()
		}
	}
	return f
}

func parseHtpasswdSnapshot(path string) htpasswdSnapshot {
	data, err := os.ReadFile(path)
	if err != nil {
		return htpasswdSnapshot{}
	}
	return parseHtpasswdBytes(data)
}

func parseHtpasswdBytes(data []byte) htpasswdSnapshot {
	sum := sha256.Sum256(data)
	users := make(map[string]string)
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			return htpasswdSnapshot{rawDigest: hex.EncodeToString(sum[:]), valid: false}
		}
		user := line[:idx]
		if _, dup := users[user]; dup {
			return htpasswdSnapshot{rawDigest: hex.EncodeToString(sum[:]), valid: false}
		}
		users[user] = line
	}
	return htpasswdSnapshot{
		rawDigest: hex.EncodeToString(sum[:]),
		users:     users,
		valid:     true,
	}
}

func digestWAFDirectiveFiles(w config.WAFConfig) string {
	if !w.Enabled || (len(w.DirectivesFiles) == 0 && strings.TrimSpace(w.InlineRules) == "") {
		return ""
	}
	if waf.Compiled {
		if fw, err := waf.New(context.Background(), w, waf.Options{}); err == nil && fw != nil {
			dig := fw.SourceDigest()
			_ = fw.Close()
			if dig != "" {
				return dig
			}
		}
	}
	h := sha256.New()
	visited := make(map[string]bool)
	wroteAny := false

	var walkFile func(path string)
	var scanSecLangText func(text, baseDir string)

	resolveAndWalk := func(rawRef, baseDir string) {
		ref := strings.Trim(strings.TrimSpace(rawRef), `"'`)
		if ref == "" || strings.HasPrefix(ref, "@") {
			return
		}
		target := ref
		if !filepath.IsAbs(target) && baseDir != "" {
			target = filepath.Join(baseDir, target)
		}
		if strings.ContainsAny(target, "*?[") {
			if matches, err := filepath.Glob(target); err == nil && len(matches) > 0 {
				sort.Strings(matches)
				for _, m := range matches {
					walkFile(m)
				}
				return
			}
		}
		walkFile(target)
	}

	scanSecLangText = func(text, baseDir string) {
		var continued strings.Builder
		for _, rawLine := range strings.Split(text, "\n") {
			line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasSuffix(line, `\`) {
				continued.WriteString(strings.TrimSuffix(line, `\`))
				continued.WriteByte(' ')
				continue
			}
			if continued.Len() > 0 {
				continued.WriteString(line)
				line = continued.String()
				continued.Reset()
			}
			lower := strings.ToLower(line)
			for _, kw := range []string{"includeoptional ", "include "} {
				if strings.HasPrefix(lower, kw) {
					resolveAndWalk(line[len(kw):], baseDir)
				}
			}
			for _, op := range []string{"@pmfromfile ", "@pmf ", "@ipmatchfromfile ", "@ipmatchf "} {
				if idx := strings.Index(lower, op); idx >= 0 {
					rest := line[idx+len(op):]
					if end := strings.IndexAny(rest, `"'`); end >= 0 {
						rest = rest[:end]
					}
					resolveAndWalk(strings.TrimSpace(rest), baseDir)
				}
			}
		}
	}

	walkFile = func(path string) {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || clean == "." || visited[clean] {
			return
		}
		visited[clean] = true
		wroteAny = true
		data, err := os.ReadFile(clean)
		if err != nil {
			fmt.Fprintf(h, "err:%s\n", clean)
			return
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "file:%s:%x\n", clean, sum[:])
		scanSecLangText(string(data), filepath.Dir(clean))
	}

	for _, p := range w.DirectivesFiles {
		resolveAndWalk(p, "")
	}
	if strings.TrimSpace(w.InlineRules) != "" {
		scanSecLangText(w.InlineRules, "")
	}
	if !wroteAny {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func classifyHtpasswdContentChange(before, after htpasswdSnapshot) string {
	if before.rawDigest == after.rawDigest && before.rawDigest != "" {
		return "unchanged"
	}
	if !before.valid || !after.valid {
		if before.rawDigest == after.rawDigest {
			return "unchanged"
		}
		return "uncertain"
	}
	removed := 0
	for u, bLine := range before.users {
		aLine, ok := after.users[u]
		if !ok {
			removed++
			continue
		}
		if aLine != bLine {
			return "uncertain"
		}
	}
	for u := range after.users {
		if _, ok := before.users[u]; !ok {
			return "uncertain"
		}
	}
	if removed > 0 {
		return "narrowed"
	}
	return "unchanged"
}

func classifyCRLContentChange(before, after []crlSet) string {
	if len(before) == 0 && len(after) == 0 {
		return "unchanged"
	}
	bByIssuer := make(map[string]map[string]bool, len(before))
	for _, c := range before {
		iss := string(c.issuer)
		m := bByIssuer[iss]
		if m == nil {
			m = make(map[string]bool)
			bByIssuer[iss] = m
		}
		for s := range c.revoked {
			m[s] = true
		}
	}
	aByIssuer := make(map[string]map[string]bool, len(after))
	for _, c := range after {
		iss := string(c.issuer)
		m := aByIssuer[iss]
		if m == nil {
			m = make(map[string]bool)
			aByIssuer[iss] = m
		}
		for s := range c.revoked {
			m[s] = true
		}
	}
	if len(bByIssuer) != len(aByIssuer) {
		return "uncertain"
	}
	added := 0
	removed := 0
	for iss, bRev := range bByIssuer {
		aRev, ok := aByIssuer[iss]
		if !ok {
			return "uncertain"
		}
		for s := range aRev {
			if !bRev[s] {
				added++
			}
		}
		for s := range bRev {
			if !aRev[s] {
				removed++
			}
		}
	}
	switch {
	case added == 0 && removed == 0:
		return "unchanged"
	case added > 0 && removed == 0:
		return "narrowed"
	case removed > 0 && added == 0:
		return "widened"
	default:
		return "uncertain"
	}
}

func normalizeConfigForCompare(c *config.Config) *config.Config {
	if c == nil {
		return nil
	}
	if cloned, err := c.Clone(); err == nil {
		return cloned
	}
	return c
}

// ComputePublishEntryTransitions classifies all entries between the retiring
// generation (prevRaw, prevEff, prevFiles) and the new generation
// (nextRaw, nextEff, nextFiles) at Publish, returning the EntryTransition slice
// consumed by ExchangeRegistry.PublishGeneration (#642, #563, #562).
func ComputePublishEntryTransitions(
	prevRaw, prevEff *config.Config,
	prevFiles *LoadedAccessFiles,
	nextRaw, nextEff *config.Config,
	nextFiles *LoadedAccessFiles,
	pluginChanges []PluginModuleChange,
	keepOpenEntries []string,
) []respwriter.EntryTransition {
	prevEff = normalizeConfigForCompare(prevEff)
	prevRaw = normalizeConfigForCompare(prevRaw)
	nextEff = normalizeConfigForCompare(nextEff)
	nextRaw = normalizeConfigForCompare(nextRaw)
	if prevEff == nil {
		prevEff = prevRaw
	}
	if prevRaw == nil {
		prevRaw = prevEff
	}
	if nextEff == nil {
		nextEff = nextRaw
	}
	if nextRaw == nil {
		nextRaw = nextEff
	}
	if prevEff == nil || nextEff == nil {
		return nil
	}
	if prevFiles == nil {
		prevFiles = captureLoadedAccessFiles(prevEff)
	}
	if nextFiles == nil {
		nextFiles = captureLoadedAccessFiles(nextEff)
	}

	changedPlugins := make(map[string]bool, len(pluginChanges))
	for _, ch := range pluginChanges {
		changedPlugins[ch.Name] = true
	}
	for name, bDig := range prevFiles.pluginDigestByName {
		if nextFiles.pluginDigestByName[name] != bDig {
			changedPlugins[name] = true
		}
	}
	for name, aDig := range nextFiles.pluginDigestByName {
		if prevFiles.pluginDigestByName[name] != aDig {
			changedPlugins[name] = true
		}
	}

	pairs := correlateConfigEntries(prevRaw, nextRaw)
	hostBefore, hostAfter := computeHostDispatchAffected(prevEff, nextEff)
	blockStructAfter := computeBlockStructChanged(prevEff, nextEff)

	var transitions []respwriter.EntryTransition
	for _, pair := range pairs {
		if pair.before != nil && pair.after == nil {
			b := pair.before
			bEffSrv, _ := effectiveSrvLoc(prevEff, b.srvIdx, b.locIdx, b.srv, b.loc)
			closeCode := respwriter.WSClosePolicyViolation
			if !listenerExistsInConfig(nextEff, bEffSrv.Listen) {
				closeCode = respwriter.WSCloseGoingAway
			}
			transitions = append(transitions, respwriter.EntryTransition{
				FromServerIndex:   b.srvIdx,
				FromLocationIndex: b.locIdx,
				FromRouteID:       literalRouteID(b.loc),
				Removed:           true,
				Retire:            true,
				Mandatory:         true,
				CloseCode:         closeCode,
				DisplayLabel:      formatEntryDisplayLabel(b.srv, b.loc),
			})
			continue
		}
		if pair.before == nil {
			continue
		}

		b := pair.before
		a := pair.after
		bEffSrv, bEffLoc := effectiveSrvLoc(prevEff, b.srvIdx, b.locIdx, b.srv, b.loc)
		aEffSrv, aEffLoc := effectiveSrvLoc(nextEff, a.srvIdx, a.locIdx, a.srv, a.loc)

		classes, targetChanged := classifyPublishPair(
			prevEff, nextEff,
			prevFiles, nextFiles,
			b.srvIdx, b.locIdx, bEffSrv, bEffLoc,
			a.srvIdx, a.locIdx, aEffSrv, aEffLoc,
			hostBefore[b.srvIdx] || hostAfter[a.srvIdx],
			blockStructAfter[a.srvIdx],
			changedPlugins,
		)

		hasNarrowed := slices.Contains(classes, "narrowed") || aEffLoc.Withdrawn || aEffLoc.Deny
		hasUncertain := slices.Contains(classes, "uncertain") || targetChanged
		triggersRetirement := hasNarrowed || hasUncertain

		// Point 1 (ADR 0026 #642 amendment): keep_open_entries eligibility is
		// defined from constituent reasons, never from combined class alone. Any
		// mandatory tightening (narrowed) or withdrawal overrides keep_open_entries.
		keepOpenEligible := triggersRetirement && !hasNarrowed
		wantKeepOpen := matchesKeepOpenTarget(keepOpenEntries, a.srvIdx, a.locIdx, a.srv, a.loc)

		retire := false
		keepOpen := false
		if triggersRetirement {
			if keepOpenEligible && wantKeepOpen {
				keepOpen = true
			} else {
				retire = true
			}
		}
		closeCode := respwriter.WSClosePolicyViolation
		if bEffSrv.Listen != aEffSrv.Listen && !listenerExistsInConfig(nextEff, bEffSrv.Listen) {
			closeCode = respwriter.WSCloseGoingAway
		}

		transitions = append(transitions, respwriter.EntryTransition{
			FromServerIndex:   b.srvIdx,
			FromLocationIndex: b.locIdx,
			FromRouteID:       literalRouteID(b.loc),
			ToServerIndex:     a.srvIdx,
			ToLocationIndex:   a.locIdx,
			ToRouteID:         literalRouteID(a.loc),
			Retire:            retire,
			Mandatory:         hasNarrowed,
			KeepOpen:          keepOpen,
			CloseCode:         closeCode,
			DisplayLabel:      formatEntryDisplayLabel(a.srv, a.loc),
		})
	}
	return transitions
}

func listenerExistsInConfig(c *config.Config, listen string) bool {
	if c == nil || strings.TrimSpace(listen) == "" {
		return false
	}
	for _, s := range c.Servers {
		if s.Listen == listen {
			return true
		}
	}
	return false
}

func matchesKeepOpenTarget(keepOpen []string, srvIdx, locIdx int, srv config.ServerConfig, loc config.LocationConfig) bool {
	if len(keepOpen) == 0 {
		return false
	}
	rid := literalRouteID(loc)
	coordStr := fmt.Sprintf("servers[%d].locations[%d]", srvIdx, locIdx)
	listenPath := fmt.Sprintf("%s %s", srv.Listen, loc.Match.Path)
	for _, item := range keepOpen {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if (rid != "" && trimmed == rid) || trimmed == coordStr || trimmed == listenPath {
			return true
		}
	}
	return false
}

func effectiveSrvLoc(c *config.Config, srvIdx, locIdx int, fallbackSrv config.ServerConfig, fallbackLoc config.LocationConfig) (config.ServerConfig, config.LocationConfig) {
	if c == nil || srvIdx < 0 || srvIdx >= len(c.Servers) {
		return fallbackSrv, fallbackLoc
	}
	srv := c.Servers[srvIdx]
	if locIdx < 0 || locIdx >= len(srv.Locations) {
		return srv, fallbackLoc
	}
	return srv, srv.Locations[locIdx]
}

func classifyPublishPair(
	beforeEff, afterEff *config.Config,
	beforeFiles, afterFiles *LoadedAccessFiles,
	bSrvIdx, bLocIdx int, bSrv config.ServerConfig, bLoc config.LocationConfig,
	aSrvIdx, aLocIdx int, aSrv config.ServerConfig, aLoc config.LocationConfig,
	hostDispatchAffected bool,
	blockStructureChanged bool,
	changedPlugins map[string]bool,
) ([]string, bool) {
	var classes []string
	targetChanged := false
	record := func(cls string) {
		if cls != "" && cls != "unchanged" {
			classes = append(classes, cls)
		}
	}

	if hostDispatchAffected {
		record("uncertain")
	}
	if blockStructureChanged {
		record("uncertain")
	}
	if bSrv.Listen != aSrv.Listen {
		record(classifyPublishBindChange(bSrv.Listen, aSrv.Listen))
	}

	// Stage-0 withdrawal gate.
	if bLoc.Withdrawn != aLoc.Withdrawn {
		if aLoc.Withdrawn {
			record("narrowed")
		} else {
			record("widened")
		}
	}

	// Listener TLS, client_auth, CA/CRL file contents, client_address, proxy_protocol, redirect_https.
	for _, cls := range classifyPublishListenerAndCert(beforeEff, afterEff, beforeFiles, afterFiles, bSrv, aSrv, bLoc, aLoc) {
		record(cls)
	}

	// Match, predicates, rewrites, plugins, CORS.
	if publishLocationKey(&bLoc) != publishLocationKey(&aLoc) {
		record("uncertain")
	}
	if !reflect.DeepEqual(bLoc.Rewrites, aLoc.Rewrites) {
		record("uncertain")
	}
	if !publishPluginsEqual(beforeEff, afterEff, bSrv, aSrv, bLoc, aLoc, changedPlugins) {
		record("uncertain")
	}
	record(classifyPublishCORS(bLoc.CORS, aLoc.CORS))

	// Auth (CIDR, credentials, and loaded htpasswd content).
	for _, cls := range classifyPublishAuthWithCoords(beforeFiles, afterFiles, bSrvIdx, bLocIdx, aSrvIdx, aLocIdx, bLoc.Auth, aLoc.Auth) {
		record(cls)
	}

	// WAF policy and loaded rule files.
	record(classifyPublishWAF(beforeEff, afterEff, beforeFiles, afterFiles, bSrvIdx, bLocIdx, aSrvIdx, aLocIdx, bLoc.WAF, aLoc.WAF))

	// Action / target / upstream.
	if bLoc.Deny != aLoc.Deny {
		if aLoc.Deny {
			record("narrowed")
		} else {
			record("widened")
		}
	} else if !publishTargetAndUpstreamEqual(beforeEff, afterEff, bLoc, aLoc) {
		targetChanged = true
		record("uncertain")
	}

	return classes, targetChanged
}

type publishIndexedLoc struct {
	srvIdx int
	locIdx int
	srv    config.ServerConfig
	loc    config.LocationConfig
}

type publishLocPair struct {
	before *publishIndexedLoc
	after  *publishIndexedLoc
}

func literalRouteID(loc config.LocationConfig) string {
	if loc.RouteID == nil {
		return ""
	}
	s := strings.TrimSpace(*loc.RouteID)
	if s == "" || config.ContainsSecretRef(s) {
		return ""
	}
	return s
}

func formatEntryDisplayLabel(srv config.ServerConfig, loc config.LocationConfig) string {
	hostLabel := srv.Listen
	if len(srv.ServerNames) > 0 && strings.TrimSpace(srv.ServerNames[0]) != "" {
		hostLabel = strings.TrimSpace(srv.ServerNames[0])
		if len(srv.ServerNames) > 1 {
			hostLabel = fmt.Sprintf("%s (+%d)", hostLabel, len(srv.ServerNames)-1)
		}
	} else if strings.TrimSpace(srv.Name) != "" {
		hostLabel = strings.TrimSpace(srv.Name)
	}
	path := loc.Match.Path
	if path == "" {
		path = "/"
	}
	if rid := literalRouteID(loc); rid != "" {
		return fmt.Sprintf("%s %s (%s)", hostLabel, path, rid)
	}
	return fmt.Sprintf("%s %s", hostLabel, path)
}

func publishLocationKey(l *config.LocationConfig) string {
	t := l.Match.Type
	if t == "" {
		t = "prefix"
	}
	return t + " " + l.Match.Path + "\x00" + l.Match.CanonicalPredicates()
}

func correlateConfigEntries(before, after *config.Config) []publishLocPair {
	var bList, aList []publishIndexedLoc
	for i, srv := range before.Servers {
		for j, loc := range srv.Locations {
			bList = append(bList, publishIndexedLoc{srvIdx: i, locIdx: j, srv: srv, loc: loc})
		}
	}
	for i, srv := range after.Servers {
		for j, loc := range srv.Locations {
			aList = append(aList, publishIndexedLoc{srvIdx: i, locIdx: j, srv: srv, loc: loc})
		}
	}
	bByID := make(map[string]int)
	for idx, item := range bList {
		if id := literalRouteID(item.loc); id != "" {
			bByID[id] = idx
		}
	}
	serverBlockMap := correlatePublishServerBlocks(before.Servers, after.Servers)
	usedBefore := make(map[int]bool)
	usedAfter := make(map[int]bool)
	var pairs []publishLocPair

	for aIdx := range aList {
		aItem := &aList[aIdx]
		if id := literalRouteID(aItem.loc); id != "" {
			if bIdx, ok := bByID[id]; ok && !usedBefore[bIdx] {
				usedBefore[bIdx] = true
				usedAfter[aIdx] = true
				bItem := bList[bIdx]
				aCopy := *aItem
				pairs = append(pairs, publishLocPair{before: &bItem, after: &aCopy})
			}
			continue
		}
		for bIdx := range bList {
			if usedBefore[bIdx] {
				continue
			}
			bItem := &bList[bIdx]
			if literalRouteID(bItem.loc) != "" {
				continue
			}
			if mappedAfter, ok := serverBlockMap[bItem.srvIdx]; ok && mappedAfter == aItem.srvIdx {
				if publishLocationKey(&bItem.loc) == publishLocationKey(&aItem.loc) {
					usedBefore[bIdx] = true
					usedAfter[aIdx] = true
					bCopy := *bItem
					aCopy := *aItem
					pairs = append(pairs, publishLocPair{before: &bCopy, after: &aCopy})
					break
				}
			}
		}
	}
	for aIdx := range aList {
		if !usedAfter[aIdx] {
			aCopy := aList[aIdx]
			pairs = append(pairs, publishLocPair{after: &aCopy})
		}
	}
	for bIdx := range bList {
		if !usedBefore[bIdx] {
			bCopy := bList[bIdx]
			pairs = append(pairs, publishLocPair{before: &bCopy})
		}
	}
	return pairs
}

func correlatePublishServerBlocks(before, after []config.ServerConfig) map[int]int {
	out := make(map[int]int)
	usedAfter := make(map[int]bool)
	for bi, bs := range before {
		match := -1
		for ai, as := range after {
			if usedAfter[ai] {
				continue
			}
			if bs.Listen == as.Listen && publishStringSetsEqual(bs.ServerNames, as.ServerNames) {
				if match >= 0 {
					match = -2
					break
				}
				match = ai
			}
		}
		if match >= 0 {
			out[bi] = match
			usedAfter[match] = true
		}
	}
	for bi, bs := range before {
		if _, ok := out[bi]; ok || strings.TrimSpace(bs.Name) == "" {
			continue
		}
		match := -1
		for ai, as := range after {
			if usedAfter[ai] {
				continue
			}
			if bs.Name == as.Name {
				if match >= 0 {
					match = -2
					break
				}
				match = ai
			}
		}
		if match >= 0 {
			out[bi] = match
			usedAfter[match] = true
		}
	}
	for bi, bs := range before {
		if _, ok := out[bi]; ok {
			continue
		}
		match := -1
		for _, bloc := range bs.Locations {
			id := literalRouteID(bloc)
			if id == "" {
				continue
			}
			for ai, as := range after {
				if usedAfter[ai] {
					continue
				}
				for _, aloc := range as.Locations {
					if literalRouteID(aloc) == id {
						match = ai
						break
					}
				}
				if match >= 0 {
					break
				}
			}
			if match >= 0 {
				break
			}
		}
		if match >= 0 {
			out[bi] = match
			usedAfter[match] = true
		}
	}
	for bi, bs := range before {
		if _, ok := out[bi]; ok {
			continue
		}
		var bCount, aCount, aIdx int
		for _, other := range before {
			if other.Listen == bs.Listen {
				bCount++
			}
		}
		for ai, as := range after {
			if as.Listen == bs.Listen {
				aCount++
				aIdx = ai
			}
		}
		if bCount == 1 && aCount == 1 && !usedAfter[aIdx] {
			out[bi] = aIdx
			usedAfter[aIdx] = true
		}
	}
	return out
}

func publishStringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

func computeHostDispatchAffected(before, after *config.Config) (map[int]bool, map[int]bool) {
	affectedBefore := make(map[int]bool)
	affectedAfter := make(map[int]bool)
	bToA := correlatePublishServerBlocks(before.Servers, after.Servers)
	aToB := make(map[int]int, len(bToA))
	for b, a := range bToA {
		aToB[a] = b
	}
	listeners := make(map[string]bool)
	for _, s := range before.Servers {
		listeners[s.Listen] = true
	}
	for _, s := range after.Servers {
		listeners[s.Listen] = true
	}
	for listen := range listeners {
		var bIdxs, aIdxs []int
		for i, s := range before.Servers {
			if s.Listen == listen {
				bIdxs = append(bIdxs, i)
			}
		}
		for i, s := range after.Servers {
			if s.Listen == listen {
				aIdxs = append(aIdxs, i)
			}
		}
		changedSet := make(map[string]bool)
		addPatterns := func(names []string) {
			if config.IsCatchAllServerNames(names) {
				changedSet["*"] = true
				return
			}
			for _, n := range names {
				if norm := config.NormalizeHostPattern(n); norm != "" {
					changedSet[norm] = true
				}
			}
		}
		var bCorrelatedOnListen, aCorrelatedOnListen []int
		for _, bi := range bIdxs {
			ai, ok := bToA[bi]
			if !ok || after.Servers[ai].Listen != listen {
				addPatterns(before.Servers[bi].ServerNames)
				affectedBefore[bi] = true
				continue
			}
			bCorrelatedOnListen = append(bCorrelatedOnListen, bi)
			for _, p := range publishSymmetricHostDiff(before.Servers[bi].ServerNames, after.Servers[ai].ServerNames) {
				changedSet[p] = true
			}
		}
		for _, ai := range aIdxs {
			bi, ok := aToB[ai]
			if !ok || before.Servers[bi].Listen != listen {
				addPatterns(after.Servers[ai].ServerNames)
				affectedAfter[ai] = true
				continue
			}
			aCorrelatedOnListen = append(aCorrelatedOnListen, ai)
		}
		for x := 0; x < len(bCorrelatedOnListen); x++ {
			for y := x + 1; y < len(bCorrelatedOnListen); y++ {
				bi1, bi2 := bCorrelatedOnListen[x], bCorrelatedOnListen[y]
				ai1, ai2 := bToA[bi1], bToA[bi2]
				pos1 := slices.Index(aCorrelatedOnListen, ai1)
				pos2 := slices.Index(aCorrelatedOnListen, ai2)
				if pos1 > pos2 {
					addPatterns(before.Servers[bi1].ServerNames)
					addPatterns(before.Servers[bi2].ServerNames)
					addPatterns(after.Servers[ai1].ServerNames)
					addPatterns(after.Servers[ai2].ServerNames)
					affectedBefore[bi1] = true
					affectedBefore[bi2] = true
					affectedAfter[ai1] = true
					affectedAfter[ai2] = true
				}
			}
		}
		if len(bIdxs) > 0 && len(aIdxs) > 0 {
			firstB := bIdxs[0]
			firstA := aIdxs[0]
			if mappedA, ok := bToA[firstB]; !ok || mappedA != firstA {
				addPatterns(before.Servers[firstB].ServerNames)
				addPatterns(after.Servers[firstA].ServerNames)
				affectedBefore[firstB] = true
				affectedAfter[firstA] = true
			}
		}
		if len(changedSet) == 0 {
			continue
		}
		var changed []string
		for k := range changedSet {
			changed = append(changed, k)
		}
		for _, bi := range bIdxs {
			if bIdxs[0] == bi || publishBlockOverlaps(before.Servers[bi].ServerNames, changed) {
				affectedBefore[bi] = true
				if ai, ok := bToA[bi]; ok {
					affectedAfter[ai] = true
				}
			}
		}
		for _, ai := range aIdxs {
			if aIdxs[0] == ai || publishBlockOverlaps(after.Servers[ai].ServerNames, changed) {
				affectedAfter[ai] = true
				if bi, ok := aToB[ai]; ok {
					affectedBefore[bi] = true
				}
			}
		}
	}
	return affectedBefore, affectedAfter
}

func publishSymmetricHostDiff(bNames, aNames []string) []string {
	if config.IsCatchAllServerNames(bNames) != config.IsCatchAllServerNames(aNames) {
		out := []string{"*"}
		for _, n := range append(append([]string(nil), bNames...), aNames...) {
			if norm := config.NormalizeHostPattern(n); norm != "" {
				out = append(out, norm)
			}
		}
		return out
	}
	bSet := make(map[string]bool, len(bNames))
	for _, n := range bNames {
		if norm := config.NormalizeHostPattern(n); norm != "" {
			bSet[norm] = true
		}
	}
	aSet := make(map[string]bool, len(aNames))
	for _, n := range aNames {
		if norm := config.NormalizeHostPattern(n); norm != "" {
			aSet[norm] = true
		}
	}
	var diff []string
	for k := range bSet {
		if !aSet[k] {
			diff = append(diff, k)
		}
	}
	for k := range aSet {
		if !bSet[k] {
			diff = append(diff, k)
		}
	}
	return diff
}

func publishBlockOverlaps(blockNames, changed []string) bool {
	if len(changed) == 0 {
		return false
	}
	if config.IsCatchAllServerNames(blockNames) {
		return true
	}
	for _, bn := range blockNames {
		for _, ch := range changed {
			if config.HostPatternsOverlap(bn, ch) {
				return true
			}
		}
	}
	return false
}

func computeBlockStructChanged(before, after *config.Config) map[int]bool {
	changed := make(map[int]bool)
	bToA := correlatePublishServerBlocks(before.Servers, after.Servers)
	for bi, ai := range bToA {
		bs := before.Servers[bi]
		as := after.Servers[ai]
		if len(bs.Locations) != len(as.Locations) {
			changed[ai] = true
			continue
		}
		for j := range bs.Locations {
			if literalRouteID(bs.Locations[j]) != literalRouteID(as.Locations[j]) ||
				publishLocationKey(&bs.Locations[j]) != publishLocationKey(&as.Locations[j]) {
				changed[ai] = true
				break
			}
		}
	}
	return changed
}

func classifyPublishBindChange(beforeListen, afterListen string) string {
	if beforeListen == afterListen {
		return "unchanged"
	}
	bHost, bPort, err1 := net.SplitHostPort(strings.TrimSpace(beforeListen))
	aHost, aPort, err2 := net.SplitHostPort(strings.TrimSpace(afterListen))
	if err1 != nil || err2 != nil || bPort != aPort || bHost == "" || aHost == "" {
		return "uncertain"
	}
	bAddr, err1 := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(bHost, "["), "]"))
	aAddr, err2 := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(aHost, "["), "]"))
	if err1 != nil || err2 != nil {
		return "uncertain"
	}
	bAddr = bAddr.Unmap()
	aAddr = aAddr.Unmap()
	if bAddr.Is4() && aAddr.Is4() {
		if !bAddr.IsUnspecified() && aAddr.IsUnspecified() {
			return "widened"
		}
		if bAddr.IsUnspecified() && !aAddr.IsUnspecified() {
			return "narrowed"
		}
	}
	if bAddr.Is6() && aAddr.Is6() {
		if !bAddr.IsUnspecified() && aAddr.IsUnspecified() {
			return "widened"
		}
		if bAddr.IsUnspecified() && !aAddr.IsUnspecified() {
			return "narrowed"
		}
	}
	return "uncertain"
}

func classifyPublishListenerAndCert(
	beforeEff, afterEff *config.Config,
	beforeFiles, afterFiles *LoadedAccessFiles,
	bSrv, aSrv config.ServerConfig,
	bLoc, aLoc config.LocationConfig,
) []string {
	var out []string
	bTLS := listenerTLSEnabled(beforeEff, bSrv.Listen)
	aTLS := listenerTLSEnabled(afterEff, aSrv.Listen)
	if bTLS != aTLS || bSrv.H2C != aSrv.H2C || !reflect.DeepEqual(bSrv.HTTP3, aSrv.HTTP3) {
		out = append(out, "uncertain")
	}
	if bSrv.RedirectHTTPS != aSrv.RedirectHTTPS {
		out = append(out, "uncertain")
	}
	if bSrv.ProxyProtocol != aSrv.ProxyProtocol || !reflect.DeepEqual(bSrv.ClientAddress, aSrv.ClientAddress) {
		out = append(out, "uncertain")
	}

	bMode, bMeta := summarizePublishClientAuth(beforeEff, bSrv.Listen)
	aMode, aMeta := summarizePublishClientAuth(afterEff, aSrv.Listen)
	bCA := beforeFiles.caDigestByAddr[bSrv.Listen]
	aCA := afterFiles.caDigestByAddr[aSrv.Listen]
	crlClass := classifyCRLContentChange(beforeFiles.crlsByAddr[bSrv.Listen], afterFiles.crlsByAddr[aSrv.Listen])

	if aMode != "none" && crlClass != "unchanged" {
		out = append(out, crlClass)
	}
	if bMode != "none" && aMode != "none" && (bMeta != aMeta || bCA != aCA) {
		out = append(out, "uncertain")
	}
	if bMode != aMode {
		switch {
		case aMode == "require":
			out = append(out, "narrowed")
		case bMode == "require" && aMode == "none":
			out = append(out, "widened")
		case bMode == "require" && aMode == "request":
			if !aLoc.RequireClientCert {
				out = append(out, "widened")
			}
		default:
			if aMode == "request" && aLoc.RequireClientCert && bMode == "none" {
				out = append(out, "narrowed")
			} else if bMode == "request" && bLoc.RequireClientCert && aMode == "none" {
				out = append(out, "widened")
			} else {
				out = append(out, "uncertain")
			}
		}
	} else if bLoc.RequireClientCert != aLoc.RequireClientCert && aMode == "request" {
		if aLoc.RequireClientCert {
			out = append(out, "narrowed")
		} else {
			out = append(out, "widened")
		}
	}
	return out
}

func listenerTLSEnabled(c *config.Config, listen string) bool {
	for _, s := range c.Servers {
		if s.Listen == listen && s.TLS != nil && s.TLS.Enabled {
			return true
		}
	}
	return false
}

func summarizePublishClientAuth(c *config.Config, listen string) (string, string) {
	mode := ""
	var sans, fwd []string
	for _, s := range c.Servers {
		if s.Listen != listen || s.TLS == nil || !s.TLS.Enabled || s.TLS.ClientAuth == nil || !s.TLS.ClientAuth.Active() {
			continue
		}
		m := strings.TrimSpace(s.TLS.ClientAuth.Mode)
		if m == "require" || mode == "" {
			mode = m
		}
		for _, san := range s.TLS.ClientAuth.VerifySAN {
			sans = append(sans, strings.ToLower(strings.TrimSpace(san)))
		}
		fwd = append(fwd, strings.ToLower(strings.TrimSpace(s.TLS.ClientAuth.ForwardCertificate)))
	}
	if mode == "" {
		mode = "none"
	}
	sort.Strings(sans)
	sort.Strings(fwd)
	return mode, fmt.Sprintf("san=%v|fwd=%v", sans, fwd)
}

func publishPluginsEqual(
	beforeEff, afterEff *config.Config,
	bSrv, aSrv config.ServerConfig,
	bLoc, aLoc config.LocationConfig,
	changedPlugins map[string]bool,
) bool {
	if !slices.Equal(bSrv.Plugins, aSrv.Plugins) || !slices.Equal(bLoc.Plugins, aLoc.Plugins) || bLoc.Plugin != aLoc.Plugin {
		return false
	}
	all := append(append([]string(nil), aSrv.Plugins...), aLoc.Plugins...)
	if aLoc.Plugin != "" {
		all = append(all, aLoc.Plugin)
	}
	for _, name := range all {
		if changedPlugins[name] {
			return false
		}
		bp, bok := beforeEff.Plugins[name]
		ap, aok := afterEff.Plugins[name]
		if bok != aok || !reflect.DeepEqual(bp, ap) {
			return false
		}
	}
	return true
}

func classifyPublishCORS(b, a *config.CORSConfig) string {
	bOn := b != nil && b.Enabled
	aOn := a != nil && a.Enabled
	switch {
	case !bOn && !aOn:
		return "unchanged"
	case !bOn && aOn:
		return "widened"
	case bOn && !aOn:
		return "narrowed"
	}
	if !publishStringSetsEqual(b.AllowedMethods, a.AllowedMethods) || !publishStringSetsEqual(b.AllowedHeaders, a.AllowedHeaders) {
		return "uncertain"
	}
	bOrigins := make(map[string]bool, len(b.AllowedOrigins))
	for _, o := range b.AllowedOrigins {
		bOrigins[strings.TrimSpace(o)] = true
	}
	aOrigins := make(map[string]bool, len(a.AllowedOrigins))
	for _, o := range a.AllowedOrigins {
		aOrigins[strings.TrimSpace(o)] = true
	}
	added, removed := false, false
	for o := range aOrigins {
		if !bOrigins[o] {
			added = true
		}
	}
	for o := range bOrigins {
		if !aOrigins[o] {
			removed = true
		}
	}
	if !added && !removed && b.AllowCredentials == a.AllowCredentials {
		return "unchanged"
	}
	if added && removed {
		return "uncertain"
	}
	if removed && !added && (b.AllowCredentials == a.AllowCredentials || (b.AllowCredentials && !a.AllowCredentials)) {
		return "narrowed"
	}
	if !added && !removed && b.AllowCredentials && !a.AllowCredentials {
		return "narrowed"
	}
	if added && !removed {
		return "widened"
	}
	if !b.AllowCredentials && a.AllowCredentials && !removed {
		return "widened"
	}
	return "uncertain"
}

func classifyPublishAuth(beforeFiles, afterFiles *LoadedAccessFiles, b, a *config.AuthConfig) []string {
	return classifyPublishAuthWithCoords(beforeFiles, afterFiles, 0, 0, 0, 0, b, a)
}

func classifyPublishAuthWithCoords(beforeFiles, afterFiles *LoadedAccessFiles, bSrvIdx, bLocIdx, aSrvIdx, aLocIdx int, b, a *config.AuthConfig) []string {
	var out []string
	var bAllow, bDeny, aAllow, aDeny []string
	if b != nil {
		bAllow, bDeny = b.Allow, b.Deny
	}
	if a != nil {
		aAllow, aDeny = a.Allow, a.Deny
	}
	if cls := classifyPublishCIDR(bAllow, bDeny, aAllow, aDeny); cls != "unchanged" {
		out = append(out, cls)
	}
	bBasic := b != nil && b.Basic != nil
	bJWT := b != nil && b.JWT != nil
	bFwd := b != nil && b.ForwardAuth != nil
	aBasic := a != nil && a.Basic != nil
	aJWT := a != nil && a.JWT != nil
	aFwd := a != nil && a.ForwardAuth != nil
	bAny := bBasic || bJWT || bFwd
	aAny := aBasic || aJWT || aFwd
	switch {
	case !bAny && !aAny:
	case !bAny && aAny:
		out = append(out, "narrowed")
	case bAny && !aAny:
		out = append(out, "widened")
	case bBasic != aBasic || bJWT != aJWT || bFwd != aFwd:
		out = append(out, "uncertain")
	case bBasic && aBasic:
		bFile := strings.TrimSpace(b.Basic.File)
		aFile := strings.TrimSpace(a.Basic.File)
		if bFile != aFile {
			out = append(out, "uncertain")
		} else if bFile != "" && beforeFiles != nil && afterFiles != nil {
			bSnap := beforeFiles.htpasswdSnapshotFor(bSrvIdx, bLocIdx, bFile)
			aSnap := afterFiles.htpasswdSnapshotFor(aSrvIdx, aLocIdx, aFile)
			if cls := classifyHtpasswdContentChange(bSnap, aSnap); cls != "unchanged" {
				out = append(out, cls)
			}
		}
	case bJWT && aJWT:
		if !reflect.DeepEqual(b.JWT, a.JWT) {
			out = append(out, "uncertain")
		}
	case bFwd && aFwd:
		if !reflect.DeepEqual(b.ForwardAuth, a.ForwardAuth) {
			out = append(out, "uncertain")
		}
	}
	return out
}

func classifyPublishCIDR(bAllow, bDeny, aAllow, aDeny []string) string {
	cls, _ := config.ClassifyCIDRChange(bAllow, bDeny, aAllow, aDeny, false)
	return cls
}

func classifyPublishWAF(
	beforeEff, afterEff *config.Config,
	beforeFiles, afterFiles *LoadedAccessFiles,
	bSrvIdx, bLocIdx, aSrvIdx, aLocIdx int,
	bLocWAF, aLocWAF *config.WAFConfig,
) string {
	bw := beforeEff.WAF
	if bLocWAF != nil {
		bw = *bLocWAF
	}
	aw := afterEff.WAF
	if aLocWAF != nil {
		aw = *aLocWAF
	}
	if !bw.Enabled && !aw.Enabled {
		return "unchanged"
	}
	if bw.Enabled {
		if strings.TrimSpace(bw.Mode) == "" {
			bw.Mode = "block"
		}
		if bw.BlockStatus == 0 {
			bw.BlockStatus = 403
		}
		if bw.RequestBodyLimit == 0 {
			bw.RequestBodyLimit = config.Size(128 << 10)
		}
	}
	if aw.Enabled {
		if strings.TrimSpace(aw.Mode) == "" {
			aw.Mode = "block"
		}
		if aw.BlockStatus == 0 {
			aw.BlockStatus = 403
		}
		if aw.RequestBodyLimit == 0 {
			aw.RequestBodyLimit = config.Size(128 << 10)
		}
	}

	bDig := beforeFiles.globalWAFDigest
	if bLocWAF != nil {
		bDig = beforeFiles.wafDigestByLoc[[2]int{bSrvIdx, bLocIdx}]
	}
	aDig := afterFiles.globalWAFDigest
	if aLocWAF != nil {
		aDig = afterFiles.wafDigestByLoc[[2]int{aSrvIdx, aLocIdx}]
	}
	if bw.Enabled && aw.Enabled && bDig != aDig {
		return "uncertain"
	}
	if bw.Enabled && aw.Enabled &&
		(bw.CRSEnabled != aw.CRSEnabled || bw.Paranoia != aw.Paranoia || bw.BlockStatus != aw.BlockStatus ||
			bw.RequestBodyLimit != aw.RequestBodyLimit || bw.ResponseBodyCheck != aw.ResponseBodyCheck ||
			!slices.Equal(bw.DirectivesFiles, aw.DirectivesFiles) || strings.TrimSpace(bw.InlineRules) != strings.TrimSpace(aw.InlineRules)) {
		return "uncertain"
	}
	bMode := "disabled"
	if bw.Enabled {
		bMode = strings.TrimSpace(bw.Mode)
	}
	aMode := "disabled"
	if aw.Enabled {
		aMode = strings.TrimSpace(aw.Mode)
	}
	if bMode == aMode {
		return "unchanged"
	}
	noEngineCtrl := len(bw.DirectivesFiles) == 0 && len(aw.DirectivesFiles) == 0 &&
		!strings.Contains(strings.ToLower(bw.InlineRules), "ctl:ruleengine") &&
		!strings.Contains(strings.ToLower(aw.InlineRules), "ctl:ruleengine") &&
		!strings.Contains(strings.ToLower(bw.InlineRules), "include ") &&
		!strings.Contains(strings.ToLower(aw.InlineRules), "include ")
	switch {
	case bMode == "disabled" && aMode == "block":
		return "narrowed"
	case bMode == "block" && aMode == "disabled":
		return "widened"
	case noEngineCtrl && ((bMode == "disabled" && aMode == "detect") || (bMode == "detect" && aMode == "disabled")):
		return "unchanged"
	case noEngineCtrl && bMode == "detect" && aMode == "block":
		return "narrowed"
	case noEngineCtrl && bMode == "block" && aMode == "detect":
		return "widened"
	case !noEngineCtrl && bMode == "disabled" && aMode == "detect":
		return "narrowed"
	case !noEngineCtrl && bMode == "detect" && aMode == "disabled":
		return "widened"
	default:
		return "uncertain"
	}
}

func publishTargetAndUpstreamEqual(before, after *config.Config, bLoc, aLoc config.LocationConfig) bool {
	if bLoc.Root != aLoc.Root || bLoc.ProxyPass != aLoc.ProxyPass || bLoc.FastCGIPass != aLoc.FastCGIPass ||
		bLoc.UWSGIPass != aLoc.UWSGIPass || bLoc.Redirect != aLoc.Redirect || bLoc.Return != aLoc.Return ||
		bLoc.Deny != aLoc.Deny || bLoc.GRPC != aLoc.GRPC || bLoc.Plugin != aLoc.Plugin ||
		bLoc.DirectoryListing != aLoc.DirectoryListing || bLoc.AllowHidden != aLoc.AllowHidden ||
		!slices.Equal(bLoc.Index, aLoc.Index) || !slices.Equal(bLoc.TryFiles, aLoc.TryFiles) ||
		!maps.Equal(bLoc.FastCGIParams, aLoc.FastCGIParams) ||
		!reflect.DeepEqual(bLoc.GRPCTranscode, aLoc.GRPCTranscode) ||
		!reflect.DeepEqual(bLoc.BackendTLS, aLoc.BackendTLS) {
		return false
	}
	bUp := publishUpstreamName(bLoc)
	aUp := publishUpstreamName(aLoc)
	if bUp != aUp {
		return false
	}
	if aUp != "" {
		var bu, au *config.UpstreamConfig
		for i := range before.Upstreams {
			if before.Upstreams[i].Name == aUp {
				bu = &before.Upstreams[i]
				break
			}
		}
		for i := range after.Upstreams {
			if after.Upstreams[i].Name == aUp {
				au = &after.Upstreams[i]
				break
			}
		}
		if (bu == nil) != (au == nil) {
			return false
		}
		if bu != nil && au != nil {
			if bu.Strategy != au.Strategy || !reflect.DeepEqual(bu.Hash, au.Hash) ||
				!reflect.DeepEqual(bu.BackendTLS, au.BackendTLS) ||
				!slices.Equal(bu.Servers, au.Servers) ||
				!reflect.DeepEqual(bu.Discovery, au.Discovery) {
				return false
			}
		}
	}
	return true
}

func publishUpstreamName(loc config.LocationConfig) string {
	target := strings.TrimSpace(loc.ProxyPass)
	if loc.GRPCTranscode != nil && strings.TrimSpace(loc.GRPCTranscode.Target) != "" {
		target = strings.TrimSpace(loc.GRPCTranscode.Target)
	}
	for _, pfx := range []string{"http://", "https://", "grpc://", "grpcs://"} {
		if strings.HasPrefix(strings.ToLower(target), pfx) {
			host := target[len(pfx):]
			if slash := strings.IndexByte(host, '/'); slash >= 0 {
				host = host[:slash]
			}
			if !strings.Contains(host, ":") && !strings.Contains(host, ".") {
				return host
			}
			return ""
		}
	}
	if target != "" && !strings.Contains(target, ":") && !strings.Contains(target, "/") {
		return target
	}
	return ""
}

// Keep unused bytes import satisfied if needed by future helpers.
var _ = bytes.Equal

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/server"
)

const (
	CurrentSharingViewVersion     = 1
	BoundedDisplayMaxBytes        = 256
	BoundedPathMaxBytes           = 1024
	BoundedURLMaxBytes            = 2048
	SharingSelectorCapBytes       = 4096
	SharingMaxBlockNames          = 100
	SharingMaxBackendGroupMembers = 50
	SharingMaxEntryURLs           = 3
	SharingMaxPageEntries         = 200
	SharingMaxPageBytes           = 1 << 20 // 1 MiB
	SharingMaxValueChunkBytes     = 64 << 10
)

// BoundedText is the single wire type for all display text in the sharing view
// (spec §4.3). It is cut at a UTF-8 boundary when len(s) > limit.
type BoundedText struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	FullBytes int    `json:"full_bytes"`
}

// NewBoundedText constructs a BoundedText cut at a valid UTF-8 boundary at most
// maxBytes long.
func NewBoundedText(s string, maxBytes int) BoundedText {
	full := len(s)
	if maxBytes <= 0 || full <= maxBytes {
		return BoundedText{
			Text:      s,
			Truncated: false,
			FullBytes: full,
		}
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return BoundedText{
		Text:      s[:cut],
		Truncated: true,
		FullBytes: full,
	}
}

// SharingRouteIDView represents entry.route_id: either a literal ADR 0019
// route_id or a reference text marked from_reference (spec §4.3, RR08).
type SharingRouteIDView struct {
	Literal       string       `json:"literal,omitempty"`
	FromReference *BoundedText `json:"from_reference,omitempty"`
}

// MarshalJSON encodes SharingRouteIDView as a bare JSON string when Literal is
// set, or as {"from_reference": ...} when written as a secret reference.
func (r SharingRouteIDView) MarshalJSON() ([]byte, error) {
	if r.FromReference != nil {
		return json.Marshal(struct {
			FromReference BoundedText `json:"from_reference"`
		}{FromReference: *r.FromReference})
	}
	return json.Marshal(r.Literal)
}

// UnmarshalJSON decodes either a literal string or {"from_reference": ...}.
func (r *SharingRouteIDView) UnmarshalJSON(data []byte) error {
	var lit string
	if err := json.Unmarshal(data, &lit); err == nil {
		r.Literal = lit
		r.FromReference = nil
		return nil
	}
	var refObj struct {
		FromReference *BoundedText `json:"from_reference"`
	}
	if err := json.Unmarshal(data, &refObj); err != nil {
		return err
	}
	r.Literal = ""
	r.FromReference = refObj.FromReference
	return nil
}

// SharingSelectorView is entry.selector: either the full coordinates + cross-block
// match_ordinal or {too_large: true, full_bytes} when coordinates exceed 4 KiB
// or the block's names are truncated (spec §4.3, W-5).
type SharingSelectorView struct {
	Block           string `json:"block,omitempty"`
	MatchType       string `json:"match_type,omitempty"`
	Path            string `json:"path,omitempty"`
	MatchOrdinal    int    `json:"match_ordinal"`
	SelectorVersion string `json:"selector_version,omitempty"`
	BaseVersion     string `json:"base_version,omitempty"`
	TooLarge        bool   `json:"too_large,omitempty"`
	FullBytes       int    `json:"full_bytes,omitempty"`
}

// SharingBlockRow is one row of the response-level blocks table (spec §4.3).
type SharingBlockRow struct {
	Key                    string        `json:"key"`
	Layer                  string        `json:"layer"`
	BlockIndex             int           `json:"block_index"`
	Listen                 BoundedText   `json:"listen"`
	DisplayLabel           BoundedText   `json:"display_label"`
	Names                  []BoundedText `json:"names"`
	NameCount              int           `json:"name_count"`
	HasWildcard            bool          `json:"has_wildcard"`
	ReceivesUnmatchedHosts bool          `json:"receives_unmatched_hosts"`
	Truncated              bool          `json:"truncated"`
}

// SharingBackendGroupRow is one advisory backend group row (spec §4.2, §4.3).
type SharingBackendGroupRow struct {
	Key         string   `json:"key"`
	Kind        string   `json:"kind"`
	MemberCount int      `json:"member_count"`
	Members     []string `json:"members"`
	Truncated   bool     `json:"truncated"`
}

// SharingTransportView is listener.transport (spec §4.3).
type SharingTransportView struct {
	TLS        string `json:"tls"`
	ClientAuth string `json:"client_auth"`
	HTTP3      string `json:"http3"`
	H2C        string `json:"h2c"`
}

// SharingListenerView is listener in an entry layer (spec §4.3).
type SharingListenerView struct {
	Address   BoundedText          `json:"address"`
	BindScope string               `json:"bind_scope"`
	Transport SharingTransportView `json:"transport"`
}

// SharingURLItem is one derived URL or template for an entry (spec §4.3).
type SharingURLItem struct {
	Kind  string      `json:"kind"`
	Value BoundedText `json:"value"`
}

// SharingURLsView is urls in an entry layer (spec §4.3).
type SharingURLsView struct {
	Items     []SharingURLItem `json:"items"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated"`
}

// SharingOutsideAuthView reports outer-layer mechanisms that execute before
// location checks (spec §4.3, §9.3).
type SharingOutsideAuthView struct {
	RedirectRewrites bool `json:"redirect_rewrites"`
	RequestPlugins   bool `json:"request_plugins"`
	CORSPreflight    bool `json:"cors_preflight"`
}

// SharingJulChecksView is jul_checks in an entry layer (spec §4.3).
type SharingJulChecksView struct {
	Network           string                 `json:"network"`
	Credential        string                 `json:"credential"`
	ClientCertificate string                 `json:"client_certificate"`
	OutsideAuth       SharingOutsideAuthView `json:"outside_auth"`
}

// SharingControlsView is controls in an entry layer (spec §4.3).
type SharingControlsView struct {
	Cache     string `json:"cache"`
	WAF       string `json:"waf"`
	RateLimit string `json:"rate_limit"`
	CORS      string `json:"cors"`
}

// SharingDeclaredIntentView is declared_intent in an entry layer (spec §4.3, §4.5).
type SharingDeclaredIntentView struct {
	State            string       `json:"state"`
	Mode             string       `json:"mode,omitempty"`
	AudienceLabel    *BoundedText `json:"audience_label,omitempty"`
	ExternalEnforcer *BoundedText `json:"external_enforcer,omitempty"`
}

// SharingFindingView is a warning or intent finding on an entry layer.
type SharingFindingView struct {
	Code     string      `json:"code"`
	Severity string      `json:"severity"`
	Detail   BoundedText `json:"detail"`
}

// SharingEntryLayer is one layer (serving or saved) of an entry (spec §4.3).
type SharingEntryLayer struct {
	Layer                  string                    `json:"layer"`
	Revision               string                    `json:"revision"`
	RouteID                *SharingRouteIDView       `json:"route_id,omitempty"`
	Selector               SharingSelectorView       `json:"selector"`
	Position               SharingEntryPosition      `json:"position"`
	Identity               string                    `json:"identity"`
	ServiceBlock           string                    `json:"service_block"`
	Path                   BoundedText               `json:"path"`
	Listener               SharingListenerView       `json:"listener"`
	URLs                   SharingURLsView           `json:"urls"`
	JulChecks              SharingJulChecksView      `json:"jul_checks"`
	Action                 string                    `json:"action"`
	UpstreamRef            string                    `json:"upstream_ref,omitempty"`
	Controls               SharingControlsView       `json:"controls"`
	Withdrawn              string                    `json:"withdrawn"`
	BackendGroup           string                    `json:"backend_group,omitempty"`
	Warnings               []SharingFindingView      `json:"warnings"`
	WarningsComplete       bool                      `json:"warnings_complete"`
	DeclaredIntent         SharingDeclaredIntentView `json:"declared_intent"`
	IntentFindings         []SharingFindingView      `json:"intent_findings"`
	IntentFindingsComplete bool                      `json:"intent_findings_complete"`
	NextActions            []string                  `json:"next_actions"`
}

// SharingEntryView is one entry in the sharing view, carrying its serving layer
// and, when the saved file differs, its saved layer (spec §4.3, SD-10).
type SharingEntryView struct {
	Key            string             `json:"key"`
	PendingRemoval bool               `json:"pending_removal,omitempty"`
	Serving        *SharingEntryLayer `json:"serving,omitempty"`
	Saved          *SharingEntryLayer `json:"saved,omitempty"`
}

// SharingAuthorityView is the once-per-response authority summary (spec §4.3).
type SharingAuthorityView struct {
	ConfigAuthority string `json:"config_authority"`
	ConfigState     string `json:"config_state"`
	DesiredVersion  string `json:"desired_version,omitempty"`
	ServingVersion  string `json:"serving_version,omitempty"`
}

// SharingViewResponse is the bounded, paged sharing projection response
// (spec §4.3).
type SharingViewResponse struct {
	SharingViewVersion int                      `json:"sharing_view_version"`
	Snapshot           string                   `json:"snapshot"`
	SavedStatus        string                   `json:"saved_status"`
	Authority          SharingAuthorityView     `json:"authority"`
	Blocks             []SharingBlockRow        `json:"blocks"`
	BackendGroups      []SharingBackendGroupRow `json:"backend_groups,omitempty"`
	Entries            []SharingEntryView       `json:"entries"`
	TotalEntries       int                      `json:"total_entries"`
	NextCursor         string                   `json:"next_cursor,omitempty"`
}

// SharingValueResponse is the bounded chunked full-value read response
// (spec §4.3, W-4).
type SharingValueResponse struct {
	Snapshot   string `json:"snapshot"`
	Chunk      string `json:"chunk"`
	Offset     int    `json:"offset"`
	TotalBytes int    `json:"total_bytes"`
	NextOffset *int   `json:"next_offset,omitempty"`
}

type cachedSharingDerivation struct {
	snapshotToken string
	servingRaw    *config.Config
	savedRaw      *config.Config
	savedStatus   string
	authority     SharingAuthorityView
	blocksByKey   map[string]SharingBlockRow
	groupsByKey   map[string]SharingBackendGroupRow
	entries       []SharingEntryView
}

func (s *Server) deriveSharingSnapshot() (*cachedSharingDerivation, error) {
	var snap server.LiveSnapshot
	if s.deps.LiveSnapshot != nil {
		snap = s.deps.LiveSnapshot()
	}
	if snap.RawConfig == nil && s.deps.LoadConfig != nil {
		cfg, err := s.deps.LoadConfig()
		if err != nil {
			return nil, err
		}
		snap = server.LiveSnapshot{
			Generation:      1,
			RawConfig:       cfg,
			EffectiveConfig: cfg,
		}
	}
	if snap.RawConfig == nil || snap.EffectiveConfig == nil {
		return nil, fmt.Errorf("sharing view: serving snapshot is unavailable")
	}

	rawBytes, err := config.Marshal(snap.RawConfig)
	if err != nil {
		return nil, err
	}
	rawSum := sha256.Sum256(rawBytes)
	rawDigest := hex.EncodeToString(rawSum[:8])

	servingVersion := s.servingVersion()
	if servingVersion == "" {
		servingVersion = server.CanonicalVersion(snap.EffectiveConfig)
	}

	authStatus := s.currentAuthority()
	desiredVersion := authStatus.DiskVersion
	if desiredVersion == "" {
		desiredVersion = servingVersion
	}

	var savedRawBytes []byte
	savedRawDigest := "same"
	if s.deps.ReadConfigRaw != nil {
		if diskBytes, readErr := s.deps.ReadConfigRaw(); readErr == nil {
			dSum := sha256.Sum256(diskBytes)
			savedRawDigest = hex.EncodeToString(dSum[:8])
			if !bytesEqualTrimmed(diskBytes, rawBytes) && desiredVersion != servingVersion {
				savedRawBytes = diskBytes
			} else if !bytesEqualTrimmed(diskBytes, rawBytes) {
				// Even if effective versions coincide, check whether parsed raw configs differ.
				savedRawBytes = diskBytes
			}
		} else {
			savedRawDigest = "unreadable"
		}
	}

	snapshotToken := fmt.Sprintf("g%d:%s:%s:%s", snap.Generation, rawDigest, desiredVersion, savedRawDigest)

	s.sharingCacheMu.Lock()
	if s.sharingCache != nil && s.sharingCache.snapshotToken == snapshotToken {
		cached := s.sharingCache
		s.sharingCacheMu.Unlock()
		return cached, nil
	}
	s.sharingCacheMu.Unlock()

	derived := buildSharingDerivation(
		snapshotToken,
		snap.RawConfig,
		snap.EffectiveConfig,
		servingVersion,
		desiredVersion,
		authStatus,
		savedRawBytes,
	)

	s.sharingCacheMu.Lock()
	s.sharingCache = derived
	s.sharingCacheMu.Unlock()
	return derived, nil
}

func bytesEqualTrimmed(a, b []byte) bool {
	return strings.TrimSpace(string(a)) == strings.TrimSpace(string(b))
}

func buildSharingDerivation(
	snapshotToken string,
	servingRaw *config.Config,
	servingEff *config.Config,
	servingVersion string,
	desiredVersion string,
	authStatus ConfigAuthorityStatus,
	savedRawBytes []byte,
) *cachedSharingDerivation {
	blocksByKey := make(map[string]SharingBlockRow)
	groupsByKey := make(map[string]SharingBackendGroupRow)

	servingGroupKeys := buildLayerBackendGroups("serving", servingRaw, groupsByKey)
	servingBlocks, servingLayers := buildLayerEntries(
		"serving",
		servingVersion,
		servingRaw,
		servingEff,
		false,
		servingGroupKeys,
	)
	for _, b := range servingBlocks {
		blocksByKey[b.Key] = b
	}

	savedStatus := "same_as_serving"
	var savedRawCfg *config.Config
	var savedLayers []SharingEntryLayer
	if savedRawDigestDiffers(servingRaw, savedRawBytes) {
		parsed, err := config.Parse(savedRawBytes)
		if err != nil || authStatus.DiskParseError != "" {
			savedStatus = "unavailable"
		} else {
			savedRawCfg = parsed
			savedStatus = "different"
			savedVer := desiredVersion
			if savedVer == "" || savedVer == servingVersion {
				savedVer = server.CanonicalVersion(parsed)
			}
			savedGroupKeys := buildLayerBackendGroups("saved", parsed, groupsByKey)
			var savedBlocks []SharingBlockRow
			savedBlocks, savedLayers = buildLayerEntries(
				"saved",
				savedVer,
				parsed,
				nil,
				true,
				savedGroupKeys,
			)
			for _, b := range savedBlocks {
				blocksByKey[b.Key] = b
			}
		}
	}

	entries := combineServingAndSavedLayers(servingRaw, savedRawCfg, servingLayers, savedLayers, savedStatus == "different")

	return &cachedSharingDerivation{
		snapshotToken: snapshotToken,
		servingRaw:    servingRaw,
		savedRaw:      savedRawCfg,
		savedStatus:   savedStatus,
		authority: SharingAuthorityView{
			ConfigAuthority: authStatus.Mode,
			ConfigState:     authStatus.ConfigState,
			DesiredVersion:  desiredVersion,
			ServingVersion:  servingVersion,
		},
		blocksByKey: blocksByKey,
		groupsByKey: groupsByKey,
		entries:     entries,
	}
}

func savedRawDigestDiffers(servingRaw *config.Config, savedRawBytes []byte) bool {
	if len(savedRawBytes) == 0 || servingRaw == nil {
		return false
	}
	parsed, err := config.Parse(savedRawBytes)
	if err != nil {
		return true
	}
	m1, err1 := config.Marshal(servingRaw)
	m2, err2 := config.Marshal(parsed)
	if err1 != nil || err2 != nil {
		return true
	}
	return !bytesEqualTrimmed(m1, m2)
}

func buildLayerBackendGroups(
	layer string,
	rawCfg *config.Config,
	groupsByKey map[string]SharingBackendGroupRow,
) map[[2]int]string {
	locToGroup := make(map[[2]int]string)
	if rawCfg == nil {
		return locToGroup
	}
	type groupCollector struct {
		kind    string
		target  string
		coords  [][2]int
		members []string
	}
	var order []string
	byID := make(map[string]*groupCollector)

	for i, srv := range rawCfg.Servers {
		for j, loc := range srv.Locations {
			var kind, target string
			switch {
			case loc.GRPCTranscode != nil && strings.TrimSpace(loc.GRPCTranscode.Target) != "":
				up := upstreamRef(loc.GRPCTranscode.Target, "proxy")
				if up != "" {
					kind, target = "upstream", up
				} else {
					kind, target = "address", strings.TrimSpace(loc.GRPCTranscode.Target)
				}
			case loc.ProxyPass != "":
				up := upstreamRef(loc.ProxyPass, "proxy")
				if up != "" {
					kind, target = "upstream", up
				} else {
					kind, target = "address", strings.TrimSpace(loc.ProxyPass)
				}
			case loc.FastCGIPass != "":
				kind, target = "address", "fastcgi:"+strings.TrimSpace(loc.FastCGIPass)
			case loc.UWSGIPass != "":
				kind, target = "address", "uwsgi:"+strings.TrimSpace(loc.UWSGIPass)
			case loc.Root != "":
				kind, target = "static_root", strings.TrimSpace(loc.Root)
			}
			if kind == "" || target == "" || config.ContainsSecretRef(target) {
				continue
			}
			id := kind + ":" + target
			gc, ok := byID[id]
			if !ok {
				gc = &groupCollector{kind: kind, target: target}
				byID[id] = gc
				order = append(order, id)
			}
			gc.coords = append(gc.coords, [2]int{i, j})
			memberID := fmt.Sprintf("servers[%d].locations[%d]", i, j)
			if rid := routeID(&loc); rid != "" && !config.ContainsSecretRef(rid) {
				memberID = rid
			}
			gc.members = append(gc.members, memberID)
		}
	}

	groupIdx := 0
	for _, id := range order {
		gc := byID[id]
		if len(gc.coords) < 2 {
			continue
		}
		key := fmt.Sprintf("%s:bg:%d", layer, groupIdx)
		groupIdx++
		members := gc.members
		truncated := false
		if len(members) > SharingMaxBackendGroupMembers {
			members = members[:SharingMaxBackendGroupMembers]
			truncated = true
		}
		groupsByKey[key] = SharingBackendGroupRow{
			Key:         key,
			Kind:        gc.kind,
			MemberCount: len(gc.members),
			Members:     members,
			Truncated:   truncated,
		}
		for _, c := range gc.coords {
			locToGroup[c] = key
		}
	}
	return locToGroup
}

func buildLayerEntries(
	layer string,
	revision string,
	rawCfg *config.Config,
	effCfg *config.Config,
	isSavedLayer bool,
	groupKeys map[[2]int]string,
) ([]SharingBlockRow, []SharingEntryLayer) {
	if rawCfg == nil {
		return nil, nil
	}
	ordinals := routeOrdinals(rawCfg)
	unmatchedReceiverByBlock := computeReceivesUnmatchedHosts(rawCfg)

	blocks := make([]SharingBlockRow, 0, len(rawCfg.Servers))
	var layers []SharingEntryLayer

	for i, rawSrv := range rawCfg.Servers {
		blockKey := fmt.Sprintf("%s:block:%d", layer, i)
		blockRow, anyNameTruncated := buildSharingBlockRow(blockKey, layer, i, rawSrv, unmatchedReceiverByBlock[i])
		blocks = append(blocks, blockRow)

		var effSrv config.ServerConfig
		if effCfg != nil && i < len(effCfg.Servers) {
			effSrv = effCfg.Servers[i]
		} else {
			effSrv = rawSrv
		}

		for j, rawLoc := range rawSrv.Locations {
			var effLoc config.LocationConfig
			if effCfg != nil && i < len(effCfg.Servers) && j < len(effCfg.Servers[i].Locations) {
				effLoc = effCfg.Servers[i].Locations[j]
			} else {
				effLoc = rawLoc
			}

			el := buildSingleEntryLayer(
				layer,
				revision,
				blockKey,
				i, j,
				ordinals[i][j],
				blockRow.Truncated || anyNameTruncated,
				rawCfg,
				effCfg,
				rawSrv,
				effSrv,
				rawLoc,
				effLoc,
				isSavedLayer,
				groupKeys[[2]int{i, j}],
			)
			layers = append(layers, el)
		}
	}

	return blocks, layers
}

func computeReceivesUnmatchedHosts(c *config.Config) map[int]bool {
	out := make(map[int]bool)
	firstByListen := make(map[string]int)
	for i, srv := range c.Servers {
		if _, seen := firstByListen[srv.Listen]; !seen {
			firstByListen[srv.Listen] = i
		}
		if config.IsCatchAllServerNames(srv.ServerNames) {
			out[i] = true
		}
	}
	for _, firstIdx := range firstByListen {
		out[firstIdx] = true
	}
	return out
}

func buildSharingBlockRow(
	key, layer string,
	blockIdx int,
	rawSrv config.ServerConfig,
	receivesUnmatched bool,
) (SharingBlockRow, bool) {
	labelStr := strings.TrimSpace(rawSrv.Name)
	if labelStr == "" {
		if len(rawSrv.ServerNames) > 0 && strings.TrimSpace(rawSrv.ServerNames[0]) != "" {
			labelStr = strings.TrimSpace(rawSrv.ServerNames[0])
		} else {
			labelStr = rawSrv.Listen
		}
	}
	hasWildcard := len(rawSrv.ServerNames) == 0
	for _, n := range rawSrv.ServerNames {
		norm := config.NormalizeHostPattern(n)
		if norm == "*" || strings.HasPrefix(norm, "*.") {
			hasWildcard = true
			break
		}
	}

	totalNames := len(rawSrv.ServerNames)
	limit := totalNames
	listTruncated := false
	if limit > SharingMaxBlockNames {
		limit = SharingMaxBlockNames
		listTruncated = true
	}
	names := make([]BoundedText, 0, limit)
	anyNameTruncated := false
	for k := 0; k < limit; k++ {
		bt := NewBoundedText(rawSrv.ServerNames[k], BoundedDisplayMaxBytes)
		if bt.Truncated {
			anyNameTruncated = true
		}
		names = append(names, bt)
	}

	return SharingBlockRow{
		Key:                    key,
		Layer:                  layer,
		BlockIndex:             blockIdx,
		Listen:                 NewBoundedText(rawSrv.Listen, BoundedDisplayMaxBytes),
		DisplayLabel:           NewBoundedText(labelStr, BoundedDisplayMaxBytes),
		Names:                  names,
		NameCount:              totalNames,
		HasWildcard:            hasWildcard,
		ReceivesUnmatchedHosts: receivesUnmatched,
		Truncated:              listTruncated,
	}, anyNameTruncated
}

func buildSingleEntryLayer(
	layer string,
	revision string,
	blockKey string,
	srvIdx, locIdx int,
	ordinal int,
	blockNamesTruncated bool,
	rawCfg *config.Config,
	effCfg *config.Config,
	rawSrv config.ServerConfig,
	effSrv config.ServerConfig,
	rawLoc config.LocationConfig,
	effLoc config.LocationConfig,
	isSavedLayer bool,
	backendGroupKey string,
) SharingEntryLayer {
	// 1. route_id and identity (RR08: reference in route_id -> from_reference, revision_bound).
	var ridView *SharingRouteIDView
	identity := "revision_bound"
	if rawLoc.RouteID != nil && *rawLoc.RouteID != "" {
		rawID := *rawLoc.RouteID
		if config.ContainsSecretRef(rawID) {
			bt := NewBoundedText(rawID, BoundedDisplayMaxBytes)
			ridView = &SharingRouteIDView{FromReference: &bt}
			identity = "revision_bound"
		} else {
			ridView = &SharingRouteIDView{Literal: rawID}
			identity = "durable"
		}
	}

	// 2. selector (W-5: too_large when coordinates exceed 4 KiB or block names truncated).
	matchType := rawLoc.Match.Type
	if matchType == "" {
		matchType = "prefix"
	}
	coordBytes := len(rawSrv.Listen) + len(matchType) + len(rawLoc.Match.Path)
	for _, n := range rawSrv.ServerNames {
		coordBytes += len(n)
	}
	var selView SharingSelectorView
	if coordBytes > SharingSelectorCapBytes || blockNamesTruncated {
		selView = SharingSelectorView{
			TooLarge:  true,
			FullBytes: coordBytes,
		}
	} else {
		selView = SharingSelectorView{
			Block:           blockKey,
			MatchType:       matchType,
			Path:            rawLoc.Match.Path,
			MatchOrdinal:    ordinal,
			SelectorVersion: routeSelectorVersion,
			BaseVersion:     revision,
		}
	}

	// 3. listener & transport.
	bindScope := config.BindScope(effSrv.Listen)
	if isSavedLayer && config.ContainsSecretRef(rawSrv.Listen) {
		bindScope = config.BindScopeUnknown
	}
	transport := deriveSharingTransport(rawCfg, effCfg, rawSrv, effSrv, isSavedLayer)

	// 4. urls (concrete / template / none / truncated / withheld).
	urlsView := deriveSharingURLs(rawSrv, rawLoc, transport.TLS == "enabled")

	// 5. jul_checks.
	julChecks, checksUnknown := deriveSharingJulChecks(rawCfg, effCfg, rawSrv, effSrv, rawLoc, effLoc, isSavedLayer)

	// 6. action, upstream_ref, controls, withdrawn.
	act := locationAction(&rawLoc)
	upRef := locationUpstreamName(rawLoc)
	controls := deriveSharingControls(rawCfg, effCfg, rawLoc, effLoc, isSavedLayer)
	withdrawn := "no"
	if isSavedLayer && rawLoc.Auth != nil && hasAnySecretRefInSlice(append(append([]string(nil), rawLoc.Auth.Allow...), rawLoc.Auth.Deny...)) {
		withdrawn = "unknown"
	} else if isLocationWithdrawnByConfig(effLoc) {
		withdrawn = "yes"
	}

	// 7. declared_intent (W2-2: admits unknown in saved layer when written with references).
	intentView, intentUnknown := deriveSharingDeclaredIntent(rawLoc, effLoc, isSavedLayer)

	// 8. warnings & intent_findings.
	evalCfg := effCfg
	if evalCfg == nil {
		evalCfg = rawCfg
	}
	warnings, warningsComplete := deriveSharingEntryWarnings(rawCfg, evalCfg, srvIdx, locIdx, bindScope, transport, julChecks, controls, isSavedLayer)
	if isSavedLayer && (bindScope == config.BindScopeUnknown || checksUnknown || intentUnknown) {
		warningsComplete = false
	}

	rawIntentDiags, intentComplete := config.LintLocationSharingFindingsWithRaw(rawCfg, evalCfg, srvIdx, locIdx, isSavedLayer)
	if isSavedLayer && (bindScope == config.BindScopeUnknown || checksUnknown || intentUnknown) {
		intentComplete = false
	}
	intentFindings := make([]SharingFindingView, 0, len(rawIntentDiags))
	for _, d := range rawIntentDiags {
		intentFindings = append(intentFindings, SharingFindingView{
			Code:     d.Code,
			Severity: d.Severity.String(),
			Detail:   NewBoundedText(d.Message, BoundedDisplayMaxBytes),
		})
	}

	// 9. next_actions (§9.7 closed set).
	nextActions := deriveSharingNextActions(intentView, julChecks, intentFindings)

	return SharingEntryLayer{
		Layer:        layer,
		Revision:     revision,
		RouteID:      ridView,
		Selector:     selView,
		Position:     SharingEntryPosition{BlockIndex: srvIdx, LocationIndex: locIdx},
		Identity:     identity,
		ServiceBlock: blockKey,
		Path:         NewBoundedText(rawLoc.Match.Path, BoundedPathMaxBytes),
		Listener: SharingListenerView{
			Address:   NewBoundedText(rawSrv.Listen, BoundedDisplayMaxBytes),
			BindScope: bindScope,
			Transport: transport,
		},
		URLs:                   urlsView,
		JulChecks:              julChecks,
		Action:                 act,
		UpstreamRef:            upRef,
		Controls:               controls,
		Withdrawn:              withdrawn,
		BackendGroup:           backendGroupKey,
		Warnings:               warnings,
		WarningsComplete:       warningsComplete,
		DeclaredIntent:         intentView,
		IntentFindings:         intentFindings,
		IntentFindingsComplete: intentComplete,
		NextActions:            nextActions,
	}
}

func boolStateString(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func deriveSharingTransport(
	rawCfg, effCfg *config.Config,
	rawSrv, effSrv config.ServerConfig,
	isSaved bool,
) SharingTransportView {
	tlsOn := effSrv.TLS != nil && effSrv.TLS.Enabled
	http3On := effSrv.HTTP3 != nil && effSrv.HTTP3.Enabled
	h2cOn := effSrv.H2C

	cfgForAuth := effCfg
	if cfgForAuth == nil {
		cfgForAuth = rawCfg
	}
	clientAuth := "none"
	for _, ls := range cfgForAuth.Servers {
		if ls.Listen != effSrv.Listen || ls.TLS == nil || !ls.TLS.Enabled || ls.TLS.ClientAuth == nil || !ls.TLS.ClientAuth.Active() {
			continue
		}
		m := strings.TrimSpace(ls.TLS.ClientAuth.Mode)
		if isSaved && config.ContainsSecretRef(m) {
			clientAuth = "unknown"
			break
		}
		if m == "require" || clientAuth == "none" {
			clientAuth = m
		}
	}
	return SharingTransportView{
		TLS:        boolStateString(tlsOn),
		ClientAuth: clientAuth,
		HTTP3:      boolStateString(http3On),
		H2C:        boolStateString(h2cOn),
	}
}

func deriveSharingURLs(rawSrv config.ServerConfig, rawLoc config.LocationConfig, tlsEnabled bool) SharingURLsView {
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	portSuffix := extractPortSuffix(rawSrv.Listen, tlsEnabled)
	path := rawLoc.Match.Path
	if path == "" {
		path = "/"
	}
	matchType := rawLoc.Match.Type
	if matchType == "" {
		matchType = "prefix"
	}

	if len(rawSrv.ServerNames) == 0 {
		return SharingURLsView{
			Items:     []SharingURLItem{{Kind: "none", Value: NewBoundedText("", BoundedURLMaxBytes)}},
			Count:     0,
			Truncated: false,
		}
	}

	total := len(rawSrv.ServerNames)
	limit := total
	truncated := false
	if limit > SharingMaxEntryURLs {
		limit = SharingMaxEntryURLs
		truncated = true
	}

	items := make([]SharingURLItem, 0, limit)
	for i := 0; i < limit; i++ {
		host := strings.TrimSpace(rawSrv.ServerNames[i])
		if config.ContainsSecretRef(host) || config.ContainsSecretRef(path) || config.ContainsSecretRef(rawSrv.Listen) {
			rawText := fmt.Sprintf("%s://%s%s%s", scheme, host, portSuffix, path)
			items = append(items, SharingURLItem{
				Kind:  "withheld",
				Value: NewBoundedText(rawText, BoundedURLMaxBytes),
			})
			continue
		}
		fullURL := fmt.Sprintf("%s://%s%s%s", scheme, host, portSuffix, path)
		bt := NewBoundedText(fullURL, BoundedURLMaxBytes)
		switch {
		case len(host) > BoundedDisplayMaxBytes || len(path) > BoundedPathMaxBytes || bt.Truncated:
			items = append(items, SharingURLItem{
				Kind:  "truncated",
				Value: bt,
			})
		case host == "*" || strings.HasPrefix(host, "*.") || matchType == "regex":
			items = append(items, SharingURLItem{
				Kind:  "template",
				Value: bt,
			})
		default:
			items = append(items, SharingURLItem{
				Kind:  "concrete",
				Value: bt,
			})
		}
	}
	return SharingURLsView{
		Items:     items,
		Count:     total,
		Truncated: truncated,
	}
}

func extractPortSuffix(listen string, tlsEnabled bool) string {
	idx := strings.LastIndexByte(listen, ':')
	if idx < 0 {
		return ""
	}
	port := strings.TrimSpace(listen[idx+1:])
	if port == "" || (tlsEnabled && port == "443") || (!tlsEnabled && port == "80") {
		return ""
	}
	return ":" + port
}

func deriveSharingJulChecks(
	rawCfg, effCfg *config.Config,
	rawSrv, effSrv config.ServerConfig,
	rawLoc, effLoc config.LocationConfig,
	isSaved bool,
) (SharingJulChecksView, bool) {
	unknown := false
	netCheck := deriveNetworkCheckKind(effLoc)
	if isSaved && rawLoc.Auth != nil && hasAnySecretRefInSlice(append(append([]string(nil), rawLoc.Auth.Allow...), rawLoc.Auth.Deny...)) {
		netCheck = "unknown"
		unknown = true
	}
	credCheck := deriveCredentialCheckKind(effLoc)
	cfgForCert := effCfg
	if cfgForCert == nil {
		cfgForCert = rawCfg
	}
	certCheck := deriveClientCertCheckKind(cfgForCert, effSrv, effLoc)
	if certCheck == "unknown" {
		unknown = true
	}

	var hasRedirectRewrite bool
	for _, rw := range rawLoc.Rewrites {
		if rw.Flag == "redirect" || rw.Flag == "permanent" {
			hasRedirectRewrite = true
			break
		}
	}
	hasReqPlugin := len(rawSrv.Plugins) > 0 || len(rawLoc.Plugins) > 0 || strings.TrimSpace(rawLoc.Plugin) != ""
	hasCORSPreflight := effLoc.CORS != nil && effLoc.CORS.Enabled

	return SharingJulChecksView{
		Network:           netCheck,
		Credential:        credCheck,
		ClientCertificate: certCheck,
		OutsideAuth: SharingOutsideAuthView{
			RedirectRewrites: hasRedirectRewrite,
			RequestPlugins:   hasReqPlugin,
			CORSPreflight:    hasCORSPreflight,
		},
	}, unknown
}

func hasAnySecretRefInSlice(ss []string) bool {
	for _, s := range ss {
		if config.ContainsSecretRef(s) {
			return true
		}
	}
	return false
}

func deriveSharingControls(
	rawCfg, effCfg *config.Config,
	rawLoc, effLoc config.LocationConfig,
	isSaved bool,
) SharingControlsView {
	cfg := effCfg
	if cfg == nil {
		cfg = rawCfg
	}
	cacheOn := cfg.Cache.Enabled && effLoc.Cache
	rlOn := (effLoc.RateLimit != nil && effLoc.RateLimit.Enabled) || (effLoc.RateLimit == nil && cfg.RateLimit.Enabled)
	corsOn := effLoc.CORS != nil && effLoc.CORS.Enabled

	w := effectiveRawWAFForLocation(cfg, effLoc.WAF)
	wafState := "disabled"
	if w.Enabled {
		if isSaved && config.ContainsSecretRef(w.Mode) {
			wafState = "unknown"
		} else if w.Mode == "detect" {
			wafState = "detect"
		} else {
			wafState = "block"
		}
	}

	return SharingControlsView{
		Cache:     boolStateString(cacheOn),
		WAF:       wafState,
		RateLimit: boolStateString(rlOn),
		CORS:      boolStateString(corsOn),
	}
}

func deriveSharingDeclaredIntent(
	rawLoc, effLoc config.LocationConfig,
	isSaved bool,
) (SharingDeclaredIntentView, bool) {
	if rawLoc.Sharing == nil {
		return SharingDeclaredIntentView{State: config.SharingModeNotDeclared}, false
	}
	if isSaved && (config.ContainsSecretRef(rawLoc.Sharing.Mode) ||
		config.ContainsSecretRef(rawLoc.Sharing.AudienceLabelValue()) ||
		config.ContainsSecretRef(rawLoc.Sharing.ExternalEnforcerValue())) {
		return SharingDeclaredIntentView{State: config.SharingModeUnknown}, true
	}
	mode := strings.TrimSpace(effLoc.Sharing.Mode)
	if config.ContainsSecretRef(rawLoc.Sharing.Mode) && effLoc.Sharing != nil {
		mode = strings.TrimSpace(effLoc.Sharing.Mode)
	}
	out := SharingDeclaredIntentView{
		State: mode,
		Mode:  mode,
	}
	if lbl := rawLoc.Sharing.AudienceLabelValue(); lbl != "" {
		bt := NewBoundedText(lbl, BoundedDisplayMaxBytes)
		out.AudienceLabel = &bt
	}
	if enf := rawLoc.Sharing.ExternalEnforcerValue(); enf != "" {
		bt := NewBoundedText(enf, BoundedDisplayMaxBytes)
		out.ExternalEnforcer = &bt
	}
	return out, false
}

func deriveSharingEntryWarnings(
	rawCfg, c *config.Config,
	srvIdx, locIdx int,
	bindScope string,
	transport SharingTransportView,
	checks SharingJulChecksView,
	controls SharingControlsView,
	isSavedLayer bool,
) ([]SharingFindingView, bool) {
	if c == nil || srvIdx < 0 || srvIdx >= len(c.Servers) {
		return nil, true
	}
	srv := c.Servers[srvIdx]
	loc := srv.Locations[locIdx]
	var warnings []SharingFindingView
	addWarn := func(code, msg string) {
		warnings = append(warnings, SharingFindingView{
			Code:     code,
			Severity: "warning",
			Detail:   NewBoundedText(msg, BoundedDisplayMaxBytes),
		})
	}

	unmatched := computeReceivesUnmatchedHosts(c)
	if unmatched[srvIdx] {
		addWarn("receives_unmatched_hosts", "Server block answers unmatched Host headers on this listener.")
	}
	if bindScope == config.BindScopeAllInterfaces {
		addWarn("all_interfaces_bind", "Listener binds all network interfaces.")
	}
	if srv.RedirectHTTPS != 0 {
		addWarn("server_https_redirect", "Plaintext requests to this server block are redirected to HTTPS before route selection.")
	}
	if checks.OutsideAuth.RedirectRewrites || checks.OutsideAuth.RequestPlugins || checks.OutsideAuth.CORSPreflight {
		addWarn("checks_bypassed_by_outer_layers", "Redirect rewrites, request plugins, or CORS preflight execute before per-location checks.")
	}
	if controls.Cache == "enabled" && (checks.Network != "none" || checks.Credential != "none" || checks.ClientCertificate != "not_required") {
		addWarn("cache_on_restricted_entry", "Response cache is enabled on a location with access checks.")
	}
	if checks.Network != "none" {
		for _, ls := range c.Servers {
			if ls.Listen == srv.Listen && ls.ClientAddress != nil && len(ls.ClientAddress.TrustedProxies) > 0 {
				addWarn("cidr_with_trusted_proxies", "CIDR allow/deny checks evaluate the client address attributed by trusted_proxies.")
				break
			}
		}
	}
	if checks.Credential == "jwt" {
		addWarn("jwt_header_only", "Jul validates JWTs in Authorization: Bearer only; provider tokens in other headers or cookies are not checked by Jul.")
	}

	// Listener-wide mixed checks and request-mode mTLS sharing.
	hasOpenOnListen := false
	hasProtectedOnListen := false
	for _, ls := range c.Servers {
		if ls.Listen != srv.Listen {
			continue
		}
		for _, ll := range ls.Locations {
			if ll.Deny {
				continue
			}
			if config.LocationHasNetworkCheck(ll) || config.LocationHasCredentialCheck(ll) || config.LocationHasClientCertCheckInConfig(c, ls, ll) {
				hasProtectedOnListen = true
			} else {
				hasOpenOnListen = true
			}
		}
	}
	if hasOpenOnListen && hasProtectedOnListen {
		addWarn("shared_listener_mixed_checks", "Listener serves both open and access-checked routes.")
	}
	if transport.ClientAuth == "request" && hasOpenOnListen {
		addWarn("client_auth_request_mode_shared", "Listener requests client certificates in request mode while also serving open routes.")
	}

	// Cross-entry cache sharing.
	for _, d := range config.LintSharingFindingsWithRaw(rawCfg, c, isSavedLayer) {
		if d.Code != config.FindingCacheSharedAcrossEntries {
			continue
		}
		var si, li int
		if _, err := fmt.Sscanf(d.Field, "servers[%d].locations[%d]", &si, &li); err == nil && si == srvIdx && li == locIdx {
			addWarn(config.FindingCacheSharedAcrossEntries, d.Message)
		}
	}
	_ = loc
	return warnings, bindScope != config.BindScopeUnknown
}

func deriveSharingNextActions(
	intent SharingDeclaredIntentView,
	checks SharingJulChecksView,
	intentFindings []SharingFindingView,
) []string {
	var actions []string
	add := func(a string) {
		if !slices.Contains(actions, a) {
			actions = append(actions, a)
		}
	}
	if intent.State == config.SharingModeNotDeclared {
		add("declare_intent")
	}
	for _, f := range intentFindings {
		switch f.Code {
		case config.FindingIntentRestrictedUnenforced:
			if intent.ExternalEnforcer != nil {
				add("review_external_policy")
			} else {
				add("add_jul_check")
			}
		case config.FindingIntentRestrictedCached:
			add("review_cache_privacy")
		}
	}
	if checks.Network != "none" || checks.Credential != "none" || checks.ClientCertificate != "not_required" {
		add("verify_from_client")
	}
	if len(actions) == 0 {
		actions = []string{"none"}
	}
	return actions
}

func combineServingAndSavedLayers(
	servingRaw *config.Config,
	savedRaw *config.Config,
	servingLayers []SharingEntryLayer,
	savedLayers []SharingEntryLayer,
	hasSaved bool,
) []SharingEntryView {
	if !hasSaved || savedRaw == nil {
		out := make([]SharingEntryView, 0, len(servingLayers))
		for i := range servingLayers {
			sl := servingLayers[i]
			out = append(out, SharingEntryView{
				Key:     entryViewKey(sl),
				Serving: &sl,
			})
		}
		return out
	}

	pairs := correlatePlanEntries(servingRaw, savedRaw)
	serveByPos := make(map[[2]int]SharingEntryLayer, len(servingLayers))
	for _, sl := range servingLayers {
		serveByPos[[2]int{sl.Position.BlockIndex, sl.Position.LocationIndex}] = sl
	}
	savedByPos := make(map[[2]int]SharingEntryLayer, len(savedLayers))
	for _, dl := range savedLayers {
		savedByPos[[2]int{dl.Position.BlockIndex, dl.Position.LocationIndex}] = dl
	}

	var out []SharingEntryView
	for _, p := range pairs {
		switch {
		case p.before != nil && p.after != nil:
			sl := serveByPos[[2]int{p.before.srvIdx, p.before.locIdx}]
			dl := savedByPos[[2]int{p.after.srvIdx, p.after.locIdx}]
			out = append(out, SharingEntryView{
				Key:     entryViewKey(sl),
				Serving: &sl,
				Saved:   &dl,
			})
		case p.before == nil && p.after != nil:
			dl := savedByPos[[2]int{p.after.srvIdx, p.after.locIdx}]
			out = append(out, SharingEntryView{
				Key:   entryViewKey(dl),
				Saved: &dl,
			})
		case p.before != nil && p.after == nil:
			sl := serveByPos[[2]int{p.before.srvIdx, p.before.locIdx}]
			out = append(out, SharingEntryView{
				Key:            entryViewKey(sl),
				PendingRemoval: true,
				Serving:        &sl,
			})
		}
	}
	return out
}

func entryViewKey(l SharingEntryLayer) string {
	if l.RouteID != nil && l.RouteID.Literal != "" {
		return "route_id:" + l.RouteID.Literal
	}
	return fmt.Sprintf("%s:pos:%d:%d", l.Layer, l.Position.BlockIndex, l.Position.LocationIndex)
}

// BuildSharingViewPage returns one bounded page of the sharing projection,
// validating any revision-bound cursor against the captured snapshot (SH-13).
func (s *Server) BuildSharingViewPage(cursor string, requestedLimit int) (SharingViewResponse, error) {
	derived, err := s.deriveSharingSnapshot()
	if err != nil {
		return SharingViewResponse{}, err
	}

	startIdx := 0
	if strings.TrimSpace(cursor) != "" {
		idx, parseErr := parseSharingCursor(cursor, derived.snapshotToken)
		if parseErr != nil {
			return SharingViewResponse{}, parseErr
		}
		startIdx = idx
	}

	limit := requestedLimit
	if limit <= 0 || limit > SharingMaxPageEntries {
		limit = SharingMaxPageEntries
	}

	total := len(derived.entries)
	if startIdx > total {
		startIdx = total
	}

	var pageEntries []SharingEntryView
	usedBlocks := make(map[string]bool)
	usedGroups := make(map[string]bool)
	var pageBlockList []SharingBlockRow
	var pageGroupList []SharingBackendGroupRow

	// Account for the response envelope (including worst-case NextCursor and JSON
	// framing) plus every referenced block and backend-group row so the complete
	// serialized SharingViewResponse stays within SharingMaxPageBytes (1 MiB).
	envelopeBytes, _ := json.Marshal(SharingViewResponse{
		SharingViewVersion: CurrentSharingViewVersion,
		Snapshot:           derived.snapshotToken,
		SavedStatus:        derived.savedStatus,
		Authority:          derived.authority,
		Blocks:             []SharingBlockRow{},
		BackendGroups:      []SharingBackendGroupRow{},
		Entries:            []SharingEntryView{},
		TotalEntries:       total,
		NextCursor:         fmt.Sprintf("%s#%d", derived.snapshotToken, total),
	})
	runningBytes := len(envelopeBytes) + 64

	for idx := startIdx; idx < total && len(pageEntries) < limit; idx++ {
		entry := derived.entries[idx]
		entryBytes, _ := json.Marshal(entry)
		deltaBytes := len(entryBytes) + 1

		var newBlocks []SharingBlockRow
		seenNewBlocks := make(map[string]bool)
		var newGroups []SharingBackendGroupRow
		seenNewGroups := make(map[string]bool)

		for _, layer := range []*SharingEntryLayer{entry.Serving, entry.Saved} {
			if layer == nil {
				continue
			}
			if layer.ServiceBlock != "" && !usedBlocks[layer.ServiceBlock] && !seenNewBlocks[layer.ServiceBlock] {
				if blk, ok := derived.blocksByKey[layer.ServiceBlock]; ok {
					seenNewBlocks[layer.ServiceBlock] = true
					newBlocks = append(newBlocks, blk)
					blkBytes, _ := json.Marshal(blk)
					deltaBytes += len(blkBytes) + 1
				}
			}
			if layer.BackendGroup != "" && !usedGroups[layer.BackendGroup] && !seenNewGroups[layer.BackendGroup] {
				if grp, ok := derived.groupsByKey[layer.BackendGroup]; ok {
					seenNewGroups[layer.BackendGroup] = true
					newGroups = append(newGroups, grp)
					grpBytes, _ := json.Marshal(grp)
					deltaBytes += len(grpBytes) + 1
				}
			}
		}

		// Always include at least one entry per page (§4.3).
		if len(pageEntries) > 0 && runningBytes+deltaBytes > SharingMaxPageBytes {
			break
		}
		pageEntries = append(pageEntries, entry)
		for _, blk := range newBlocks {
			usedBlocks[blk.Key] = true
			pageBlockList = append(pageBlockList, blk)
		}
		for _, grp := range newGroups {
			usedGroups[grp.Key] = true
			pageGroupList = append(pageGroupList, grp)
		}
		runningBytes += deltaBytes
	}

	nextCursor := ""
	nextIdx := startIdx + len(pageEntries)
	if nextIdx < total {
		nextCursor = fmt.Sprintf("%s#%d", derived.snapshotToken, nextIdx)
	}

	if pageBlockList == nil {
		pageBlockList = []SharingBlockRow{}
	}
	if pageEntries == nil {
		pageEntries = []SharingEntryView{}
	}

	return SharingViewResponse{
		SharingViewVersion: CurrentSharingViewVersion,
		Snapshot:           derived.snapshotToken,
		SavedStatus:        derived.savedStatus,
		Authority:          derived.authority,
		Blocks:             pageBlockList,
		BackendGroups:      pageGroupList,
		Entries:            pageEntries,
		TotalEntries:       total,
		NextCursor:         nextCursor,
	}, nil
}

type errSharingStaleCursor struct {
	message string
}

func (e *errSharingStaleCursor) Error() string { return e.message }

func parseSharingCursor(cursor, currentToken string) (int, error) {
	idx := strings.LastIndexByte(cursor, '#')
	if idx <= 0 {
		return 0, &errSharingStaleCursor{message: "the configuration changed; reload the view"}
	}
	tokenPart := cursor[:idx]
	offsetPart := cursor[idx+1:]
	if tokenPart != currentToken {
		return 0, &errSharingStaleCursor{message: "the configuration changed; reload the view"}
	}
	n, err := strconv.Atoi(offsetPart)
	if err != nil || n < 0 {
		return 0, &errSharingStaleCursor{message: "invalid pagination cursor"}
	}
	return n, nil
}

// ReadSharingFullValue returns a bounded chunk of a full raw configuration
// value addressed by snapshot, layer, ref, field, and offset (spec §4.3, W-4).
// It never returns a resolved secret reference value.
func (s *Server) ReadSharingFullValue(
	snapshotToken, layer, ref, field string,
	offset, chunkLimit int,
) (SharingValueResponse, error) {
	derived, err := s.deriveSharingSnapshot()
	if err != nil {
		return SharingValueResponse{}, err
	}
	if strings.TrimSpace(snapshotToken) == "" || snapshotToken != derived.snapshotToken {
		return SharingValueResponse{}, &errSharingStaleCursor{message: "the configuration changed; reload the view"}
	}

	targetCfg := derived.servingRaw
	if layer == "saved" {
		if derived.savedRaw == nil {
			return SharingValueResponse{}, fmt.Errorf("saved layer is not available")
		}
		targetCfg = derived.savedRaw
	} else if layer != "" && layer != "serving" {
		return SharingValueResponse{}, fmt.Errorf("unknown layer %q (want serving or saved)", layer)
	}

	fullValue, err := resolveSharingRawFieldValue(targetCfg, layer, ref, field)
	if err != nil {
		return SharingValueResponse{}, err
	}

	if chunkLimit <= 0 || chunkLimit > SharingMaxValueChunkBytes {
		chunkLimit = SharingMaxValueChunkBytes
	}
	total := len(fullValue)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + chunkLimit
	if end > total {
		end = total
	}
	// Cut at UTF-8 boundary when not at end of string.
	for end > offset && end < total && !utf8.ValidString(fullValue[offset:end]) {
		end--
	}
	var nextOffset *int
	if end < total {
		next := end
		nextOffset = &next
	}
	return SharingValueResponse{
		Snapshot:   derived.snapshotToken,
		Chunk:      fullValue[offset:end],
		Offset:     offset,
		TotalBytes: total,
		NextOffset: nextOffset,
	}, nil
}

func resolveSharingRawFieldValue(rawCfg *config.Config, layer, ref, field string) (string, error) {
	if rawCfg == nil {
		return "", fmt.Errorf("configuration unavailable")
	}
	// Block-level fields: ref is block_index (e.g. "0" or "serving:block:0").
	if field == "listen" || field == "display_label" || field == "server_names" {
		blkIdx, err := parseBlockRef(rawCfg, ref)
		if err != nil {
			return "", err
		}
		srv := rawCfg.Servers[blkIdx]
		switch field {
		case "listen":
			return srv.Listen, nil
		case "display_label":
			if strings.TrimSpace(srv.Name) != "" {
				return strings.TrimSpace(srv.Name), nil
			}
			if len(srv.ServerNames) > 0 {
				return srv.ServerNames[0], nil
			}
			return srv.Listen, nil
		case "server_names":
			data, _ := json.Marshal(srv.ServerNames)
			return string(data), nil
		}
	}

	// Entry-level fields: ref is route_id or "blockIdx:locIdx".
	srvIdx, locIdx, err := parseEntryRef(rawCfg, ref)
	if err != nil {
		return "", err
	}
	srv := rawCfg.Servers[srvIdx]
	loc := srv.Locations[locIdx]
	switch field {
	case "path":
		return loc.Match.Path, nil
	case "route_id_ref":
		if loc.RouteID == nil {
			return "", nil
		}
		return *loc.RouteID, nil
	case "audience_label":
		if loc.Sharing == nil {
			return "", nil
		}
		return loc.Sharing.AudienceLabelValue(), nil
	case "external_enforcer":
		if loc.Sharing == nil {
			return "", nil
		}
		return loc.Sharing.ExternalEnforcerValue(), nil
	case "selector":
		ordinals := routeOrdinals(rawCfg)
		matchType := loc.Match.Type
		if matchType == "" {
			matchType = "prefix"
		}
		sel := map[string]any{
			"listen":           srv.Listen,
			"server_names":     srv.ServerNames,
			"match_type":       matchType,
			"path":             loc.Match.Path,
			"match_ordinal":    ordinals[srvIdx][locIdx],
			"selector_version": routeSelectorVersion,
		}
		data, _ := json.Marshal(sel)
		return string(data), nil
	default:
		_ = layer
		return "", fmt.Errorf("unknown field %q", field)
	}
}

func parseBlockRef(rawCfg *config.Config, ref string) (int, error) {
	trimmed := strings.TrimSpace(ref)
	if idx := strings.LastIndexByte(trimmed, ':'); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < 0 || n >= len(rawCfg.Servers) {
		return -1, fmt.Errorf("invalid block reference %q", ref)
	}
	return n, nil
}

func parseEntryRef(rawCfg *config.Config, ref string) (int, int, error) {
	trimmed := strings.TrimSpace(ref)
	if i, j, ok := findServingLocationByRouteID(rawCfg, trimmed); ok {
		return i, j, nil
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) >= 2 {
		bi, err1 := strconv.Atoi(parts[len(parts)-2])
		li, err2 := strconv.Atoi(parts[len(parts)-1])
		if err1 == nil && err2 == nil && bi >= 0 && bi < len(rawCfg.Servers) && li >= 0 && li < len(rawCfg.Servers[bi].Locations) {
			return bi, li, nil
		}
	}
	return -1, -1, fmt.Errorf("entry reference %q not found", ref)
}

func (s *Server) handleSharingView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if id, ok := rbac.IdentityFromContext(r.Context()); ok && !id.Has(rbac.ConfigRead) {
		writeForbidden(w, r, rbac.ConfigRead, id)
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	limit := 0
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 {
			limit = n
		}
	}
	page, err := s.BuildSharingViewPage(cursor, limit)
	if err != nil {
		if _, ok := err.(*errSharingStaleCursor); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":       false,
				"conflict": true,
				"message":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":      false,
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSharingValue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if id, ok := rbac.IdentityFromContext(r.Context()); ok && !id.Has(rbac.ConfigRead) {
		writeForbidden(w, r, rbac.ConfigRead, id)
		return
	}
	q := r.URL.Query()
	snapshot := strings.TrimSpace(q.Get("snapshot"))
	layer := strings.TrimSpace(q.Get("layer"))
	if layer == "" {
		layer = "serving"
	}
	ref := strings.TrimSpace(q.Get("ref"))
	field := strings.TrimSpace(q.Get("field"))
	offset := 0
	if rawOff := strings.TrimSpace(q.Get("offset")); rawOff != "" {
		if n, err := strconv.Atoi(rawOff); err == nil && n >= 0 {
			offset = n
		}
	}
	limit := 0
	if rawLim := strings.TrimSpace(q.Get("limit")); rawLim != "" {
		if n, err := strconv.Atoi(rawLim); err == nil && n > 0 {
			limit = n
		}
	}
	res, err := s.ReadSharingFullValue(snapshot, layer, ref, field, offset, limit)
	if err != nil {
		if _, ok := err.(*errSharingStaleCursor); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"ok":       false,
				"conflict": true,
				"message":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

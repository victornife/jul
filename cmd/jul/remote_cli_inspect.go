// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"flag"
	"fmt"
	"jul/internal/adminapi"
)

func cmdRemoteCacheInspect(args []string) int {
	fs := flag.NewFlagSet("cache inspect", flag.ContinueOnError)
	var common remoteCommon
	host := fs.String("host", "", "configured host authority")
	url := fs.String("url", "", "exact URL; query values are never returned")
	uri := fs.String("uri", "", "exact request URI, with --host")
	prefix := fs.String("prefix", "", "segment-bounded prefix, with --host")
	glob := fs.String("glob", "", "anchored glob, with --host")
	tag := fs.String("tag", "", "origin tag, with --host")
	route := fs.String("route-id", "", "configured route identity")
	key := fs.String("key", "", "exact internal cache key")
	cursor := fs.String("cursor", "", "opaque next cursor; repeat the same selector")
	page := fs.Int("page-size", 50, "resources per page (1..50)")
	client, code, ok := parseRemote(fs, args, &common)
	if !ok {
		return code
	}
	if fs.NArg() != 0 {
		return renderUsageError(common.json, "cache inspect", "unexpected positional arguments")
	}
	selectors := 0
	for _, v := range []string{*url, *uri, *prefix, *glob, *tag, *key} {
		if v != "" {
			selectors++
		}
	}
	if selectors > 1 {
		return renderUsageError(common.json, "cache inspect", "choose one exact URL, URI, prefix, glob, tag or key")
	}
	sel := adminapi.CacheSelector{Kind: "host", Host: *host, RouteID: *route}
	switch {
	case *url != "":
		sel.Kind = "exact_url"
		sel.URL = *url
	case *uri != "":
		sel.Kind = "exact_url"
		sel.URI = *uri
	case *prefix != "":
		sel.Kind = "prefix"
		sel.Prefix = *prefix
	case *glob != "":
		sel.Kind = "glob"
		sel.Pattern = *glob
	case *tag != "":
		sel.Kind = "tag"
		sel.Tags = []string{*tag}
	case *key != "":
		sel.Kind = "key"
		sel.Key = *key
	}
	ctx, cancel := remoteContext()
	defer cancel()
	v, raw, err := client.CacheInspect(ctx, adminapi.CacheInspectRequest{Selector: sel, Cursor: *cursor, PageSize: *page})
	if err != nil {
		return renderRemoteError(common.json, "cache inspect", err, raw.RequestID)
	}
	if common.json {
		return renderCacheJSON("cache inspect", true, 0, raw.Body)
	}
	d := v.Inspection
	fmt.Fprintf(stdout, "cache inspection: %s; resources=%d representations=%d accuracy=%s evidence=%s\n", d.State, d.MatchedResources, d.MatchedRepresentations, d.CountAccuracy, d.EvidenceState)
	if v.PolicySource != "" || v.RouteCacheState != "" {
		maxTTL := "none"
		if d.Policy.MaxTTLSeconds != nil {
			maxTTL = fmt.Sprintf("%.0fs", *d.Policy.MaxTTLSeconds)
		}
		fmt.Fprintf(stdout, "policy: source=%s route_state=%s default_ttl=%.0fs max_ttl=%s swr=%.0fs sie=%.0fs bypass_any_cookie=%t bypass_cookies=%d\n",
			v.PolicySource, v.RouteCacheState, d.Policy.DefaultTTLSeconds, maxTTL, d.Policy.SWRSeconds, d.Policy.SIESeconds, d.Policy.BypassAnyCookie, len(d.Policy.BypassCookies))
	}
	for _, r := range d.Resources {
		fmt.Fprintf(stdout, "%s%s representations=%d state=%s bytes=%d\n", r.Host, r.Path, r.Representations, r.FreshnessState, r.BodyBytes)
	}
	for _, r := range d.Decisions {
		fmt.Fprintf(stdout, "observed %s: %s %s%s at %s\n", r.Reason, r.Summary, r.Host, r.Path, r.ObservedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if v.NextCursor != "" {
		fmt.Fprintf(stdout, "next_cursor=%s expires_at=%s (repeat the same selector)\n", v.NextCursor, v.ExpiresAt)
	}
	fmt.Fprintln(stdout, "Metadata only. No origin request performed; absent entries do not explain history or guarantee future hits.")
	return 0
}

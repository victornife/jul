// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build consul

package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"
)

// consulDiscoverer resolves a service from Consul's health API over plain HTTP.
// No Consul client library is linked in; only the documented REST endpoint
// (/v1/health/service/<name>) is used, keeping the binary lean.
type consulDiscoverer struct {
	client   *http.Client
	endpoint string // fully-formed health endpoint with query string
	token    string
	describe string
}

func newConsulDiscoverer(cfg config.DiscoveryConfig, dial DialFunc) (Discoverer, error) {
	c := cfg.Consul
	if c == nil || strings.TrimSpace(c.Service) == "" {
		return nil, fmt.Errorf("consul discovery requires consul.service")
	}
	addr := strings.TrimSpace(c.Address)
	if addr == "" {
		addr = "http://127.0.0.1:8500"
	}
	base, err := url.Parse(addr)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("consul discovery: invalid address %q", c.Address)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/health/service/" + c.Service
	q := base.Query()
	passing := true
	if c.PassingOnly != nil {
		passing = *c.PassingOnly
	}
	if passing {
		q.Set("passing", "true")
	}
	if t := strings.TrimSpace(c.Tag); t != "" {
		q.Set("tag", t)
	}
	if dc := strings.TrimSpace(c.Datacenter); dc != "" {
		q.Set("dc", dc)
	}
	base.RawQuery = q.Encode()

	client := &http.Client{Timeout: 10 * time.Second}
	if dial != nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = dial
		client.Transport = t
	}

	return &consulDiscoverer{
		client:   client,
		endpoint: base.String(),
		token:    strings.TrimSpace(c.Token),
		describe: "consul:" + c.Service,
	}, nil
}

// consulServiceEntry is the subset of Consul's /v1/health/service response that
// the resolver reads.
type consulServiceEntry struct {
	Node struct {
		Address string `json:"Address"`
	} `json:"Node"`
	Service struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
		Weights struct {
			Passing int `json:"Passing"`
		} `json:"Weights"`
	} `json:"Service"`
}

func (d *consulDiscoverer) Resolve(ctx context.Context) ([]Target, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint, nil)
	if err != nil {
		return nil, err
	}
	if d.token != "" {
		req.Header.Set("X-Consul-Token", d.token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("consul: unexpected status %s", resp.Status)
	}
	var entries []consulServiceEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("consul: decode response: %w", err)
	}
	out := make([]Target, 0, len(entries))
	for _, e := range entries {
		addr := strings.TrimSpace(e.Service.Address)
		if addr == "" {
			addr = strings.TrimSpace(e.Node.Address)
		}
		if addr == "" || e.Service.Port == 0 {
			continue
		}
		out = append(out, Target{
			Address: net.JoinHostPort(addr, strconv.Itoa(e.Service.Port)),
			Weight:  e.Service.Weights.Passing,
		})
	}
	return out, nil
}

func (d *consulDiscoverer) Describe() string { return d.describe }

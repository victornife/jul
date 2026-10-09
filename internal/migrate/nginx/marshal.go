// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"bytes"
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"jul/internal/config"
	"reflect"
)

// MarshalCandidate omits ordinary default-valued fields from importer output.
// Optional policies, explicit zero durations and empty MIME tables retain their
// presence. Every result is reparsed and compared to the full candidate, so a
// future default change cannot silently change migration behavior.
func MarshalCandidate(candidate *config.Config) ([]byte, error) {
	full, err := config.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	effective, err := config.Parse(full)
	if err != nil {
		return nil, err
	}
	scaffold := &config.Config{
		Servers:   make([]config.ServerConfig, len(candidate.Servers)),
		Upstreams: make([]config.UpstreamConfig, len(candidate.Upstreams)),
	}
	for i, srv := range candidate.Servers {
		scaffold.Servers[i].Locations = make([]config.LocationConfig, len(srv.Locations))
	}
	for i, up := range candidate.Upstreams {
		scaffold.Upstreams[i].Servers = make([]config.UpstreamServer, len(up.Servers))
		for j := range scaffold.Upstreams[i].Servers {
			scaffold.Upstreams[i].Servers[j].Address = "127.0.0.1:1"
		}
	}
	baselineBytes, err := config.Marshal(scaffold)
	if err != nil {
		return nil, err
	}
	baseline, err := config.Parse(baselineBytes)
	if err != nil {
		return nil, err
	}
	baselineBytes, err = config.Marshal(baseline)
	if err != nil {
		return nil, err
	}
	effectiveBytes, err := config.Marshal(effective)
	if err != nil {
		return nil, err
	}
	var values, defaults map[string]any
	if err = toml.Unmarshal(effectiveBytes, &values); err != nil {
		return nil, err
	}
	if err = toml.Unmarshal(baselineBytes, &defaults); err != nil {
		return nil, err
	}
	pruneCandidateDefaults(values, defaults)
	compact, err := toml.Marshal(values)
	if err != nil {
		return nil, err
	}
	roundTrip, err := config.Parse(compact)
	if err != nil {
		return nil, err
	}
	roundTripBytes, err := config.Marshal(roundTrip)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(effectiveBytes, roundTripBytes) {
		return nil, fmt.Errorf("compact importer output changed effective configuration")
	}
	return compact, nil
}

func pruneCandidateDefaults(values, defaults map[string]any) {
	for key, value := range values {
		// Resource and route identity must remain explicit even when it happens
		// to equal a scaffold value.
		if key == "match" || (key == "address" || key == "name" || key == "listen") && value != "" {
			continue
		}
		baseline, exists := defaults[key]
		if !exists {
			continue
		} // optional policy presence is significant
		switch typed := value.(type) {
		case map[string]any:
			if base, ok := baseline.(map[string]any); ok {
				pruneCandidateDefaults(typed, base)
				if len(typed) == 0 {
					delete(values, key)
				}
			}
		case []any:
			if reflect.DeepEqual(value, baseline) {
				delete(values, key)
				continue
			}
			base, ok := baseline.([]any)
			if ok && len(typed) > 0 && len(typed) == len(base) {
				for i, item := range typed {
					if table, ok := item.(map[string]any); ok {
						if defaultTable, ok := base[i].(map[string]any); ok {
							pruneCandidateDefaults(table, defaultTable)
						}
					}
				}
			}
		default:
			if reflect.DeepEqual(value, baseline) {
				delete(values, key)
			}
		}
	}
}

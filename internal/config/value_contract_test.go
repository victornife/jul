// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

type valueContractDocument struct {
	Version int                  `json:"version"`
	Fields  []valueContractField `json:"fields"`
}

type valueContractField struct {
	GoField       string `json:"go_field"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Constraint    string `json:"constraint"`
	ZeroSemantics string `json:"zero_semantics"`
}

func configPackageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func TestConfigValueContractCoversEveryNumericPublicLeaf(t *testing.T) {
	packageDir := configPackageDir(t)
	data, err := os.ReadFile(filepath.Join(packageDir, "..", "..", "docs", "config-value-contract.json"))
	if err != nil {
		t.Fatalf("read value contract: %v", err)
	}
	var contract valueContractDocument
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("decode value contract: %v", err)
	}
	if contract.Version != 1 {
		t.Fatalf("value contract version = %d, want 1", contract.Version)
	}

	inventory := make(map[string]valueContractField, len(contract.Fields))
	last := ""
	for _, field := range contract.Fields {
		if field.GoField <= last {
			t.Fatalf("value contract is not strictly sorted at %q", field.GoField)
		}
		last = field.GoField
		if field.Path == "" || field.Kind == "" || field.Constraint == "" || field.ZeroSemantics == "" {
			t.Fatalf("value contract entry %q is incomplete: %+v", field.GoField, field)
		}
		if _, exists := inventory[field.GoField]; exists {
			t.Fatalf("duplicate value contract entry %q", field.GoField)
		}
		inventory[field.GoField] = field
	}

	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(packageDir, "schema.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse schema.go: %v", err)
	}
	var numeric []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec := spec.(*ast.TypeSpec)
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structType.Fields.List {
				if field.Tag == nil || len(field.Names) != 1 {
					continue
				}
				ident, ok := field.Type.(*ast.Ident)
				if !ok {
					continue
				}
				switch ident.Name {
				case "int", "float64", "Duration", "Size":
					numeric = append(numeric, typeSpec.Name.Name+"."+field.Names[0].Name)
				}
			}
		}
	}
	sort.Strings(numeric)
	for _, field := range numeric {
		if _, ok := inventory[field]; !ok {
			t.Errorf("numeric public field %q has no audited validation disposition", field)
		}
	}

	// String enum/grammar fields cannot be inferred from their Go type. Keep the
	// expected set explicit so a future edit cannot silently drop their metadata.
	expectedStringConstraints := []string{
		"ACMEConfig.CA", "ACMEConfig.Challenge", "AccessLogConfig.Format",
		"BackendTLSConfig.CAMode", "BackendTLSConfig.MinVersion",
		"BackendTLSConfig.PeerIdentities",
		"AccessLogConfig.Sinks", "CORSConfig.AllowedOrigins",
		"ClientAddressConfig.ForwardedHeaders",
		"ClientAddressConfig.TrustedProxies", "ClientAuthConfig.Mode",
		"CompressionConfig.Encoders",
		"DiscoveryConfig.Type", "GlobalConfig.LogFormat", "GlobalConfig.LogLevel",
		"GlobalConfig.WorkerThreads", "GRPCTranscodeConfig.StreamMode",
		"HeaderMatch.Name", "HeaderMatch.Op", "HeaderMatch.Value",
		"HealthCheckConfig.Type", "MatchConfig.Methods", "MatchConfig.Type",
		"PluginConfig.Type", "QueryMatch.Name", "QueryMatch.Op", "QueryMatch.Value",
		"RateLimitConfig.Key", "ResponseHeaderOp.Op", "RewriteConfig.Flag", "StreamServer.Protocol",
		"StreamServer.ProxyProtocol", "TLSConfig.MinVersion", "TracingConfig.Exporter",
		"UpstreamConfig.Strategy", "WAFConfig.Mode",
	}
	for _, field := range expectedStringConstraints {
		if _, ok := inventory[field]; !ok {
			t.Errorf("enum/grammar public field %q has no audited validation disposition", field)
		}
	}
}

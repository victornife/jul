// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

func TestValidateSpoolIsolationRejectsOverlapsAndSymlinkAliases(t *testing.T) {
	base := t.TempDir()
	public := filepath.Join(base, "public")
	if err := os.Mkdir(public, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "public-alias")
	if err := os.Symlink(public, alias); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		spool     string
		staticDir string
	}{
		{name: "spool below static root", spool: filepath.Join(public, "spool"), staticDir: public},
		{name: "static root below spool", spool: base, staticDir: public},
		{name: "symlink alias", spool: filepath.Join(public, "spool"), staticDir: alias},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Servers: []config.ServerConfig{{
				Locations: []config.LocationConfig{{Root: tt.staticDir}},
			}}}
			if err := validateSpoolIsolation(cfg, tt.spool); err == nil {
				t.Fatal("overlapping spool and static roots were accepted")
			}
		})
	}
}

func TestValidateSpoolIsolationAllowsSeparateRoots(t *testing.T) {
	base := t.TempDir()
	spool := filepath.Join(base, "spool")
	static := filepath.Join(base, "public")
	if err := os.Mkdir(static, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Locations: []config.LocationConfig{{Root: static}},
	}}}
	if err := validateSpoolIsolation(cfg, spool); err != nil {
		t.Fatalf("separate roots rejected: %v", err)
	}
}

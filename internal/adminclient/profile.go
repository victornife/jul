// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Profile is the intentionally small durable remote-connection profile.
type Profile struct {
	Endpoint       string `json:"endpoint,omitempty"`
	TokenFile      string `json:"token_file,omitempty"`
	CAFile         string `json:"ca_file,omitempty"`
	ClientCertFile string `json:"client_cert_file,omitempty"`
	ClientKeyFile  string `json:"client_key_file,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
}

type profileDocument struct {
	Profiles map[string]Profile `json:"profiles"`
}

// ResolveOptions captures explicit command values. Empty values fall through
// to the named profile, then JUL_* environment variables. There is no endpoint default.
type ResolveOptions struct {
	Endpoint       string
	ProfileName    string
	ProfilesFile   string
	TokenFile      string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	Timeout        time.Duration
	Stdin          io.Reader
	Getenv         func(string) string
}

// DefaultProfilesFile is stable and cross-platform via os.UserConfigDir.
func DefaultProfilesFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jul", "profiles.json"), nil
}

// ResolveConnection implements ADR 0019 §31 precedence. Warnings contain paths
// only, never credential material.
func ResolveConnection(opts ResolveOptions) (Config, []string, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}

	var p Profile
	var warnings []string
	if opts.ProfileName != "" {
		profilesFile := opts.ProfilesFile
		if profilesFile == "" {
			var err error
			profilesFile, err = DefaultProfilesFile()
			if err != nil {
				return Config{}, nil, fmt.Errorf("resolve profiles file: %w", err)
			}
		}
		loaded, warn, err := readProfile(profilesFile, opts.ProfileName)
		if err != nil {
			return Config{}, nil, err
		}
		p = loaded
		warnings = append(warnings, warn...)
	}

	cfg := Config{}
	cfg.Endpoint = firstNonEmpty(opts.Endpoint, p.Endpoint, getenv("JUL_ENDPOINT"))
	cfg.CAFile = firstNonEmpty(opts.CAFile, p.CAFile)
	cfg.ClientCertFile = firstNonEmpty(opts.ClientCertFile, p.ClientCertFile)
	cfg.ClientKeyFile = firstNonEmpty(opts.ClientKeyFile, p.ClientKeyFile)

	if opts.Timeout > 0 {
		cfg.Timeout = opts.Timeout
	} else if p.Timeout != "" {
		d, err := time.ParseDuration(p.Timeout)
		if err != nil || d <= 0 {
			return Config{}, nil, errors.New("profile timeout must be a positive Go duration")
		}
		cfg.Timeout = d
	} else {
		cfg.Timeout = DefaultTimeout
	}

	tokenFile := firstNonEmpty(opts.TokenFile, p.TokenFile, getenv("JUL_TOKEN_FILE"))
	if tokenFile != "" {
		token, warn, err := readToken(tokenFile, stdin)
		if err != nil {
			return Config{}, nil, err
		}
		cfg.Token = token
		warnings = append(warnings, warn...)
	} else {
		cfg.Token = strings.TrimSpace(getenv("JUL_TOKEN"))
	}
	if _, err := ParseEndpoint(cfg.Endpoint); err != nil {
		return Config{}, nil, err
	}
	return cfg, warnings, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func readProfile(filename, name string) (Profile, []string, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("read profile file %q: %w", filename, err)
	}
	warnings := permissionWarnings(filename, info)
	data, err := os.ReadFile(filename)
	if err != nil {
		return Profile{}, nil, fmt.Errorf("read profile file %q: %w", filename, err)
	}
	var doc profileDocument
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Profile{}, nil, fmt.Errorf("parse profile file %q: %w", filename, err)
	}
	p, ok := doc.Profiles[name]
	if !ok {
		return Profile{}, nil, fmt.Errorf("profile %q not found", name)
	}
	return p, warnings, nil
}

func readToken(filename string, stdin io.Reader) (string, []string, error) {
	if filename == "-" {
		data, err := io.ReadAll(io.LimitReader(stdin, 64<<10))
		if err != nil {
			return "", nil, fmt.Errorf("read token from stdin: %w", err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", nil, errors.New("token from stdin is empty")
		}
		return token, nil, nil
	}
	info, err := os.Stat(filename)
	if err != nil {
		return "", nil, fmt.Errorf("read token file %q: %w", filename, err)
	}
	warnings := permissionWarnings(filename, info)
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", nil, fmt.Errorf("read token file %q: %w", filename, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", nil, errors.New("token file is empty")
	}
	return token, warnings, nil
}

func permissionWarnings(filename string, info os.FileInfo) []string {
	if runtime.GOOS == "windows" || info == nil {
		return nil
	}
	if info.Mode().Perm()&0o077 != 0 {
		return []string{fmt.Sprintf("warning: %s is readable or writable by other users; use owner-only permissions", filename)}
	}
	return nil
}

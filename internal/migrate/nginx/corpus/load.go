// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	manifestName       = "manifest.json"
	fixtureReadmeName  = "README.md"
	maxFixtureFileSize = 1 << 20
)

// Fixture is one validated on-disk corpus entry.
type Fixture struct {
	Dir      string
	Manifest Manifest
}

// RootPath returns the validated NGINX root file.
func (f Fixture) RootPath() string {
	return filepath.Join(f.Dir, filepath.FromSlash(f.Manifest.Root))
}

// IncludeRoot returns the fixture-local NGINX trust root.
func (f Fixture) IncludeRoot() string {
	return filepath.Join(f.Dir, "nginx")
}

// Load reads and validates one fixture directory.
func Load(dir string) (Fixture, error) {
	manifestPath := filepath.Join(dir, manifestName)
	file, err := os.Open(manifestPath)
	if err != nil {
		return Fixture{}, fmt.Errorf("open %s: %w", manifestPath, err)
	}
	manifest, decodeErr := DecodeManifest(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return Fixture{}, fmt.Errorf("fixture %s: %w", filepath.Base(dir), decodeErr)
	}
	if closeErr != nil {
		return Fixture{}, fmt.Errorf("close %s: %w", manifestPath, closeErr)
	}
	fixture := Fixture{Dir: filepath.Clean(dir), Manifest: manifest}
	if err := fixture.ValidateLayout(); err != nil {
		return Fixture{}, err
	}
	return fixture, nil
}

// Discover loads every direct fixture directory in deterministic name order.
func Discover(root string) ([]Fixture, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read corpus root: %w", err)
	}
	var fixtures []Fixture
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("corpus entry %q is a symlink", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		fixture, err := Load(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		if fixture.Manifest.ID != entry.Name() {
			return nil, fmt.Errorf("fixture directory %q must match manifest id %q", entry.Name(), fixture.Manifest.ID)
		}
		fixtures = append(fixtures, fixture)
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("corpus contains no fixtures")
	}
	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Manifest.ID < fixtures[j].Manifest.ID })
	return fixtures, nil
}

// ValidateLayout proves that the fixture is self-contained, bounded, regular,
// and free from high-confidence private-key material.
func (f Fixture) ValidateLayout() error {
	var errs []error
	rootDir := filepath.Clean(f.Dir)
	nginxRoot := f.IncludeRoot()
	if !pathInside(rootDir, nginxRoot) {
		errs = append(errs, fmt.Errorf("fixture %q nginx root escapes fixture", f.Manifest.ID))
	}
	if info, err := os.Stat(nginxRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		errs = append(errs, fmt.Errorf("fixture %q nginx root: %w", f.Manifest.ID, err))
	}
	rootPath := f.RootPath()
	if !pathInside(nginxRoot, rootPath) {
		errs = append(errs, fmt.Errorf("fixture %q root must stay below nginx/", f.Manifest.ID))
	} else if info, err := os.Stat(rootPath); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		errs = append(errs, fmt.Errorf("fixture %q root: %w", f.Manifest.ID, err))
	}
	readmePath := filepath.Join(rootDir, fixtureReadmeName)
	if data, err := os.ReadFile(readmePath); err != nil || strings.TrimSpace(string(data)) == "" {
		if err == nil {
			err = fmt.Errorf("empty file")
		}
		errs = append(errs, fmt.Errorf("fixture %q README: %w", f.Manifest.ID, err))
	}

	walkErr := filepath.WalkDir(rootDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is forbidden: %s", relativeDisplay(rootDir, path))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular fixture file: %s", relativeDisplay(rootDir, path))
		}
		if info.Size() > maxFixtureFileSize {
			return fmt.Errorf("fixture file exceeds %d bytes: %s", maxFixtureFileSize, relativeDisplay(rootDir, path))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if containsPrivateKeyMaterial(data) {
			return fmt.Errorf("private-key material is forbidden: %s", relativeDisplay(rootDir, path))
		}
		return nil
	})
	if walkErr != nil {
		errs = append(errs, fmt.Errorf("fixture %q layout: %w", f.Manifest.ID, walkErr))
	}
	return errors.Join(errs...)
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relativeDisplay(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

func containsPrivateKeyMaterial(data []byte) bool {
	text := string(data)
	for _, marker := range []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

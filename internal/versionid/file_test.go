// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package versionid

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestKeyLifecycleAndCompetingInitializers(t *testing.T) {
	dir := privateTestDirectory(t)
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = canonical
	}
	path := filepath.Join(dir, "private", "key")
	if _, err := Load(path); err == nil {
		t.Fatal("missing key silently initialized")
	}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Init(path)
			if err != nil {
				errs <- err
				return
			}
			ids <- c.ID()
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("initializer used losing key")
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Init(path)
	if err != nil || c.ID() != first {
		t.Fatal("existing key not reused")
	}
	repeated, err := Load(path)
	if err != nil || repeated.Config(nil) != "" || repeated.ID() != first {
		t.Fatal("restart changed context")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing key overwritten")
	}
	// A private interrupted temporary file is never adopted as the installed key.
	os.WriteFile(filepath.Join(filepath.Dir(path), ".effective-version-key-interrupted"), []byte("partial"), 0600)
	if c, err := Load(path); err != nil || c.ID() != first {
		t.Fatal("orphan temporary file changed context")
	}
	backup := filepath.Join(filepath.Dir(path), "backup")
	os.WriteFile(backup, before, 0600)
	if c, err := Load(backup); err != nil || c.ID() != first {
		t.Fatal("backup/restore changed context")
	}
	os.Remove(path)
	if _, err := Load(path); err == nil {
		t.Fatal("lost established key silently replaced")
	}
}
func TestKeyCorruptionAndPermissionsFailClosed(t *testing.T) {
	dir := privateTestDirectory(t)
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = canonical
	}
	path := filepath.Join(dir, "private", "key")
	if _, err := Init(path); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)
	cases := [][]byte{nil, []byte("garbage"), append(append([]byte{}, good...), 0), bytes.Replace(good, []byte(fileMagic), []byte("unknown-format\n"), 1)}
	altered := append([]byte{}, good...)
	if altered[len(fileMagic)] == '0' {
		altered[len(fileMagic)] = '1'
	} else {
		altered[len(fileMagic)] = '0'
	}
	cases = append(cases, altered)
	for _, bad := range cases {
		os.WriteFile(path, bad, 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("corrupt key accepted")
		}
		if _, err := Init(path); err == nil {
			t.Fatal("corrupt established key replaced")
		}
	}
	os.WriteFile(path, good, 0600)
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0644)
		if _, err := Load(path); err == nil {
			t.Fatal("public-readable key accepted")
		}
		os.Chmod(path, 0400)
		if _, err := Load(path); err != nil {
			t.Fatal("read-only provisioned key rejected", err)
		}
		os.Chmod(path, 0600)
		os.Chmod(dir, 0777)
		if _, err := Load(path); err == nil {
			t.Fatal("writable key directory accepted")
		}
		os.Chmod(dir, 0700)
		link := filepath.Join(dir, "link")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(link); err == nil {
			t.Fatal("symlink key accepted")
		}
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("directory accepted as key")
	}
	blocker := filepath.Join(dir, "blocker")
	os.WriteFile(blocker, []byte("file"), 0600)
	if _, err := Init(filepath.Join(blocker, "key")); err == nil {
		t.Fatal("invalid parent accepted")
	}
}
func TestDefaultPathIsIndependentOfConfigDirectory(t *testing.T) {
	dir := privateTestDirectory(t)
	explicit := filepath.Join(dir, "private", "key")
	t.Setenv(Environment, explicit)
	p, err := DefaultPath()
	if err != nil || p != explicit {
		t.Fatal("explicit key path ignored")
	}
	if _, err := LoadDefault(); err == nil {
		t.Fatal("LoadDefault initialized key")
	}
	if _, err := Init(explicit); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDefault(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(Environment, "")
	t.Setenv("XDG_STATE_HOME", dir)
	p, err = DefaultPath()
	if err != nil || !filepath.IsAbs(p) {
		t.Fatal("no absolute platform default")
	}
}

// Each interrupted initialization must leave no installed partial key. A later
// explicit initializer can publish; a caller never uses its unpublished key.
func TestInterruptedInitializationNeverPublishesPartialKey(t *testing.T) {
	failure := errors.New("synthetic IO failure: do not expose details")
	for _, phase := range []string{"inspect", "mkdir", "check", "temp", "protect", "random", "write", "publish"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(privateTestDirectory(t), "private", "key")
			op := initOperations{os.Lstat, makePrivateDirectory, checkDirectory, os.CreateTemp, protectFile, Random, writeProtectedKey, os.Link}
			switch phase {
			case "inspect":
				op.inspect = func(string) (os.FileInfo, error) { return nil, failure }
			case "mkdir":
				op.mkdir = func(string) error { return failure }
			case "check":
				op.checkDir = func(string) error { return failure }
			case "temp":
				op.temp = func(string, string) (*os.File, error) { return nil, failure }
			case "protect":
				op.protect = func(*os.File) error { return failure }
			case "random":
				op.random = func() (*Context, error) { return nil, failure }
			case "write":
				op.write = func(f *os.File, b []byte) error { _, _ = f.Write(b[:len(b)/2]); return failure }
			case "publish":
				op.publish = func(string, string) error { return failure }
			}
			if c, err := initKey(path, op); err == nil || c != nil || (phase != "check" && strings.Contains(err.Error(), failure.Error())) {
				t.Fatal("interrupted initialization did not fail safely")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("partial key was installed")
			}
			if _, err := Init(path); err != nil {
				t.Fatal("explicit retry cannot recover", err)
			}
		})
	}
}

type failedKeyWriter struct {
	phase          string
	synced, closed bool
}

func (f *failedKeyWriter) Write(b []byte) (int, error) {
	if f.phase == "write" {
		return 0, io.ErrClosedPipe
	}
	if f.phase == "short" {
		return len(b) - 1, nil
	}
	return len(b), nil
}
func (f *failedKeyWriter) Sync() error {
	f.synced = true
	if f.phase == "sync" {
		return io.ErrClosedPipe
	}
	return nil
}
func (f *failedKeyWriter) Close() error {
	f.closed = true
	if f.phase == "close" {
		return io.ErrClosedPipe
	}
	return nil
}
func TestProtectedKeyWriteRequiresFullWriteSyncAndClose(t *testing.T) {
	for _, phase := range []string{"write", "short", "sync", "close", "success"} {
		f := &failedKeyWriter{phase: phase}
		err := writeProtectedKeyTo(f, []byte("synthetic"))
		if (err == nil) != (phase == "success") {
			t.Fatal("durable write error lost")
		}
		if phase == "success" && (!f.synced || !f.closed) {
			t.Fatal("key publication allowed without durable close")
		}
	}
}

func TestDefaultPathMissingHomeFailsWithoutFallback(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("Unix home fallback")
	}
	t.Setenv(Environment, "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("missing private home accepted")
	}
	if _, err := LoadDefault(); err == nil {
		t.Fatal("default failure ignored")
	}
}
func TestKeyEncodingCorruptionAndParentLinks(t *testing.T) {
	path := filepath.Join(privateTestDirectory(t), "private", "key")
	if _, err := Init(path); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)
	for _, at := range []int{len(fileMagic), len(fileMagic) + 64, len(good) - 1} {
		bad := append([]byte{}, good...)
		bad[at] = 'z'
		os.WriteFile(path, bad, 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("invalid framing accepted")
		}
	}
	os.WriteFile(path, good, 0600)
	if runtime.GOOS != "windows" {
		alias := filepath.Join(privateTestDirectory(t), "alias")
		if err := os.Symlink(filepath.Dir(path), alias); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(filepath.Join(alias, "key")); err == nil {
			t.Fatal("symlink parent accepted")
		}
	}
}

func privateTestDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

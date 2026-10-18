// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package versionid

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const Environment = "JUL_EFFECTIVE_VERSION_KEY_FILE"
const fileMagic = "jul-effective-version-key-v1\n"

func DefaultPath() (string, error) {
	if path := os.Getenv(Environment); path != "" {
		return filepath.Abs(path)
	}
	var root string
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", errors.New("cannot locate private version-key directory")
		}
		root = dir
	} else {
		root = os.Getenv("XDG_STATE_HOME")
		if root == "" || !filepath.IsAbs(root) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errors.New("cannot locate private version-key directory")
			}
			root = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(root, "jul", "effective-version.key"), nil
}

func LoadDefault() (*Context, error) {
	p, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return Load(p)
}

// Load never creates or repairs a key. A missing established key is therefore
// never silently replaced. All key IO completes before listeners start.
func Load(path string) (*Context, error) {
	fail := func() (*Context, error) {
		return nil, fmt.Errorf("effective version key unavailable or unprotected at %q; provision with jul version-key init or restore the original protected key", path)
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return fail()
	}
	if err := checkDirectory(filepath.Dir(path)); err != nil {
		return fail()
	}
	f, err := os.Open(path)
	if err != nil {
		return fail()
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return fail()
	}
	if err := checkFile(f, info); err != nil {
		return fail()
	}
	data, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return fail()
	}
	if len(data) != len(fileMagic)+98 || !strings.HasPrefix(string(data), fileMagic) || data[len(data)-1] != '\n' {
		return fail()
	}
	key, err := hex.DecodeString(string(data[len(fileMagic) : len(fileMagic)+64]))
	if err != nil {
		return fail()
	}
	c, err := New(key)
	if err == nil && (data[len(fileMagic)+64] != '\n' || string(data[len(fileMagic)+65:len(data)-1]) != c.ID()) {
		return fail()
	}
	if err != nil {
		return fail()
	}
	return c, nil
}

// Init publishes a fully written protected key without replacement. Private
// interrupted temporary files are ignored. Competing initializers all load the
// winner rather than use their own uncommitted key. Hard-link publication is
// required; unsupported filesystems fail explicitly and may provision a key.
type initOperations struct {
	inspect  func(string) (os.FileInfo, error)
	mkdir    func(string) error
	checkDir func(string) error
	temp     func(string, string) (*os.File, error)
	protect  func(*os.File) error
	random   func() (*Context, error)
	write    func(*os.File, []byte) error
	publish  func(string, string) error
}

func Init(path string) (*Context, error) {
	return initKey(path, initOperations{os.Lstat, makePrivateDirectory, checkDirectory, os.CreateTemp, protectFile, Random, writeProtectedKey, os.Link})
}

// Operations are per-call, so fault tests never mutate global filesystem hooks
// while concurrent initializers/readers are running.
func initKey(path string, op initOperations) (*Context, error) {
	if _, err := op.inspect(path); err == nil {
		return Load(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect effective version key destination")
	}
	dir := filepath.Dir(path)
	if err := op.mkdir(dir); err != nil {
		return nil, errors.New("cannot create private effective version key directory")
	}
	if err := op.checkDir(dir); err != nil {
		return nil, errors.New("effective version key directory is not private")
	}
	f, err := op.temp(dir, ".effective-version-key-*")
	if err != nil {
		return nil, errors.New("cannot create private version key temporary file")
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := op.protect(f); err != nil {
		f.Close()
		return nil, errors.New("cannot protect effective version key temporary file")
	}
	defer f.Close()
	c, err := op.random()
	if err != nil {
		return nil, errors.New("cannot generate effective version key")
	}
	data := []byte(fileMagic + hex.EncodeToString(c.key[:]) + "\n" + c.ID() + "\n")
	if err := op.write(f, data); err != nil {
		return nil, errors.New("cannot durably write protected effective version key")
	}
	if err := op.publish(temporary, path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errors.New("cannot atomically publish effective version key without replacement")
	}
	// Directory sync is unsupported on some platforms (including Windows).
	// Atomic visibility still holds there; backup durability follows the filesystem.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return Load(path)
}

// Keep the secret on the original protected handle through durable write.
// atomicfile.Write replaces the handle and its explicit Windows DACL.
type durableKeyWriter interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func writeProtectedKeyTo(f durableKeyWriter, data []byte) error {
	if n, err := f.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
func writeProtectedKey(f *os.File, data []byte) error { return writeProtectedKeyTo(f, data) }

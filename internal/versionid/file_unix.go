// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !windows

package versionid

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func checkDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(absolute)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("unsafe key directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
			return errors.New("untrusted key directory owner")
		}
		// A trusted-owner sticky ancestor (e.g. /tmp) cannot replace our private
		// owned child. Non-sticky group/world-writable ancestors are refused.
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errors.New("unsafe writable key ancestor")
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return nil
		}
		absolute = parent
	}
}
func makePrivateDirectory(path string) error { return os.MkdirAll(path, 0o700) }

func checkFile(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm()&0o077 != 0 || !ok || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		return errors.New("unsafe key permissions or owner")
	}
	return nil
}
func protectFile(f *os.File) error { return f.Chmod(0o600) }

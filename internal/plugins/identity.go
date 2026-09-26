// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package plugins

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"jul/internal/config"
)

// MaxModuleBytes bounds how many bytes of one WASM module Jul reads into
// memory, for path and inline sources alike.
const MaxModuleBytes = 128 << 20

// moduleByteLimit is MaxModuleBytes; tests lower it to exercise the bound.
var moduleByteLimit = MaxModuleBytes

// wasmHeader is the WebAssembly binary magic ("\0asm") and version 1.
var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// Module source kinds reported in ModuleIdentity.Source.
const (
	SourcePath   = "path"
	SourceInline = "inline"
)

// ModuleIdentity is the exact content identity of the module bytes Jul
// compiles for one plugin declaration. It is metadata, not a secret, and never
// carries module bytes or the configured path.
type ModuleIdentity struct {
	Source string `json:"source"`
	// Digest is "sha256:" followed by 64 lowercase hex digits of the bytes.
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Pinned bool   `json:"pinned"`
}

// ShortDigest is the first 12 hex digits of the digest, for display.
func (id ModuleIdentity) ShortDigest() string {
	return ShortDigest(id.Digest)
}

// ShortDigest shortens a "sha256:<hex>" digest to its first 12 hex digits.
func ShortDigest(digest string) string {
	hexPart := strings.TrimPrefix(digest, "sha256:")
	if len(hexPart) > 12 {
		hexPart = hexPart[:12]
	}
	return hexPart
}

// ErrDigestMismatch reports module bytes that differ from the configured pin.
var ErrDigestMismatch = errors.New("module sha256 does not match the configured pin")

// Module is one snapshot of a plugin's module bytes and their identity. The
// bytes are read exactly once; the digest, the pin check and compilation all
// use this same slice, so a file rewritten after the read cannot be compiled
// under an identity it does not have.
type Module struct {
	Bytes    []byte
	Identity ModuleIdentity
}

// ReadModule resolves a plugin declaration's module source, reads at most
// MaxModuleBytes once, validates the WebAssembly header, digests the exact
// bytes and verifies the optional sha256 pin.
func ReadModule(pc config.PluginConfig) (Module, error) {
	var (
		wasm   []byte
		source string
		err    error
	)
	switch {
	case pc.Path != "":
		source = SourcePath
		wasm, err = readModuleFile(pc.Path)
	case pc.Inline != "":
		source = SourceInline
		wasm, err = decodeInlineModule(pc.Inline)
	default:
		err = errors.New("no module source (set path or inline)")
	}
	if err != nil {
		return Module{}, err
	}
	if len(wasm) < len(wasmHeader) || !bytes.Equal(wasm[:len(wasmHeader)], wasmHeader) {
		return Module{}, errors.New("module is not a WebAssembly version 1 binary")
	}
	sum := sha256.Sum256(wasm)
	pin, err := config.ParseSHA256Pin(pc.SHA256)
	if err != nil {
		return Module{}, err
	}
	observed := hex.EncodeToString(sum[:])
	if pin != "" && subtle.ConstantTimeCompare([]byte(pin), []byte(observed)) != 1 {
		return Module{}, fmt.Errorf("%w: pinned sha256:%s, observed sha256:%s", ErrDigestMismatch, pin, observed)
	}
	return Module{
		Bytes: wasm,
		Identity: ModuleIdentity{
			Source: source,
			Digest: "sha256:" + observed,
			Size:   int64(len(wasm)),
			Pinned: pin != "",
		},
	}, nil
}

func readModuleFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(moduleByteLimit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > moduleByteLimit {
		return nil, fmt.Errorf("module exceeds %d bytes", moduleByteLimit)
	}
	return b, nil
}

func decodeInlineModule(inline string) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(inline)) > moduleByteLimit+2 {
		return nil, fmt.Errorf("inline module exceeds %d bytes", moduleByteLimit)
	}
	b, err := base64.StdEncoding.DecodeString(inline)
	if err != nil {
		return nil, fmt.Errorf("inline module is not valid base64: %w", err)
	}
	if len(b) > moduleByteLimit {
		return nil, fmt.Errorf("inline module exceeds %d bytes", moduleByteLimit)
	}
	return b, nil
}

// SameModules reports whether live proves that every plugin in cfg would
// compile exactly the bytes already serving: same plugin set, and for each
// plugin a fresh ReadModule whose digest equals the live one. Any read or
// validation failure is not proof. It never compiles anything.
func SameModules(live map[string]ModuleIdentity, cfg map[string]config.PluginConfig) bool {
	if len(live) != len(cfg) {
		return false
	}
	for name, pc := range cfg {
		want, ok := live[name]
		if !ok {
			return false
		}
		m, err := ReadModule(pc)
		if err != nil || m.Identity != want {
			return false
		}
	}
	return true
}

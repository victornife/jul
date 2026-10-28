// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

func TestInstrumentedVaryCookieGuest(t *testing.T) {
	profile, configured := os.LookupEnv("JUL_GUEST_COVERPROFILE")
	if !configured {
		t.Skip("guest coverage profile was not requested")
	}
	if profile == "" || !filepath.IsAbs(profile) {
		t.Fatal("JUL_GUEST_COVERPROFILE must be an absolute path")
	}

	dir := t.TempDir()
	guest := filepath.Join(dir, "guest.test.wasm")
	cmd := exec.CommandContext(t.Context(), "go", "test", "-c", "-covermode=set", "-coverpkg=./testguest-v2", "-o", guest, "./testguest-v2")
	cmd.Dir = filepath.Join("..", "..", "examples", "plugins")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build instrumented guest: %v\n%s", err, out)
	}
	binary, err := os.ReadFile(guest)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	runtime := wazero.NewRuntime(ctx)
	defer runtime.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	compiled, err := runtime.CompileModule(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	builder := runtime.NewHostModuleBuilder("jul-abi/v2")
	seen := map[string]bool{}
	var added, preset bool
	var requestHeaderCalls int
	for _, def := range compiled.ImportedFunctions() {
		module, name, imported := def.Import()
		if !imported || module != "jul-abi/v2" || seen[name] {
			continue
		}
		seen[name] = true
		builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(func(_ context.Context, mod api.Module, stack []uint64) {
			switch name {
			case "get_request_header":
				header, ok := mod.Memory().Read(uint32(stack[0]), uint32(stack[1]))
				if !ok || string(header) != "X-Op" {
					panic("unexpected guest request header")
				}
				value := []byte("vary-cookie")
				requestHeaderCalls++
				if requestHeaderCalls == 2 {
					value = []byte("req-preset-vary")
				}
				if uint32(stack[3]) < uint32(len(value)) || !mod.Memory().Write(uint32(stack[2]), value) {
					panic("guest request-header buffer is too small")
				}
				stack[0] = uint64(len(value))
			case "get_config":
				stack[0] = 0
			case "resp_add_header":
				header, ok := mod.Memory().Read(uint32(stack[0]), uint32(stack[1]))
				if !ok {
					panic("invalid guest response header")
				}
				value, ok := mod.Memory().Read(uint32(stack[2]), uint32(stack[3]))
				if !ok || string(header) != "Vary" || string(value) != "Cookie" {
					panic("unexpected guest response header")
				}
				added = true
				stack[0] = 0
			case "set_response_header":
				header, ok := mod.Memory().Read(uint32(stack[0]), uint32(stack[1]))
				if !ok {
					panic("invalid guest preset header")
				}
				value, ok := mod.Memory().Read(uint32(stack[2]), uint32(stack[3]))
				if !ok || string(header) != "Vary" || string(value) != "X-Fixture-Identity" {
					panic("unexpected guest preset header")
				}
				preset = true
			default:
				panic(fmt.Sprintf("unexpected guest ABI call %q", name))
			}
		}), def.ParamTypes(), def.ResultTypes()).Export(name)
	}
	if _, err := builder.Instantiate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := wazero.NewModuleConfig().
		WithArgs("guest.test", "-test.v", "-test.coverprofile=/coverage/guest.cover").
		WithFSConfig(wazero.NewFSConfig().WithDirMount(dir, "/coverage")).
		WithEnv("TMPDIR", "/coverage").
		WithStdout(os.Stdout).
		WithStderr(os.Stderr)
	_, err = runtime.InstantiateModule(ctx, compiled, cfg)
	var exit *sys.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 0) {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("guest did not emit Vary: Cookie")
	}
	if !preset {
		t.Fatal("guest did not preset Vary in the request phase")
	}
	coverage, err := os.ReadFile(filepath.Join(dir, "guest.cover"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, coverage, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package wasmtest

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
)

func TestBodyReaderV2(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		bodyLength, stopStatus, action uint32
	}{
		{"empty-continue", 0, 0, 1},
		{"nonempty-stop", 1, 0, 0},
		{"success-stop", 0, 200, 0},
		{"informational-stop", 0, 103, 0},
		{"denial-stop", 0, 403, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rt := wazero.NewRuntime(ctx)
			t.Cleanup(func() {
				if err := rt.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			var reads, status uint32
			_, err := rt.NewHostModuleBuilder("jul-abi/v2").
				NewFunctionBuilder().WithFunc(func(buf, limit uint32) uint32 {
				if buf != 0 || limit != 32 {
					t.Errorf("unexpected read buffer: %d/%d", buf, limit)
				}
				reads++
				return tc.bodyLength
			}).Export("read_request_body").
				NewFunctionBuilder().WithFunc(func(code uint32) { status = code }).Export("set_response_status").
				Instantiate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			module, err := rt.Instantiate(ctx, BodyReaderV2(tc.stopStatus))
			if err != nil {
				t.Fatal(err)
			}
			result, err := module.ExportedFunction("handle_request").Call(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(result) != 1 || result[0] != uint64(tc.action) || reads != 1 || status != tc.stopStatus {
				t.Fatalf("action=%v reads=%d status=%d, want %d/1/%d", result, reads, status, tc.action, tc.stopStatus)
			}
		})
	}
}

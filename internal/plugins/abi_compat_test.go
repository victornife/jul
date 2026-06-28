//go:build wasmplugins

package plugins

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
)

// goldenABIPath holds the pinned jul-abi/v1 host-function surface. The
// compatibility policy is additive-only within v1: a breaking change (renaming
// or re-typing a function) must bump to a new ABI id, so this golden must only
// ever grow. Regenerate intentionally with -update after an additive change.
const goldenABIPath = "../../testdata/plugins/abi-v1.golden"

func currentABISurface(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = r.Close(ctx) })
	p := &plugin{name: "golden", kvKeys: map[string]int{}}
	if err := registerJulHostModule(ctx, r, p); err != nil {
		t.Fatalf("register host module: %v", err)
	}
	mod := r.Module("jul")
	if mod == nil {
		t.Fatal("jul host module not instantiated")
	}
	var lines []string
	for name, d := range mod.ExportedFunctionDefinitions() {
		lines = append(lines, fmt.Sprintf("%s(%d)->%d", name, len(d.ParamTypes()), len(d.ResultTypes())))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func TestABIV1Golden(t *testing.T) {
	got := currentABISurface(t)
	if os.Getenv("UPDATE_ABI_GOLDEN") == "1" {
		if err := os.WriteFile(goldenABIPath, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenABIPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_ABI_GOLDEN=1 to seed): %v", err)
	}
	if got != string(want) {
		t.Fatalf("jul-abi/v1 host surface changed.\n--- got\n%s--- want\n%s\nAdditive changes require UPDATE_ABI_GOLDEN=1; renames/retypes require a new ABI id.", got, want)
	}
}

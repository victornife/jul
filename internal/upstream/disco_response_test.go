package upstream

import (
	"strings"
	"testing"
)

func TestDiscoveryResponseRejectsOversizedValidPrefix(t *testing.T) {
	var got map[string]any
	if err := decodeDiscoveryResponse(strings.NewReader(`{"items":[]}`+strings.Repeat(" ", maxDiscoveryResponseBytes)), &got); err == nil {
		t.Fatal("oversized discovery response accepted")
	}
	if err := decodeDiscoveryResponse(strings.NewReader(`{"items":[]}{"items":[]}`), &got); err == nil {
		t.Fatal("trailing discovery document accepted")
	}
}

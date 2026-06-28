package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestApplyMasksRegistered(t *testing.T) {
	Add("super-secret-value")
	got := Apply("the token is super-secret-value here")
	if strings.Contains(got, "super-secret-value") {
		t.Errorf("secret not masked: %q", got)
	}
	if !strings.Contains(got, Mask) {
		t.Errorf("mask missing: %q", got)
	}
}

func TestAddIgnoresShortValues(t *testing.T) {
	before := Count()
	Add("ab")
	Add("")
	if Count() != before {
		t.Errorf("short/empty values were registered")
	}
}

func TestWriterMasks(t *testing.T) {
	Add("writer-secret-token")
	var buf bytes.Buffer
	w := Writer(&buf)
	in := []byte("line with writer-secret-token in it\n")
	n, err := w.Write(in)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(in) {
		t.Errorf("n = %d, want %d (caller's view of bytes)", n, len(in))
	}
	if strings.Contains(buf.String(), "writer-secret-token") {
		t.Errorf("writer did not mask: %q", buf.String())
	}
}

func TestApplyNoSecretsIsIdentity(t *testing.T) {
	// Not asserting global emptiness (other tests register secrets); just that a
	// string with no registered secret is returned unchanged.
	const s = "no-registered-secret-here-zzz"
	if Apply(s) != s {
		t.Errorf("Apply altered an unrelated string")
	}
}

func TestSetMinLen(t *testing.T) {
	// The registry is a process-global; restore the default floor so later tests
	// in this package keep the documented behavior.
	defer SetMinLen(DefaultMinLen)

	// At the default floor a value shorter than DefaultMinLen is not registered.
	SetMinLen(DefaultMinLen)
	Add("Xq7") // 3 chars, below the default floor of 4
	if strings.Contains(Apply("a Xq7 b"), Mask) {
		t.Errorf("short value masked at the default floor")
	}

	// Lowering the floor lets a shorter secret register and mask.
	SetMinLen(3)
	Add("Xq7")
	if got := Apply("a Xq7 b"); !strings.Contains(got, Mask) || strings.Contains(got, "Xq7") {
		t.Errorf("lowered floor did not mask short secret: %q", got)
	}

	// A value below 1 restores the default floor.
	SetMinLen(0)
	Add("Yw9") // 3 chars again
	if strings.Contains(Apply("a Yw9 b"), Mask) {
		t.Errorf("SetMinLen(0) did not restore the default floor")
	}
}

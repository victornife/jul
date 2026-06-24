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

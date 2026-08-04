// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that unmarshals from TOML strings such as
// "30s", "5m", "1h30m". An empty string decodes to zero.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler for go-toml.
func (d *Duration) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalText implements encoding.TextMarshaler so durations round-trip back to
// TOML as human-readable strings (e.g. "30s"). A zero duration marshals to "0s".
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Size is a byte count that unmarshals from TOML strings with optional unit
// suffixes: b, k/kb, m/mb, g/gb (case-insensitive, base 1024). A bare number
// is interpreted as bytes. An empty string decodes to zero.
type Size int64

// UnmarshalText implements encoding.TextUnmarshaler for go-toml.
func (s *Size) UnmarshalText(text []byte) error {
	raw := strings.TrimSpace(strings.ToLower(string(text)))
	if raw == "" {
		*s = 0
		return nil
	}

	var mult int64 = 1
	switch {
	case strings.HasSuffix(raw, "gb"):
		mult, raw = 1<<30, strings.TrimSuffix(raw, "gb")
	case strings.HasSuffix(raw, "mb"):
		mult, raw = 1<<20, strings.TrimSuffix(raw, "mb")
	case strings.HasSuffix(raw, "kb"):
		mult, raw = 1<<10, strings.TrimSuffix(raw, "kb")
	case strings.HasSuffix(raw, "g"):
		mult, raw = 1<<30, strings.TrimSuffix(raw, "g")
	case strings.HasSuffix(raw, "m"):
		mult, raw = 1<<20, strings.TrimSuffix(raw, "m")
	case strings.HasSuffix(raw, "k"):
		mult, raw = 1<<10, strings.TrimSuffix(raw, "k")
	case strings.HasSuffix(raw, "b"):
		mult, raw = 1, strings.TrimSuffix(raw, "b")
	}

	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", string(text), err)
	}
	if n < 0 {
		return fmt.Errorf("invalid size %q: must not be negative", string(text))
	}
	if n > math.MaxInt64/mult {
		return fmt.Errorf("invalid size %q: value overflows int64 bytes", string(text))
	}
	*s = Size(n * mult)
	return nil
}

// Bytes returns the value as an int64 byte count.
func (s Size) Bytes() int64 { return int64(s) }

// MarshalText implements encoding.TextMarshaler so sizes round-trip back to TOML
// as strings using the largest exact base-1024 unit (e.g. "64m"), falling back
// to a bare byte count when no unit divides evenly.
func (s Size) MarshalText() ([]byte, error) {
	n := int64(s)
	if n == 0 {
		return []byte("0"), nil
	}
	units := []struct {
		suffix string
		size   int64
	}{
		{"g", 1 << 30},
		{"m", 1 << 20},
		{"k", 1 << 10},
	}
	for _, u := range units {
		if n%u.size == 0 {
			return []byte(strconv.FormatInt(n/u.size, 10) + u.suffix), nil
		}
	}
	return []byte(strconv.FormatInt(n, 10)), nil
}

// String returns a human-readable size string (e.g. "64m") or "0" for zero.
func (s Size) String() string {
	b, _ := s.MarshalText()
	return string(b)
}

// UnmarshalText lets an upstream server be written as a bare address string
// ("127.0.0.1:3000") with an optional weight suffix
// ("127.0.0.1:3000 weight=5"). Using TextUnmarshaler keeps decoding on the
// stable go-toml path (array-of-strings) rather than the unstable custom
// unmarshaler interface.
func (u *UpstreamServer) UnmarshalText(text []byte) error {
	fields := strings.Fields(string(text))
	if len(fields) == 0 {
		return fmt.Errorf("upstream server address is empty")
	}
	u.Address = fields[0]
	u.Weight = 1
	for _, f := range fields[1:] {
		key, val, ok := strings.Cut(f, "=")
		if !ok || key != "weight" {
			return fmt.Errorf("invalid upstream server option %q (want weight=N)", f)
		}
		w, err := strconv.Atoi(val)
		if err != nil || w < 1 {
			return fmt.Errorf("invalid upstream server weight %q: must be a positive integer", val)
		}
		u.Weight = w
	}
	return nil
}

// MarshalText implements encoding.TextMarshaler so an upstream server marshals
// back to its bare-address form, appending "weight=N" only for non-default
// weights.
func (u UpstreamServer) MarshalText() ([]byte, error) {
	if u.Weight > 1 {
		return []byte(fmt.Sprintf("%s weight=%d", u.Address, u.Weight)), nil
	}
	return []byte(u.Address), nil
}

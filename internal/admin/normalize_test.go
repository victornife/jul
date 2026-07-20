// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"reflect"
	"testing"
)

func TestNormalizeStringSlice(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"only blanks", []string{"", "  ", "\t"}, nil},
		{"trim and drop blanks", []string{" a ", "", "b\n", "  "}, []string{"a", "b"}},
		{"deduplicate preserving order", []string{"a", " a", "b", "a", "b", "c"}, []string{"a", "b", "c"}},
		{"no change", []string{"x", "y"}, []string{"x", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeStringSlice(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalizeStringSlice(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

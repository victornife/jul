// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !linux && !darwin && !freebsd && !windows

package storagefs

import "os"

// fsKey collapses every path into one group: nothing is measured here, so
// there is nothing to deduplicate.
type fsKey struct{}

func identity(string, os.FileInfo) (fsKey, error) { return fsKey{}, nil }

// statFS has no implementation on this platform; the result is reported as
// ReasonUnsupported, never as zero capacity.
var statFS = func(string) (Usage, error) { return Usage{}, errUnsupported }

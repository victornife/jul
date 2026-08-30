// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import "errors"

var (
	ErrCollectorUnimplemented = errors.New("support-bundle collector has no implementation")
	ErrUnsafeArtifactPath     = errors.New("support-bundle artifact path is unsafe")
	ErrDuplicateArtifact      = errors.New("support-bundle artifact path is duplicated")
	ErrTooManyArtifacts       = errors.New("support-bundle artifact count limit exceeded")
	ErrArtifactTooLarge       = errors.New("support-bundle artifact size limit exceeded")
	ErrBundleTooLarge         = errors.New("support-bundle uncompressed size limit exceeded")
	ErrArchiveTooLarge        = errors.New("support-bundle compressed size limit exceeded")
	ErrOutputExists           = errors.New("support-bundle output already exists")
	ErrUnsafeOutputPath       = errors.New("support-bundle output path is unsafe")
)

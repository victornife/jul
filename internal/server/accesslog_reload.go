// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"slices"

	"jul/internal/config"
)

// AccessLogRestartRequired reports whether moving from old to next changes the
// access-log configuration, which cannot take effect via hot reload. The
// access-log sinks are built once at startup (see
// observability.BuildAccessSinks): the "file" sink owns a rotating file handle
// and the "syslog" sink a system-log connection, so they persist across reloads
// and are torn down only at shutdown. A changed [observability.access_log]
// block would therefore persist to disk but keep writing through the sinks
// wired at startup until a restart. Mirroring TracingRestartRequired and
// ACMERestartRequired, the apply path rejects such a change with
// restart_required rather than accepting it silently, so the console's
// "applied" stays honest.
//
// AccessLogConfig is all comparable scalars except the Sinks slice, so the
// comparison is field-by-field with a slice equality for Sinks. This matches the
// equality check doReload uses for its defense-in-depth warning when a reload
// bypasses the admin gate (a direct file edit followed by SIGHUP).
func AccessLogRestartRequired(old, next *config.Config) (string, bool) {
	if accessLogEqual(old.Observability.AccessLog, next.Observability.AccessLog) {
		return "", false
	}
	return "access-log settings changed; the access-log sinks are built once at startup and take effect on restart", true
}

// accessLogEqual reports whether two access-log configurations are identical.
func accessLogEqual(a, b config.AccessLogConfig) bool {
	return slices.Equal(a.Sinks, b.Sinks) &&
		a.File == b.File &&
		a.Format == b.Format &&
		a.RotateMaxMB == b.RotateMaxMB &&
		a.RotateKeep == b.RotateKeep
}

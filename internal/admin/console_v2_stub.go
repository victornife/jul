//go:build !console

package admin

import "embed"

const consoleV2Compiled = false

// consoleV2Assets returns an empty filesystem for builds without the console
// build tag. The caller (routes) guards serving on consoleV2Compiled.
func consoleV2Assets() embed.FS { var empty embed.FS; return empty }

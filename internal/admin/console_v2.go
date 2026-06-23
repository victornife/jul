//go:build console

package admin

import "embed"

const consoleV2Compiled = true

// consoleV2FS holds the prebuilt Console v2 SPA bundle when the console build
// tag is enabled. The bundle is committed in the repo so go build/install and
// release workflows stay Node-free at runtime.
//
//go:embed assets/dist
var consoleV2FS embed.FS

// consoleV2Assets returns the embedded filesystem for the Console v2 SPA.
func consoleV2Assets() embed.FS { return consoleV2FS }

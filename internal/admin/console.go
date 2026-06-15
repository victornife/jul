//go:build console

package admin

import _ "embed"

// consoleCompiled reports that the web console UI was compiled into this build
// (binary built with -tags console).
const consoleCompiled = true

//go:embed assets/console.html
var consoleHTML string

// consolePage returns the embedded console dashboard shell.
func consolePage() string { return consoleHTML }

//go:build !console

package admin

// consoleCompiled reports whether the web console UI was compiled into this
// build. It is false unless the binary is built with -tags console.
const consoleCompiled = false

// consolePage returns the console dashboard HTML. In builds without the console
// tag it falls back to the configuration page; handleRoot never calls it in
// that case (it is gated on consoleCompiled), but the fallback keeps the
// function total and callable.
func consolePage() string { return configUIPage }

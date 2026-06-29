// egress-check is a Jul.IA middleware plugin that calls an allow-listed upstream
// before passing the request on, demonstrating the capability-gated `fetch` host
// function. The plugin must be granted `fetch = true` and a non-empty
// `allowed_hosts` list in its [plugins.NAME] config, or the fetch is blocked.
// The response status is mirrored into the X-Egress-Status header.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o egress-check.wasm .
package main

import (
	"strconv"

	"juliaplugins/sdk"
)

func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		status, _, err := sdk.Fetch("GET", "https://api.example.com/health", nil)
		if err != nil {
			req.SetResponseHeader("X-Egress-Status", "error")
			return sdk.Continue
		}
		req.SetResponseHeader("X-Egress-Status", strconv.Itoa(status))
		return sdk.Continue
	}
}

func main() {}

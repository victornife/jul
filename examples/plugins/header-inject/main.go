// header-inject is a Jul.IA middleware plugin that adds a response header and
// then passes the request to the next handler.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o header-inject.wasm .
package main

import "juliaplugins/sdk"

func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		req.SetResponseHeader("X-Plugin", "header-inject")
		sdk.Log(sdk.LevelInfo, "header-inject: added X-Plugin header")
		return sdk.Continue
	}
}

func main() {}

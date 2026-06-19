// request-block is a Jul.IA middleware plugin that rejects a request with 403
// when it carries the header "X-Block: 1", and otherwise passes it through. It
// demonstrates reading request headers and the body and writing a response from
// the guest.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o request-block.wasm .
package main

import "juliaplugins/sdk"

func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		if v, ok := req.Header("X-Block"); ok && v == "1" {
			sdk.SetResponseStatus(403)
			sdk.SetResponseHeader("Content-Type", "text/plain; charset=utf-8")
			sdk.WriteResponseBody([]byte("blocked by request-block plugin\n"))
			return sdk.Stop
		}
		return sdk.Continue
	}
}

func main() {}

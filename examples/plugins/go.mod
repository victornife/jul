// Module juliaplugins holds example WASM guest plugins for Jul.IA and the small
// guest SDK they share. It is a SEPARATE Go module (its own go.mod) so the main
// "jul" module's `go build ./...` never tries to compile these files for the
// host architecture — they use //go:wasmimport, which is only valid for
// GOARCH=wasm. Build each plugin with:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o NAME.wasm ./NAME
module juliaplugins

go 1.26.6

//go:build wasip1

// Package main is a test fixture: a WASM plugin whose pre_http hook never
// returns. Used to assert that the host's execution bounds unwind a
// runaway hook.
//
// Built by the test harness with the stdlib toolchain (Go 1.24+
// //go:wasmexport), so no TinyGo install is needed:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o spin.wasm ./main.go
package main

// spins is written on every iteration so the loop is not optimised away.
var spins uint64

//go:wasmexport pre_http
func preHTTP() int32 {
	for {
		spins++
	}
}

func main() {}

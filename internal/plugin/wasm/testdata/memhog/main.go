//go:build wasip1

// Package main is a test fixture: a WASM plugin whose pre_http hook
// allocates far past any sane memory cap. Used to assert that the host's
// page limit turns a runaway allocation into a clean hook error rather
// than host memory growth.
//
// Built by the test harness with the stdlib toolchain (Go 1.24+
// //go:wasmexport), so no TinyGo install is needed:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o memhog.wasm ./main.go
package main

// sink holds every allocation so nothing is collected.
var sink [][]byte

//go:wasmexport pre_http
func preHTTP() int32 {
	for i := 0; i < 4096; i++ {
		sink = append(sink, make([]byte, 1<<20))
	}
	return 0
}

func main() {}

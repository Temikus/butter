// Package wasmtest builds the WASM plugin fixtures used by the host's
// execution-bound tests. Fixtures are compiled with the stdlib toolchain
// (GOOS=wasip1, //go:wasmexport) so the tests run anywhere Go does — no
// TinyGo install required.
package wasmtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// buildMu serialises fixture builds so parallel tests sharing a fixture
// do not race on the cached output file.
var buildMu sync.Mutex

// Build compiles the fixture in testdata/<name> to a .wasm module and
// returns its path. Output is cached in the OS temp dir and rebuilt only
// when the fixture source is newer. The test is skipped when the Go
// toolchain is unavailable.
func Build(t *testing.T, name string) string {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not found, cannot build WASM fixture %q: %v", name, err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate wasmtest package directory")
	}
	src := filepath.Join(filepath.Dir(thisFile), "..", "testdata", name, "main.go")
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatalf("WASM fixture source %s: %v", src, err)
	}

	buildMu.Lock()
	defer buildMu.Unlock()

	outDir := filepath.Join(os.TempDir(), "butter-wasm-fixtures")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("creating fixture cache dir: %v", err)
	}
	out := filepath.Join(outDir, name+".wasm")

	if info, err := os.Stat(out); err == nil && info.ModTime().After(srcInfo.ModTime()) {
		return out
	}

	// Build to a unique temp file and rename into place: the cache dir is
	// shared across test binaries, so a concurrent process must never see a
	// half-written module (nor keep serving one from a crashed build, whose
	// mtime would defeat the staleness check above).
	tmp, err := os.CreateTemp(outDir, name+".*.wasm")
	if err != nil {
		t.Fatalf("creating fixture temp file: %v", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	//nolint:gosec // goBin comes from PATH; the remaining args are fixed and repo-local
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", tmpPath, src)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building WASM fixture %q: %v\n%s", name, err, output)
	}
	if err := os.Rename(tmpPath, out); err != nil {
		t.Fatalf("publishing WASM fixture %q: %v", name, err)
	}
	return out
}

package taskstest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// WriteFile creates path's parent directory (mode dirPermFor(perm)) on fs and
// then writes data through iox.WriteFileAtomicFs — the same MkdirAll-then-
// atomic-write sequence production writers use (e.g.
// internal/operations/signer.go#appendAllowedSignersLine).
//
// A fixture calling afero.WriteFile directly does not create parents, which
// only stays invisible on a MemMapFs (it auto-creates them); the moment the
// backing fs is a real one or a BasePathFs, the fixture fails on a path
// production writes fine. That mismatch — the fixture and the subject
// disagreeing about what "write a file" means — is what this helper closes.
//
// It fails the test immediately (t.Helper + t.Fatalf) on either step's error:
// a fixture that half-writes is worse than one that stops, since the test
// body that follows would otherwise run against filesystem state nobody
// chose.
//
// The canonical body lives here rather than internal/testsupport (which
// delegates to it) for the same reason Isolate/ProjectDir/ChangeDir's bodies
// do: internal/shared is self-contained and must never import testsupport,
// while testsupport may import shared — one body, no duplicate.
func WriteFile(t testing.TB, fs afero.Fs, path string, data []byte, perm os.FileMode) {
	t.Helper()
	writeFile(t, fs, path, data, perm)
}

// fatalReporter is the part of testing.TB writeFile needs to report a
// failure. It exists — rather than writeFile taking testing.TB directly —
// for the same reason appDirReporter does (see requireIsolatedAppDir): a
// check that can only report by failing the test that calls it cannot be
// asserted ON, and testing.TB cannot be implemented outside the testing
// package (its unexported method blocks exactly that). A recorder standing
// in for fatalReporter lets writefile_test.go prove WriteFile stops on error
// without the proving test itself going red.
type fatalReporter interface {
	Helper()
	Fatalf(format string, args ...any)
}

// writeFile is WriteFile's body, over the narrower fatalReporter so it is
// callable from a test with a recorder in place of t. testing.TB satisfies
// fatalReporter structurally, so WriteFile's callers need nothing extra.
func writeFile(t fatalReporter, fs afero.Fs, path string, data []byte, perm os.FileMode) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, dirPermFor(perm)); err != nil {
		t.Fatalf("taskstest.WriteFile: mkdir %s: %v", dir, err)
	}
	if err := iox.WriteFileAtomicFs(fs, path, data, perm); err != nil {
		t.Fatalf("taskstest.WriteFile: write %s: %v", path, err)
	}
}

// WriteFileString is WriteFile for a string payload — the common case in test
// fixtures that build a YAML or text body rather than raw bytes.
func WriteFileString(t testing.TB, fs afero.Fs, path, content string, perm os.FileMode) {
	t.Helper()
	WriteFile(t, fs, path, []byte(content), perm)
}

// SeedTree writes every entry in files (a path relative to root, mapped to
// its content) through WriteFile, creating whatever nested directories each
// entry needs. It is the shape roughly forty fixtures across the repo
// currently re-implement inline as a per-file afero.WriteFile loop.
//
// Every file is written 0o644: SeedTree is for ordinary fixture content, not
// the handful of private (0o600) stores that need WriteFile called directly.
func SeedTree(t testing.TB, fs afero.Fs, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		WriteFileString(t, fs, filepath.Join(root, filepath.FromSlash(rel)), content, 0o644)
	}
}

// dirPermFor derives a parent-directory mode from the file mode being
// written there: 0o600 — the private-store convention (e.g.
// appendAllowedSignersLine's allowed_signers write) — pairs with 0o700, and
// every other mode pairs with 0o755. This mirrors that existing pairing
// rather than adding a fourth mode spelling to a repo that already has three.
func dirPermFor(perm os.FileMode) os.FileMode {
	if perm == 0o600 {
		return 0o700
	}
	return 0o755
}

package selfexec

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubExecutable / stubStat install temporary overrides for the Path seams
// and restore them on test cleanup.
func stubExecutable(t *testing.T, path string, err error) {
	t.Helper()
	orig := osExecutable
	osExecutable = func() (string, error) { return path, err }
	t.Cleanup(func() { osExecutable = orig })
}

func stubStat(t *testing.T, fn func(string) (os.FileInfo, error)) {
	t.Helper()
	orig := osStat
	osStat = fn
	t.Cleanup(func() { osStat = orig })
}

func TestPath_HappyPath(t *testing.T) {
	stubExecutable(t, "/usr/local/bin/ctxloom", nil)
	stubStat(t, func(string) (os.FileInfo, error) { return nil, nil })

	assert.Equal(t, "/usr/local/bin/ctxloom", Path())
}

func TestPath_OSExecutableError(t *testing.T) {
	// When os.Executable itself errors (rare; AIX, sandboxes), we fall
	// back to bare "ctxloom" so a PATH lookup can recover.
	stubExecutable(t, "", errors.New("not supported"))
	// osStat should NOT be consulted on this branch — install a stub
	// that fails loudly if it is.
	stubStat(t, func(p string) (os.FileInfo, error) {
		t.Fatalf("osStat must not be called when osExecutable errors; got %q", p)
		return nil, nil
	})

	assert.Equal(t, "ctxloom", Path())
}

func TestPath_DeletedSuffixStripped(t *testing.T) {
	// Linux returns "/path/ctxloom (deleted)" after the running inode is
	// unlinked (typical `go install` upgrade). We must strip the suffix
	// before stat'ing — and if the stripped path still exists, use it.
	stubExecutable(t, "/old/ctxloom (deleted)", nil)
	var statted string
	stubStat(t, func(p string) (os.FileInfo, error) {
		statted = p
		return nil, nil // pretend the stripped path exists
	})

	got := Path()
	assert.Equal(t, "/old/ctxloom", got)
	assert.Equal(t, "/old/ctxloom", statted, "stat must run against the stripped path, not the original")
}

func TestPath_DeletedAndMissingFallsBack(t *testing.T) {
	// Stripped path also doesn't exist (the new binary lives at a
	// different location) → bare "ctxloom" + PATH lookup.
	stubExecutable(t, "/old/ctxloom (deleted)", nil)
	stubStat(t, func(string) (os.FileInfo, error) {
		return nil, errors.New("no such file")
	})

	assert.Equal(t, "ctxloom", Path())
}

func TestPath_PathMissingFallsBack(t *testing.T) {
	// No "(deleted)" suffix, but the path doesn't exist either. Could
	// happen if the binary was moved across filesystems. Fall back.
	stubExecutable(t, "/where/did/it/go", nil)
	stubStat(t, func(string) (os.FileInfo, error) {
		return nil, errors.New("not found")
	})

	assert.Equal(t, "ctxloom", Path())
}

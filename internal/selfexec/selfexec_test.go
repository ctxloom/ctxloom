package selfexec

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	assert.Equal(t, "ctxloom", Path())
	// U098-F01: a resolution failure used to be indistinguishable from a
	// correctly-resolved absolute path — agent.CtxloomCommand's own invariant
	// is that the bare-name fallback is a stated exception, not a silent one.
	assert.Contains(t, buf.String(), "could not resolve", "the fallback must be visible, not silent")
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

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	assert.Equal(t, "ctxloom", Path())
	// U098-F01: this fallback must be visible too, not just the osExecutable
	// error branch — both failures were mapped onto the same silent success
	// value before the fix.
	assert.Contains(t, buf.String(), "no longer points at a live file")
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

// TestSetPathForTesting_OverridesAndRestores proves the external test seam:
// while set, Path returns the override unconditionally (bypassing
// osExecutable/osStat entirely), and the returned restore func puts the real
// decision tree back.
func TestSetPathForTesting_OverridesAndRestores(t *testing.T) {
	stubExecutable(t, "/real/ctxloom", nil)
	stubStat(t, func(string) (os.FileInfo, error) { return nil, nil })
	assert.Equal(t, "/real/ctxloom", Path(), "sanity: real decision tree wired")

	restore := SetPathForTesting("ctxloom")
	assert.Equal(t, "ctxloom", Path(), "override must win over the real decision tree")

	restore()
	assert.Equal(t, "/real/ctxloom", Path(), "restore must put the real decision tree back")
}

// TestSetPathForTesting_Nested proves nesting restores the PREVIOUS value,
// not the zero value — so a package-level TestMain override survives a
// nested per-test override/restore.
func TestSetPathForTesting_Nested(t *testing.T) {
	outer := SetPathForTesting("outer")
	defer outer()
	assert.Equal(t, "outer", Path())

	inner := SetPathForTesting("inner")
	assert.Equal(t, "inner", Path())
	inner()
	assert.Equal(t, "outer", Path(), "restoring the inner override must not clear the outer one")
}

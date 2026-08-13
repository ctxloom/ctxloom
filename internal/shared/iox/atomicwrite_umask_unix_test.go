//go:build !windows

package iox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAtomicWriters_PermIsExact_UmaskIndependent pins the mode semantics both
// writers actually have, which are NOT os.WriteFile's: perm is applied with an
// explicit Chmod after the temp file is created, so the caller gets exactly the
// mode it asked for regardless of the process umask. os.WriteFile and
// afero.WriteFile pass perm to open(2)/Create, where the kernel masks it.
//
// So a caller migrating from os.WriteFile to WriteFileAtomic under a
// restrictive umask gets a WIDER file than before (0644 stays 0644 instead of
// becoming 0600). That divergence is real and is now documented rather than
// silently inherited; masking it here is not available, because reading the
// umask in Go requires syscall.Umask, a process-global mutation with no
// Windows equivalent that would race every other goroutine creating a file.
//
// The stdlib comparison inside the fixture is load-bearing: it asserts the
// umask is observably in force from the code-under-test's vantage point before
// anything is claimed about our writers.
func TestAtomicWriters_PermIsExact_UmaskIndependent(t *testing.T) {
	const restrictive = 0o077
	prev := syscall.Umask(restrictive)
	defer syscall.Umask(prev)

	dir := t.TempDir()

	// Fixture check: with umask 077 in force, the stdlib writer must produce
	// 0600 from a 0644 request. If it does not, the umask never reached the
	// code under test and everything below would be vacuous.
	stdlibPath := filepath.Join(dir, "stdlib.json")
	require.NoError(t, os.WriteFile(stdlibPath, []byte("x"), 0o644))
	stdlibInfo, err := os.Stat(stdlibPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), stdlibInfo.Mode().Perm(),
		"fixture is not hostile: the umask did not mask os.WriteFile's mode")

	writers := map[string]func(path string, data []byte, perm os.FileMode) error{
		"WriteFileAtomic": func(path string, data []byte, perm os.FileMode) error {
			return WriteFileAtomic(path, data, perm)
		},
		"WriteFileAtomicFs": func(path string, data []byte, perm os.FileMode) error {
			return WriteFileAtomicFs(afero.NewOsFs(), path, data, perm)
		},
	}
	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(dir, name+".json")
			require.NoError(t, w(target, []byte("x"), 0o644))
			info, serr := os.Stat(target)
			require.NoError(t, serr)
			assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(),
				"perm must be applied exactly; the umask must not narrow it")
		})
	}
}

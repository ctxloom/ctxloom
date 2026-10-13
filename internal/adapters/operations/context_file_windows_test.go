//go:build windows

package operations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolveContextFile_RefusesADrivelessRootedPath (ruling #35): on Windows
// `\x` and `/x` name the current drive's root and `C:x` a drive's current
// directory. None is absolute, none can be resolved against the target, and
// each is refused as outside it rather than taken as a path under it.
func TestResolveContextFile_RefusesADrivelessRootedPath(t *testing.T) {
	target := t.TempDir()
	for _, dest := range []string{`\x`, `/x`, `\docs\AGENTS.md`, `C:x`, `C:AGENTS.md`} {
		_, err := ResolveContextFile(target, dest)
		require.ErrorIs(t, err, ErrContextFileOutsideTarget, dest)
	}
}

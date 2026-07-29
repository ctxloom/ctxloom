package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// U090-F08 parity: internal/remote used to assemble the lockfile path itself
// (a private `lockfileName` const + a `filename` field no option ever
// overrode), which left paths.LockPath — the package that owns this layout —
// with zero production callers and free to drift from it. This pins the two to
// one answer, including the default-baseDir branch.
func TestLockfileManagerPath_MatchesPathsLockPath(t *testing.T) {
	for _, base := range []string{"/proj/.ctxloom", ".ctxloom", "/tmp/x/.ctxloom"} {
		assert.Equal(t, paths.LockPath(base), NewLockfileManager(base).Path(),
			"lockfile path must come from paths.LockPath, not a private copy")
	}
	// An empty baseDir defaults to the bare .ctxloom dir name.
	assert.Equal(t, paths.LockPath(paths.AppDirName), NewLockfileManager("").Path())
}

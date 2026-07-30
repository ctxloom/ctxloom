package coord

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestNew_FailureAfterEphemeralFallbackLeavesNoTempDir pins that a New that
// fails AFTER falling back to an ephemeral state dir takes that directory with
// it. The fallback is reachable in production — a second session-owning
// process for the same project loses the owner lock and runs on ephemeral
// state — and the failure after it is too (Cfg is required without an injected
// Spawner). Left behind, each such attempt strands a 0700 temp directory that
// nothing ever reaps.
func TestNew_FailureAfterEphemeralFallbackLeavesNoTempDir(t *testing.T) {
	testsupport.Isolate(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Park the project's owner lock on a live process that is NOT this one, so
	// New loses the claim and must fall back to ephemeral state. (claimOwner
	// treats a lock held by THIS pid as stale by design, so the test process
	// cannot hold it against itself.)
	const key = "abort-path-project"
	dir, err := stateDirForProject(key)
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(filepath.Join(dir, "owner.pid"), []byte(strconv.Itoa(os.Getppid())+"\n"), 0o600))
	if _, cerr := claimOwner(dir); !assert.ErrorIs(t, cerr, errStateOwned, "precondition: the lock must be unavailable to New") {
		return
	}

	// Cfg nil with no injected Spawner: the first failure after the state dir
	// is acquired.
	c, err := New(Options{ProjectDir: t.TempDir(), ProjectKey: key})
	assert.Nil(t, c)
	assert.Error(t, err)

	entries, rerr := os.ReadDir(tmp)
	assert.NoError(t, rerr)
	var leaked []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ctxloom-coord-") {
			leaked = append(leaked, filepath.Join(tmp, e.Name()))
		}
	}
	assert.Empty(t, leaked, "a failed New must remove the ephemeral state dir it created")
}

package sessionlock

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestAcquire_ALockFileRemovedBeforeTheProbeLocksIsIndeterminateAndNotReminted
// forces the race a probe that could create its lock file would lose: the
// lock file is removed after Acquire resolved it and before it locks. A probe that
// locked a file it had just created would read Dead, and the file it left —
// unlocked, with no pid — would read Dead to every later sweep, even while a
// session runs. The probe must refuse and leave nothing at the path.
func TestAcquire_ALockFileRemovedBeforeTheProbeLocksIsIndeterminateAndNotReminted(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	const harp = "vanishing-harp"
	require.NoError(t, Hold(harp))
	Release(harp)
	path := lockPath(t, harp)

	prev := beforeProbeLock
	beforeProbeLock = func(p string) { require.NoError(t, os.Remove(p)) }
	t.Cleanup(func() { beforeProbeLock = prev })

	p, release := Acquire(harp)
	release()
	assert.Equal(t, Indeterminate, p.Verdict, "a lock file that vanished under the probe proves nothing")
	assert.NoFileExists(t, path, "the probe must not leave a lock file it created")
}

// TestAcquire_ALockFileStampedAfreshBeforeTheProbeLocksIsStillProbed: the
// file is removed and stamped afresh (a Hold starting) before the probe
// locks. The probe did not create that file: its lock is the verdict as for
// any other file — Dead, kept, so the starting Hold waits
// out the reclaim — and the file stays.
func TestAcquire_ALockFileStampedAfreshBeforeTheProbeLocksIsStillProbed(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)
	const harp = "replaced-harp"
	require.NoError(t, Hold(harp))
	Release(harp)
	path := lockPath(t, harp)

	prev := beforeProbeLock
	beforeProbeLock = func(p string) {
		require.NoError(t, os.Remove(p))
		require.NoError(t, stampPID(p))
	}
	t.Cleanup(func() { beforeProbeLock = prev })

	p, release := Acquire(harp)
	release()
	assert.Equal(t, Dead, p.Verdict)
	assert.FileExists(t, path, "a stamped file the probe did not create is left alone")
}

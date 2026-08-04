package config

import (
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A review pass once claimed the exec/PATH seams (companionProbeTimeout,
// companionProbeWaitDelay, companionVersionOutput, companionLoadoutOutput,
// pathDirs, readDir) are unsynchronised package vars READ FROM GOROUTINES while
// the exported Set*ForTesting helpers WRITE them without a lock.
//
// The description of the code is exact. The conclusion — that this is a race —
// is not, and a mutex on every read would buy nothing:
//
//  1. The writers are test-only. Set*ForTesting has no production caller and
//     `ctxloom` never reassigns any of these six vars at runtime, so production
//     has readers and no writer at all.
//  2. Every read is separated from every write by a happens-before pair the
//     probe functions already own: the `go` statement that starts each probe
//     goroutine (write -> goroutine) and the wg.Wait() both probes block on
//     before returning (goroutine -> restore). Install, probe, restore cannot
//     race by construction.
//  3. There is no t.Parallel() anywhere in this package and `just test-pkg`
//     runs -race on every invocation, so the detector has had every opportunity
//     and has never fired here.
//
// What that claim does correctly identify is that the safety is UNSTATED and rests
// on the probes' internal structure. So the answer is a pin, not a lock: this
// drives both probes concurrently with the seams installed once — production's
// read pattern — and asserts every probe observed the installed values. If
// someone drops a wg.Wait(), starts a probe without joining it, or parallelises
// this package's tests while a restore is in flight, it fails here rather than
// as an unattributable failure somewhere else.
//
// MEASURED, and stated because it is the opposite of what one would assume: the
// ASSERTIONS are the gate, not the detector. Injecting a deliberate concurrent
// `lookPath = ...` write alongside the probe fan-out fails this test on the
// value assertions while -race stays silent. Treating "race-clean" as the pass
// condition would therefore have proved nothing, which is why the observed
// values are checked rather than merely the absence of a warning.
func TestCompanionProbeSeams_ConcurrentProbesAreRaceFree(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(lookPathOnly(map[string]string{"ltk": "/fake/ltk"}))
	defer restoreLook()
	restoreVersion := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return []byte(`{"version":"1.2.3"}`), nil
	})
	defer restoreVersion()
	restoreLoadout := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, exec.ErrNotFound
	})
	defer restoreLoadout()

	var wg sync.WaitGroup
	statuses := make([][]CompanionStatus, 8)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = ProbeCompanions()
			_ = ProbeCompanionLoadouts(nil)
		}(i)
	}
	wg.Wait()

	// Not a smoke test: assert the seams were actually reached, or a clean run
	// would prove only that nothing ran.
	for i, got := range statuses {
		require.NotEmpty(t, got, "probe %d returned no companions — the seams were never exercised", i)
		var found bool
		for _, st := range got {
			if st.Bin == "ltk" {
				found = true
				assert.Equal(t, "/fake/ltk", st.Path, "the lookPath seam was read")
				assert.Equal(t, "1.2.3", st.Version, "the version-output seam was read")
			}
		}
		require.True(t, found, "probe %d never saw the installed companion", i)
	}
}

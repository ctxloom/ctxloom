package pidalive

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeIsPidOnly is a compile-time assertion that Probe's whole input is a pid.
// It is the structural half of the package doc's central claim: there is no
// identity parameter, so nothing in this API could distinguish the process the
// caller meant from an unrelated one that inherited its number. Adding a
// pidfd/start-time token to close the reuse window changes this signature, and
// this line stops compiling — which is exactly when the doc above must be
// rewritten rather than quietly left overstating what the package can do.
var probeIsPidOnly func(int) State = Probe

// TestProbe_AnswersAboutThePidNotAboutTheCaller pins the behavioural half: a
// pid this test never started, does not own and cannot signal still reads
// Alive. That is the whole of what Probe knows, and it is why a pid recycled
// between being written to a file and being probed reads Alive too.
func TestProbe_AnswersAboutThePidNotAboutTheCaller(t *testing.T) {
	require.NotNil(t, probeIsPidOnly)

	parent := os.Getppid()
	// Fixture check: the subject must genuinely be a process other than this
	// one, or the assertion below says nothing about foreign pids.
	require.NotEqual(t, os.Getpid(), parent, "no distinct process to probe")
	require.Positive(t, parent)

	assert.Equal(t, Alive, Probe(parent),
		"Probe reports on whatever process holds the number, not on one the caller identified")
}

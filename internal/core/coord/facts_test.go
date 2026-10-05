package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// CauseIsFailure splits the terminal causes into ends that did not finish and
// clean ones.
func TestCauseIsFailure(t *testing.T) {
	for _, cause := range []string{CauseLaunchFailed, CauseRunnerLoss, CauseDrainInterrupted, CauseStopped} {
		assert.True(t, CauseIsFailure(cause), "%s did not finish", cause)
	}
	for _, cause := range []string{CauseRunnerExit, CauseIdleReaped, CauseDrained, CauseFinalReported} {
		assert.False(t, CauseIsFailure(cause), "%s is a clean end", cause)
	}
}

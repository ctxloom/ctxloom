package runner

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/testsupport/trackedtest"
)

// The three runner-side owners of background goroutines — the Home, the
// engine host and the runner link — under the discipline trackedtest drives;
// they share one join budget, tighter than the coordinator's.
func TestTrackedOwners_RunnerSide(t *testing.T) {
	h, eh, l := &Home{}, &EngineHost{}, &RunnerLink{}
	trackedtest.RunOwnerTests(t, map[string]trackedtest.Owner{
		"Home":       {Dispatch: h.goTracked, Wait: h.waitTracked, Seal: h.tracked.Seal},
		"EngineHost": {Dispatch: eh.goTracked, Wait: eh.waitTracked, Seal: eh.tracked.Seal},
		"RunnerLink": {Dispatch: l.goTracked, Wait: l.waitTracked, Seal: l.tracked.Seal},
	})
	assert.Equal(t, 3*time.Second, homeCloseJoinBudget)
	assert.Equal(t, 3*time.Second, engineHostCloseJoinBudget)
	assert.Equal(t, 3*time.Second, runnerLinkCloseJoinBudget)
}

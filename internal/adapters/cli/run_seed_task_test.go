package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// seedTaskIntoSession warned-and-returned on ANY task-store error,
// so a corrupt or unreadable task log at session launch was swallowed into a
// raw stderr line — at exactly the kind of startup choke point that must route
// through strictness. --seed-task is an EXPLICIT ask ("start this session on
// that task"); when it cannot be honored the user otherwise believes the task
// is In Progress and attributed to this session when it is not.
//
// Routed through strictness rather than left as a warning: fatal in strict
// mode, still downgradable with --degraded for anyone who genuinely wants the
// launch to proceed regardless. The never-block-launch carve-out is what made
// this implicit, and the task's own framing is that leaving it implicit is the
// one wrong answer.
func TestSeedTaskIntoSession_StoreFailureIsAFatalFinding(t *testing.T) {
	resetStrictness(t)
	mark := strictness.Checkpoint()

	// A work dir with no readable project task store: SetTaskStatus cannot
	// resolve the named task, so the explicit seed cannot be honored.
	seedTaskIntoSession(t.TempDir(), "active-harp", "no-such-task", "")

	findings := strictness.Since(mark)
	require.NotEmpty(t, findings, "an unhonorable explicit --seed-task must record a finding, not just warn")
	assert.Equal(t, strictness.ClassTask, findings[0].Class)
	assert.Contains(t, findings[0].Message, "no-such-task", "the finding must name the task that was asked for")
	assert.NotEmpty(t, findings[0].FixIt, "a fatal finding must carry a fix-it")
}

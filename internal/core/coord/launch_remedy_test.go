package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const launchRemedyFix = "declare `auth: token` on a container agent, or run it with `runtime: host`"

var errLaunchRemedyCause = errors.New("container: no credential to forward")

// remedyLaunchSpawner fails ResolveLaunch — where a container cell's
// isolation prepare raises its remediable errors — with err.
type remedyLaunchSpawner struct {
	*fakeSpawner
	err error
}

func (s *remedyLaunchSpawner) ResolveLaunch(context.Context, *SpawnPlan, SpawnStart) (Resolved, error) {
	return Resolved{}, s.err
}

func childLaunchNotice(t *testing.T, c *Coordinator, harp string) Message {
	t.Helper()
	var got Message
	require.Eventually(t, func() bool {
		msgs, err := ownerMail(t, c, 50*time.Millisecond)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.From == harp && m.Kind == KindError {
				got = m
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the parent never received the child's launch-failure notice")
	return got
}

// TestFailChild_LaunchNoticeCarriesTheRemedy: a launch error that names its
// own fix (report.Remediable, anywhere in a %w chain) reaches the parent's
// terminal notice WITH that fix, in the one human form clifmt.FixLine owns —
// not flattened to err.Error(), which carries only the message.
func TestFailChild_LaunchNoticeCarriesTheRemedy(t *testing.T) {
	resetStrictness(t)
	sp := &remedyLaunchSpawner{
		fakeSpawner: newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless}}, nil),
		err:         fmt.Errorf("prepare cell: %w", report.Errorf(launchRemedyFix, "%w", errLaunchRemedyCause)),
	}
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	notice := childLaunchNotice(t, c, out.Harp)
	assert.Contains(t, notice.Body, errLaunchRemedyCause.Error(), "the failure itself still reaches the parent")
	assert.Contains(t, notice.Body, clifmt.FixLine("", launchRemedyFix), "the remedy reaches the parent as its fix line")
}

// TestLaunchFailureDetail pins the fold's two arms: a remedy is appended as
// the fix line; an error with none (or an empty one) is its text unchanged.
func TestLaunchFailureDetail(t *testing.T) {
	plain := errors.New("runner never dialed home")
	assert.Equal(t, plain.Error(), launchFailureDetail(plain), "no remedy: the detail is the error's text, nothing appended")

	empty := report.Error{Msg: "listing"}
	assert.Equal(t, "listing", launchFailureDetail(empty), "an empty remedy appends no fix line")

	wrapped := fmt.Errorf("outer: %w", report.Errorf(launchRemedyFix, "inner"))
	got := launchFailureDetail(wrapped)
	assert.True(t, strings.HasPrefix(got, wrapped.Error()), "the error's text leads: %q", got)
	assert.Equal(t, wrapped.Error()+clifmt.FixLine("", launchRemedyFix), got)
	fix, ok := clifmt.RemedyOf(wrapped)
	require.True(t, ok)
	assert.Equal(t, launchRemedyFix, fix, "coord reads the same remedy the CLI renderer would")
}

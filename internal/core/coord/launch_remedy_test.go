package coord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const launchRemedyFix = "run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN"

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
		fakeSpawner: newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless}}, nil),
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

// TestIssueStartRun_RefusalNoticeCarriesTheRemedy: a runner that refuses
// StartRun with an error naming its fix — over the real wire — reaches the
// parent's terminal notice WITH that fix line, not flattened to the refusal's
// text.
func TestIssueStartRun_RefusalNoticeCarriesTheRemedy(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	sp.refuse = refuseOnly(func(req *agentcoordpb.RunnerRequest) bool { return req.GetStartRun() != nil }, remedialRefusal())
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	notice := childLaunchNotice(t, c, out.Harp)
	assert.Contains(t, notice.Body, "StartRun refused", "the refusal is the runner's")
	assert.Contains(t, notice.Body, errLaunchRemedyCause.Error(), "the refusal's text still reaches the parent")
	assert.Contains(t, notice.Body, clifmt.FixLine("", launchRemedyFix), "the remedy reaches the parent as its fix line")
}

// refuseOnly refuses, with err, every runner request match picks out; every
// other request reaches the engine host as usual.
func refuseOnly(match func(*agentcoordpb.RunnerRequest) bool, err error) func(*agentcoordpb.RunnerRequest) error {
	return func(req *agentcoordpb.RunnerRequest) error {
		if match(req) {
			return err
		}
		return nil
	}
}

// remedialRefusal is a runner refusal naming its own fix.
func remedialRefusal() error {
	return fmt.Errorf("execute: %w", report.Errorf(launchRemedyFix, "%w", errLaunchRemedyCause))
}

// TestTurn_RefusalCarriesTheRemedy: a runner that refuses a Turn frame with
// an error naming its fix — over the real wire — answers the caller with an
// error the fix is still reachable from, not the refusal flattened to text.
func TestTurn_RefusalCarriesTheRemedy(t *testing.T) {
	resetStrictness(t)
	sp := oneShotSpawner(t, func() *scriptedChat { return &scriptedChat{} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task one", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	sp.mu.Lock()
	sp.refuse = refuseOnly(func(req *agentcoordpb.RunnerRequest) bool { return req.GetTurn() != nil }, remedialRefusal())
	sp.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err = c.Turn(ctx, out.RunID, engine.Turn{Prompt: "framed turn", Resume: nativeSession(c, out.Harp)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), errLaunchRemedyCause.Error(), "the refusal's text still reaches the caller")
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok, "the refusal's remedy must survive the coordinator's wrap: %v", err)
	assert.Equal(t, launchRemedyFix, fix)
}

// TestControlPause_RefusalCarriesTheRemedy: a runner that refuses a pause
// with an error naming its fix answers ControlPause's caller with an error
// the fix is still reachable from.
func TestControlPause_RefusalCarriesTheRemedy(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(t, 0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")

	sp.mu.Lock()
	sp.refuse = refuseOnly(func(req *agentcoordpb.RunnerRequest) bool { return req.GetPauseRun() != nil }, remedialRefusal())
	sp.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	_, err := c.ControlPause(ctx, humanInitiator(), out.Harp, "human is reviewing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), errLaunchRemedyCause.Error(), "the refusal's text still reaches the caller")
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok, "the refusal's remedy must survive the coordinator's wrap: %v", err)
	assert.Equal(t, launchRemedyFix, fix)
}

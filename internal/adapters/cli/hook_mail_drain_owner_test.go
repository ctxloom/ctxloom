package cli

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// tacky-carload: the hook claims the spool only for the session OWNER's
// engine, which the launch marks with sessions.EnvSessionOwner. A child
// engine that loads the same hooks (a trusted repository's own settings file)
// carries its own harp but no marker, and must leave its spool to its runner.

// withSessionOwnerEnv fixes what the session-owner marker said for one test.
func withSessionOwnerEnv(t *testing.T, on bool) {
	t.Helper()
	prev := sessionOwnerEnv
	sessionOwnerEnv = func() bool { return on }
	t.Cleanup(func() { sessionOwnerEnv = prev })
}

func TestHookMailDrain_AChildWithoutTheOwnerMarkerClaimsNothing(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(sessions.EnvHarp, mailDrainOwner)
	withSessionOwnerEnv(t, false)
	name := seedOwnerMail(t, "parent", "message", "for the child's runner, not its hook\n")

	var out bytes.Buffer
	require.NoError(t, runHookMailDrain(mailDrainCmd(&out), nil))

	assert.Empty(t, out.String(), "nothing reaches the child engine's turn")
	assert.Equal(t, []string{name}, spoolNames(t, spool.DirIn), "the message is still waiting for the runner")
	assert.Empty(t, spoolNames(t, spool.ClaimedDirName), "and was never claimed")
	assert.Empty(t, deliveredIDs(t))
}

func TestHookMailDrain_TheOwnerDrainsUnderItsMarker(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(sessions.EnvHarp, mailDrainOwner)
	withSessionOwnerEnv(t, true)
	name := seedOwnerMail(t, "child-one", "report", "FINAL: done\n")

	var out bytes.Buffer
	require.NoError(t, runHookMailDrain(mailDrainCmd(&out), nil))

	assert.Contains(t, drainedEnvelope(t, &out).HookSpecificOutput.AdditionalContext, "FINAL: done")
	assert.Empty(t, spoolNames(t, spool.DirIn))
	assert.Equal(t, []string{stem(name)}, deliveredIDs(t))
}

// The marker is consumed by EVERY ctxloom process (switches runs in each),
// so the owner's own MCP server and the runners it starts — built from
// os.Environ() — never hand it to a child engine.
func TestSwitches_ConsumeTheSessionOwnerMarker(t *testing.T) {
	t.Setenv(sessions.EnvSessionOwner, sessions.SessionOwnerOn)
	prev := sessionOwnerEnv
	sessionOwnerEnv = sync.OnceValue(func() bool { return consumeEnvSwitch(sessions.EnvSessionOwner) })
	t.Cleanup(func() { sessionOwnerEnv = prev })
	withSigCheckEnv(t, false)
	withSessionSigCheckEnv(t, false)

	_ = switches(nil)

	_, still := os.LookupEnv(sessions.EnvSessionOwner)
	assert.False(t, still, "the process no longer carries the marker")
	assert.True(t, sessionOwnerEnv(), "but it remembers that it was the owner's")
}

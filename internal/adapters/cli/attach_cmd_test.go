package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAttachArgv_HostRunsTmuxDirectly pins the simple half: a run with no
// container needs no hop, because its tmux server is already on this machine.
func TestAttachArgv_HostRunsTmuxDirectly(t *testing.T) {
	got, err := attachArgv(attachTarget{
		Socket: "ctxloom-terminal",
		Window: "ctxloom:ab12cd34-h1",
	}, false)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"tmux", "-L", "ctxloom-terminal", "attach", "-t", "ctxloom:ab12cd34-h1",
	}, got)
}

// TestAttachArgv_ContainerExecsTheClientInside is the whole reason this
// command is an exec wrapper and not a relay.
//
// The client must start INSIDE the container, because tmux is client/server
// and the server's socket lives in the container's filesystem. A host-side
// `tmux -L ... attach` cannot see that socket at all — it would create a new,
// empty server on a host socket of the same name and attach to nothing. So the
// assertion that matters is that the tmux words are ARGUMENTS TO exec, not a
// command run here.
func TestAttachArgv_ContainerExecsTheClientInside(t *testing.T) {
	got, err := attachArgv(attachTarget{
		Runtime:       "podman",
		ContainerName: "ctxloom-sharp-close-treat",
		Socket:        "ctxloom-terminal",
		Window:        "ctxloom:ab12cd34-h1",
	}, false)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"podman", "exec", "-it", "ctxloom-sharp-close-treat",
		"tmux", "-L", "ctxloom-terminal", "attach", "-t", "ctxloom:ab12cd34-h1",
	}, got)
}

// TestAttachArgv_ContainerKeepsTheTtyFlags pins -it specifically, because
// dropping it fails in the most confusing way available: without a tty tmux
// refuses to start, and with -t but not -i the pane RENDERS while every
// keystroke is discarded. That is indistinguishable from a hung agent.
func TestAttachArgv_ContainerKeepsTheTtyFlags(t *testing.T) {
	got, err := attachArgv(attachTarget{
		Runtime:       "docker",
		ContainerName: "c1",
		Socket:        "s",
		Window:        "w:1",
	}, false)

	require.NoError(t, err)
	require.Greater(t, len(got), 3)
	assert.Equal(t, []string{"docker", "exec", "-it", "c1"}, got[:4],
		"the exec hop must keep both -i and -t")
}

// TestAttachArgv_ReadOnlyAttachesWithMinusR is the one rule that survived the
// relay's deletion unchanged in MEANING and changed in mechanism.
//
// The relay enforced read-only by declining to forward keystrokes, so the
// guarantee was only as good as our own client. tmux -r is enforced by the
// SERVER, so there is no local filter to bypass, and the flag must actually
// reach the tmux words rather than the exec hop.
func TestAttachArgv_ReadOnlyAttachesWithMinusR(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target attachTarget
	}{
		{"host", attachTarget{Socket: "s", Window: "w:1"}},
		{"container", attachTarget{Runtime: "podman", ContainerName: "c1", Socket: "s", Window: "w:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := attachArgv(tc.target, true)
			require.NoError(t, err)
			assert.Equal(t, "-r", got[len(got)-1],
				"-r must be the tmux client's flag, at the end of the tmux words: %v", got)

			plain, err := attachArgv(tc.target, false)
			require.NoError(t, err)
			assert.NotContains(t, plain, "-r",
				"a writable attach must not carry -r: %v", plain)
		})
	}
}

// TestAttachArgv_UnknownWindowIsRefused stops the failure mode that would be
// worst here. `tmux attach -t` against an empty or wrong target does not
// reliably error — against an existing-but-different window it SUCCEEDS and
// shows another run's pane. So a missing window must be refused before an argv
// is ever built, not passed through for tmux to interpret.
func TestAttachArgv_UnknownWindowIsRefused(t *testing.T) {
	_, err := attachArgv(attachTarget{Socket: "s"}, false)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPaneTargetUnknown)
}

// TestAttachArgv_ContainerWithoutANameIsRefused: `podman exec -it "" tmux ...`
// is a well-formed command line that addresses nothing.
func TestAttachArgv_ContainerWithoutANameIsRefused(t *testing.T) {
	_, err := attachArgv(attachTarget{Runtime: "podman", Socket: "s", Window: "w:1"}, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "podman")
}

// TestRunAttach_RefusesUntilTheWindowIsJournaled pins the deliberate gap.
//
// This test asserts that the command REFUSES, which is unusual and is the
// point: the alternative to refusing is guessing a window name, and a wrong
// guess silently shows the operator a different agent's pane. The refusal is
// addressed to a user, so it says the command is not available yet and why,
// rather than naming the internal fact that would complete it.
//
// Delete this test when the window becomes resolvable — it is the marker for
// unfinished work, not a permanent contract.
func TestRunAttach_RefusesUntilTheWindowIsJournaled(t *testing.T) {
	err := runAttach(attachCmd, []string{"sharp-close-treat"})

	require.Error(t, err, "attach must not report success while doing nothing")
	assert.ErrorIs(t, err, ErrPaneTargetUnknown)
	assert.Contains(t, err.Error(), "sharp-close-treat", "the refusal must name the run")
	assert.Contains(t, err.Error(), "not available yet", "the refusal must say the command does not work yet")
}

// TestAttachCmd_HiddenUntilItWorks: a command with no success path must not be
// advertised in --help or the generated reference. Unhide it together with
// deleting the test above.
func TestAttachCmd_HiddenUntilItWorks(t *testing.T) {
	assert.True(t, attachCmd.Hidden, "attach has no success path, so help and the generated docs must not list it")
	assert.False(t, attachCmd.IsAvailableCommand(), "cobra's doc generator skips exactly the unavailable commands")
}

// TestAttachSocket_IsTheHostedPaneSocket keeps the wrapper pointed at the
// server internal/adapters/tmuxhost actually creates. A wrapper that attached to a
// different socket would create an empty tmux server and attach to nothing --
// succeeding, and showing the user nothing.
//
// The name is spelled as a literal rather than compared against
// tmuxhost.TmuxSocketName(): sourcing both sides from the one constant would
// make the assertion true by construction, so it would survive any rename of
// the socket -- the single thing it exists to catch.
func TestAttachSocket_IsTheHostedPaneSocket(t *testing.T) {
	assert.Equal(t, "ctxloom-terminal", attachSocket())
}

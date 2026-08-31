package acp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
)

// terminal/*'s only reclamation path is terminal/release, and release is driven
// by the CLIENT. Nothing on ctxloom's side is a backstop: localTerminals.terms
// is inserted in create, read in lookup and deleted in release, and nothing
// iterates it — so a client that creates a terminal and then disconnects
// without releasing leaves its tmux window and both files behind forever.
//
// That matters more here than it would elsewhere because the tmux session is
// SHARED and long-lived: nothing tears the server down and later runs adopt it,
// so dead windows accumulate in a server other runs are using, and a dead pane
// is indistinguishable from a live one when triaging.
//
// This drives a REAL tmux (see tmux_host_test.go's realTmux) because the thing
// under test is whether tmux actually loses the window, which a fake runner
// cannot show.
func TestLocalTerminals_SessionEndReclaimsAnUnreleasedTerminal(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	resp, err := l.create(ctx, api.CreateTerminalRequest{
		Command: "sh",
		Args:    []string{"-c", "echo unreleased-probe-6f4c"},
	})
	require.NoError(t, err)

	term, ok := l.lookup(resp.TerminalId)
	require.True(t, ok)

	// Let it finish, so the files exist and the pane is dead-but-present
	// (remain-on-exit) — the exact state an abandoned terminal is left in.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer waitCancel()
	_, err = l.wait(waitCtx, api.WaitForTerminalExitRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err)

	// PRECONDITIONS. Without these, "it is gone" is satisfied by something that
	// was never there — this project's catalogued absence-satisfies-absence
	// tautology.
	require.FileExists(t, term.outputPath, "precondition: capture file exists before the session ends")
	require.FileExists(t, term.statusPath, "precondition: status file exists before the session ends")
	listed, lerr := runner.Run(context.Background(), "list-windows", "-a", "-F", "#{session_name}:#{window_name}")
	require.NoError(t, lerr)
	require.Contains(t, listed, term.window, "precondition: the tmux window exists before the session ends")

	// The client never releases. The session simply ends.
	cancel()

	// THE WINDOW is the assertion that bites. os.Remove ignores contexts, so a
	// files-only assertion stays green even when the tmux calls are handed a
	// cancelled context and die before running — measured on the hosting side,
	// where exactly that mutation survived a files-only test.
	require.Eventually(t, func() bool {
		out, err := runner.Run(context.Background(), "list-windows", "-a", "-F", "#{session_name}:#{window_name}")
		return err != nil || !strings.Contains(out, term.window)
	}, 15*time.Second, 50*time.Millisecond,
		"ending the session must destroy the tmux window of a terminal the client never released")

	require.Eventually(t, func() bool {
		_, oerr := os.Stat(term.outputPath)
		_, serr := os.Stat(term.statusPath)
		return os.IsNotExist(oerr) && os.IsNotExist(serr)
	}, 15*time.Second, 50*time.Millisecond,
		"ending the session must also reclaim the terminal's files")
}

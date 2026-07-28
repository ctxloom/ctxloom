package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateACPWorkspaceRequiresAgent pins U034-F03: `--workspace worktree`
// with no `--agent` used to be silently accepted and then silently discarded
// by acpWorkspaceAxis (internal/operations/engine_session.go) — the user
// asks for worktree isolation, gets none, and is told nothing. It must be
// refused instead, loudly, before the session ever opens.
func TestValidateACPWorkspaceRequiresAgent(t *testing.T) {
	newCmd := func(t *testing.T) *cobra.Command {
		t.Helper()
		cmd := &cobra.Command{Use: "server"}
		registerACPServerFlags(cmd)
		return cmd
	}

	t.Run("workspace without agent is refused", func(t *testing.T) {
		cmd := newCmd(t)
		require.NoError(t, cmd.Flags().Set("workspace", "worktree"))
		err := validateACPWorkspaceRequiresAgent(cmd, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--workspace requires --agent")
	})

	t.Run("workspace with agent is accepted", func(t *testing.T) {
		cmd := newCmd(t)
		require.NoError(t, cmd.Flags().Set("workspace", "worktree"))
		require.NoError(t, validateACPWorkspaceRequiresAgent(cmd, "reviewer"))
	})

	t.Run("no workspace flag at all is accepted regardless of agent", func(t *testing.T) {
		cmd := newCmd(t)
		require.NoError(t, validateACPWorkspaceRequiresAgent(cmd, ""))
	})

	t.Run("workspace explicitly set to empty string is NOT Changed, so not rejected", func(t *testing.T) {
		// cobra's Changed only reflects an actual --flag=value on argv; a
		// zero-value default (the common "no --workspace at all" case) must
		// not trip this guard even though the underlying string is "".
		cmd := newCmd(t)
		assert.False(t, cmd.Flags().Changed("workspace"))
		require.NoError(t, validateACPWorkspaceRequiresAgent(cmd, ""))
	})
}

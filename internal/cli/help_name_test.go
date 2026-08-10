package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// "help" is a legal resource name — a bundle of help docs, an agent called
// help. Guarding on the literal made every such resource UNADDRESSABLE and,
// worse, turned a genuine request into "print help, exit 0": `bundle create
// help` reported success and created nothing.
//
// The courtesy shortcut is kept where it costs nothing (nothing of that name
// exists, so the command was going to fail anyway); an explicit creation, and
// any operation on a resource that DOES exist, must be honoured.
func TestResourceNamedHelp_IsAddressable(t *testing.T) {
	// ProjectDir, not setupEditProject: these drive the cobra RunE bodies, and
	// those call the package-level GetConfig(), which resolves the REAL app
	// paths (including $HOME/.ctxloom) unless the environment is isolated.
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	t.Run("bundle create help creates a bundle named help", func(t *testing.T) {
		cmd, _ := formatCmd("text")
		cmd.SetContext(context.Background())
		require.NoError(t, runBundleCreate(cmd, []string{"help"}))

		cfg, err := config.LoadFresh()
		require.NoError(t, err)
		b, err := operations.GetBundle(cfg, "help")
		require.NoError(t, err, "a bundle literally named help must actually be created")
		assert.Equal(t, "help", b.Name)
	})

	t.Run("bundle show help shows it", func(t *testing.T) {
		cmd, out := formatCmd("text")
		cmd.SetContext(context.Background())
		require.NoError(t, runBundleShow(cmd, []string{"help"}))
		assert.Contains(t, out.String(), "Bundle: help",
			"the bundle must be shown, not the command's help text")
	})

	t.Run("bundle edit help edits it", func(t *testing.T) {
		bundleEditDesc = "help docs"
		t.Cleanup(func() { bundleEditDesc = "" })
		cmd, out := formatCmd("text")
		cmd.Use = "edit <name>"
		cmd.Short = "Edit bundle metadata"
		cmd.SetContext(context.Background())
		require.NoError(t, runBundleEdit(cmd, []string{"help"}))
		assert.NotContains(t, out.String(), "Edit bundle metadata", "an existing bundle must be edited, not described")

		cfg, err := config.LoadFresh()
		require.NoError(t, err)
		b, err := operations.GetBundle(cfg, "help")
		require.NoError(t, err)
		assert.Equal(t, "help docs", b.Description)
	})
}

// The negative control: with nothing of that name, the courtesy shortcut still
// fires — `bundle show help` prints the command's help and exits 0, exactly as
// before.
func TestNameHelp_StillPrintsHelpWhenNoSuchResourceExists(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	cmd, out := formatCmd("text")
	cmd.Use = "show <name>"
	cmd.Short = "Show bundle contents"
	cmd.SetContext(context.Background())
	require.NoError(t, runBundleShow(cmd, []string{"help"}),
		"no bundle of that name: the command was going to fail, so the shortcut is free")
	assert.NotContains(t, out.String(), "Bundle: help", "there is no such bundle to show")
	assert.Contains(t, out.String(), "Show bundle contents", "the courtesy help is what comes out instead")
}

// Agents behave the same way: an agent literally named help is showable.
func TestAgentNamedHelp_IsShowable(t *testing.T) {
	root := testsupport.ProjectDir(t)
	agentsDir := filepath.Join(root, ".ctxloom", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "help.yaml"),
		[]byte("llm: claude-code\nprofiles: [default]\n"), 0o644))
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	cmd, out := formatCmd("text")
	cmd.SetContext(context.Background())
	require.NoError(t, agentShowCmd.RunE(cmd, []string{"help"}))

	assert.Contains(t, out.String(), "Agent: help",
		"the agent must be shown, not the command's help text")
}

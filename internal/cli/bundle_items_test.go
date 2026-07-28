package cli

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// seedBundleMCP creates bundle "demo" (if it doesn't already exist) with one
// MCP server entry, as `bundle mcp edit` would see it before any edit.
func seedBundleMCP(t *testing.T, cfg *config.Config, name string) {
	t.Helper()
	if _, err := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: name}); err == nil {
		return
	}
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	if err != nil {
		require.ErrorContains(t, err, "already exists")
	}
	_, err = operations.SetBundleMCP(context.Background(), cfg, operations.SetBundleMCPRequest{
		Bundle: "demo",
		Name:   name,
		MCP:    operations.BundleMCPInput{Command: "real-mcp-server", Args: []string{"--flag"}},
	})
	require.NoError(t, err)
}

// TestRunBundleMCPEdit_EmptiedBufferAborts pins U034-F01: an emptied editor
// buffer (the user deleted everything and saved) used to silently gut the MCP
// entry — yaml.Unmarshal("", &edited) succeeds with a zero-value struct, no
// error of its own — and report "Updated" success. It must instead abort,
// leaving the bundle unchanged.
func TestRunBundleMCPEdit_EmptiedBufferAborts(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "") // the emptied-buffer save

	cmd := &cobra.Command{}
	err := runBundleMCPEdit(cmd, []string{"demo", "srv"})
	require.Error(t, err, "an emptied editor buffer must abort, not silently gut the MCP entry")

	after, gerr := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: "srv"})
	require.NoError(t, gerr)
	assert.Equal(t, "real-mcp-server", after.MCP.Command, "the bundle must be UNCHANGED after an aborted edit")
}

// TestRunBundleMCPEdit_NoCommandAborts is U034-F01's sibling case: valid YAML
// that simply carries no `command:` (e.g. the user deleted just that line)
// is just as unusable as an empty buffer and must abort the same way.
func TestRunBundleMCPEdit_NoCommandAborts(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "args:\n  - --flag\n")

	cmd := &cobra.Command{}
	err := runBundleMCPEdit(cmd, []string{"demo", "srv"})
	require.Error(t, err, "a command-less MCP edit must abort")

	after, gerr := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: "srv"})
	require.NoError(t, gerr)
	assert.Equal(t, "real-mcp-server", after.MCP.Command, "the bundle must be UNCHANGED after an aborted edit")
}

// TestRunBundleMCPEdit_ValidEditSucceeds is the anti-regression half: a
// genuinely edited, valid MCP config still saves normally.
func TestRunBundleMCPEdit_ValidEditSucceeds(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "command: new-mcp-server\nargs:\n  - --new-flag\n")

	cmd := &cobra.Command{}
	err := runBundleMCPEdit(cmd, []string{"demo", "srv"})
	require.NoError(t, err)

	after, gerr := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: "srv"})
	require.NoError(t, gerr)
	assert.Equal(t, "new-mcp-server", after.MCP.Command)
}

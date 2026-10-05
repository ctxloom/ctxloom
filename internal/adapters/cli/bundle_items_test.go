package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/wire"
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
		MCP:    operations.BundleMCPInput{Command: new("real-mcp-server"), Args: &[]string{"--flag"}},
	})
	require.NoError(t, err)
}

// TestRunBundleMCPEdit_EmptiedBufferAborts pins that an emptied editor
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

// TestRunBundleMCPEdit_NoTargetAborts is the sibling case of an emptied
// buffer: valid YAML that names no target (e.g. the user deleted just the
// `command:` line) is just as unusable as an empty buffer and must abort,
// leaving the bundle unchanged.
func TestRunBundleMCPEdit_NoTargetAborts(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "args:\n  - --flag\n")

	cmd := &cobra.Command{}
	err := runBundleMCPEdit(cmd, []string{"demo", "srv"})
	require.ErrorIs(t, err, wire.ErrMCPServerNoTarget, "a target-less MCP edit must abort")

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

// `bundle mcp edit`'s success line is the command's primary output, so it must
// go to cmd.OutOrStdout() like every other command's — a package-level
// fmt.Printf bypasses the writer cobra was given, which is why the message was
// unassertable and invisible to any caller that redirected output.
func TestRunBundleMCPEdit_WritesToTheCommandsOutWriter(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "command: new-mcp-server\nargs:\n  - --new-flag\n")

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	require.NoError(t, runBundleMCPEdit(cmd, []string{"demo", "srv"}))

	assert.Contains(t, out.String(), `Updated MCP server "srv" in bundle "demo"`,
		"the success line must reach the writer cobra was given")
}

// A remote server has a url and no command; the editor must save it.
func TestRunBundleMCPEdit_RemoteServerWithoutACommandSaves(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "url: https://mcp.example.test/v1\nheaders:\n  Authorization: Bearer ${env:TOKEN}\ntags:\n  - remote\n")

	require.NoError(t, runBundleMCPEdit(&cobra.Command{}, []string{"demo", "srv"}))

	after, err := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, "https://mcp.example.test/v1", after.MCP.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${env:TOKEN}"}, after.MCP.Headers)
	assert.Equal(t, []string{"remote"}, after.MCP.Tags)
	assert.Empty(t, after.MCP.Command, "the buffer is the whole entry: the deleted command is cleared")
}

// The editor buffer is the whole entry, so a field deleted from it is cleared
// rather than kept from the stored entry.
func TestRunBundleMCPEdit_AFieldDeletedInTheEditorIsCleared(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "command: real-mcp-server\n")

	require.NoError(t, runBundleMCPEdit(&cobra.Command{}, []string{"demo", "srv"}))

	after, err := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: "srv"})
	require.NoError(t, err)
	assert.Empty(t, after.MCP.Args)
}

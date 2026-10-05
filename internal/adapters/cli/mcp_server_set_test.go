package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// execMCPServerSet drives `ctxloom mcp server set` through the real root, with
// the leaf's flags reset on both sides so no run inherits another's.
func execMCPServerSet(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetFlagState(mcpServerSetCmd)
	t.Cleanup(func() { resetFlagState(mcpServerSetCmd) })
	return runRoot(t, append([]string{"mcp", "server", "set"}, args...)...)
}

// seedRemoteMCP gives bundle "demo" a remote server "srv".
func seedRemoteMCP(t *testing.T, cfg *config.Config) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)
	_, err = operations.SetBundleMCP(context.Background(), cfg, operations.SetBundleMCPRequest{
		Bundle: "demo", Name: "srv", MCP: operations.BundleMCPInput{
			URL:     new("https://mcp.example.test/v1"),
			Headers: &map[string]string{"Authorization": "Bearer ${TOKEN}"},
			Tags:    &[]string{"remote"},
		},
	})
	require.NoError(t, err)
}

func getDemoMCP(t *testing.T, cfg *config.Config) bundles.BundleMCP {
	t.Helper()
	got, err := operations.GetBundleMCP(context.Background(), cfg, operations.GetBundleMCPRequest{Bundle: "demo", Name: "srv"})
	require.NoError(t, err)
	return got.MCP
}

func TestMCPServerSet_WithoutURLKeepsTheRemoteFields(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	_, err := execMCPServerSet(t, "demo#mcp/srv", "--notes", "hello")
	require.NoError(t, err)

	got := getDemoMCP(t, cfg)
	assert.Equal(t, "https://mcp.example.test/v1", got.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${TOKEN}"}, got.Headers)
	assert.Equal(t, []string{"remote"}, got.Tags)
	assert.Equal(t, "hello", got.Notes)
}

func TestMCPServerSet_AnEmptyValueClears(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	_, err := execMCPServerSet(t, "demo#mcp/srv", "--url", "", "--header", "", "--tag", "", "--command", "local-bin")
	require.NoError(t, err)

	got := getDemoMCP(t, cfg)
	assert.Equal(t, "local-bin", got.Command)
	assert.Empty(t, got.URL)
	assert.Empty(t, got.Headers)
	assert.Empty(t, got.Tags)
}

// The repeatable flags accumulate, and a set on a name the bundle lacks
// creates the entry.
func TestMCPServerSet_RepeatableFlagsCreateAMissingEntry(t *testing.T) {
	cfg := setupEditProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)

	_, err = execMCPServerSet(t, "demo#mcp/srv", "--command", "bin",
		"--arg", "-a", "--arg", "b=c", "--env", "K=V", "--env", "Q=x=y", "--tag", "t1", "--tag", "t2")
	require.NoError(t, err)

	got := getDemoMCP(t, cfg)
	assert.Equal(t, "bin", got.Command)
	assert.Equal(t, []string{"-a", "b=c"}, got.Args)
	assert.Equal(t, map[string]string{"K": "V", "Q": "x=y"}, got.Env, "a value keeps every = after the first")
	assert.Equal(t, []string{"t1", "t2"}, got.Tags)
}

func TestMCPServerSet_RepeatableHeaders(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	_, err := execMCPServerSet(t, "demo#mcp/srv", "--header", "Authorization=Bearer ${T2}", "--header", "X-Team=core")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${T2}", "X-Team": "core"}, getDemoMCP(t, cfg).Headers)
}

func TestMCPServerSet_RefusesAMalformedPair(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	_, err := execMCPServerSet(t, "demo#mcp/srv", "--header", "Authorization: Bearer x")
	require.ErrorIs(t, err, errMCPSetPairNoEquals)
	_, err = execMCPServerSet(t, "demo#mcp/srv", "--env", "K=1", "--env", "K=2")
	require.ErrorIs(t, err, errMCPSetPairTwice)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${TOKEN}"}, getDemoMCP(t, cfg).Headers)
}

// An entry the loader would refuse is never saved.
func TestMCPServerSet_RefusesAnEntryWithTwoTargets(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	_, err := execMCPServerSet(t, "demo#mcp/srv", "--command", "bin")
	require.ErrorIs(t, err, wire.ErrMCPServerTwoTargets)
	assert.Empty(t, getDemoMCP(t, cfg).Command)
}

func TestMCPServerSet_RefusesARefThatIsNotABundleMCPItem(t *testing.T) {
	setupEditProject(t)
	for _, ref := range []string{"srv", "demo#fragment/srv", "demo#mcp/"} {
		_, err := execMCPServerSet(t, ref, "--notes", "x")
		require.ErrorIs(t, err, errNotABundleMCPRef, ref)
	}
}

func TestMCPServerSet_EmitsTheResult(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	out, err := execMCPServerSet(t, "demo#mcp/srv", "--notes", "n", "--format", "json")
	require.NoError(t, err)
	var res operations.SetBundleMCPResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, operations.SetBundleMCPStatusUpdated, res.Status)
	assert.Equal(t, "demo", res.Bundle)
	assert.Equal(t, "srv", res.Name)
	assert.NotEmpty(t, res.Path)

	out, err = execMCPServerSet(t, "demo#mcp/fresh", "--command", "bin", "--format", "json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, operations.SetBundleMCPStatusCreated, res.Status)
	assert.Equal(t, "fresh", res.Name)
}

// The text line says whether the entry was made or changed. --format text is
// explicit because a test's stdout is not a terminal, which picks json.
func TestMCPServerSet_TextSaysCreatedOrSet(t *testing.T) {
	cfg := setupEditProject(t)
	seedRemoteMCP(t, cfg)

	out, err := execMCPServerSet(t, "demo#mcp/fresh", "--command", "bin", "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "Created MCP server \"fresh\" in bundle \"demo\"\n", out)

	out, err = execMCPServerSet(t, "demo#mcp/srv", "--notes", "n", "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "Set MCP server \"srv\" in bundle \"demo\"\n", out)
}

// The editor buffer is the whole entry, served_by included.
func TestRunBundleMCPEdit_RoundTripsServedBy(t *testing.T) {
	cfg := setupEditProject(t)
	seedBundleMCP(t, cfg, "srv")
	setFakeEditor(t, "served_by: "+wire.ServedBySessionEndpoint+"\nnotes: companion\n")

	require.NoError(t, runBundleMCPEdit(&cobra.Command{}, []string{"demo", "srv"}))
	got := getDemoMCP(t, cfg)
	assert.Equal(t, wire.ServedBySessionEndpoint, got.ServedBy)
	assert.Empty(t, got.Command)
}

package operations

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// remoteMCPInput is a fully named remote server entry: every field a caller
// can set on a network-hosted MCP server.
func remoteMCPInput() BundleMCPInput {
	return BundleMCPInput{
		URL:          new("https://mcp.example.test/v1"),
		Headers:      &map[string]string{"Authorization": "Bearer ${env:TOKEN}"},
		Tags:         &[]string{"remote", "search"},
		Notes:        new("remote notes"),
		Installation: new("set TOKEN"),
	}
}

func assertRemoteMCP(t *testing.T, got bundles.BundleMCP) {
	t.Helper()
	assert.Equal(t, "https://mcp.example.test/v1", got.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${env:TOKEN}"}, got.Headers)
	assert.Equal(t, []string{"remote", "search"}, got.Tags)
	assert.Equal(t, "remote notes", got.Notes)
}

// A set names only the fields it changes. Setting a remote server without a
// url must keep its url, headers and tags: they are what makes it dialable.
func TestSetBundleMCP_KeepsEveryFieldTheCallerDidNotName(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
		Name: "b", SetMCPServers: map[string]BundleMCPInput{"srv": remoteMCPInput()},
	})
	require.NoError(t, err)

	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{
		Bundle: "b", Name: "srv", MCP: BundleMCPInput{Installation: new("export TOKEN first")},
	})
	require.NoError(t, err)

	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assertRemoteMCP(t, got.MCP)
	assert.Equal(t, "export TOKEN first", got.MCP.Installation)
}

// An explicit empty value clears a field; it is how a caller turns a remote
// server back into a stdio one.
func TestSetBundleMCP_AnExplicitEmptyValueClearsTheField(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
		Name: "b", SetMCPServers: map[string]BundleMCPInput{"srv": remoteMCPInput()},
	})
	require.NoError(t, err)

	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "srv", MCP: BundleMCPInput{
		Command: new("local-bin"),
		URL:     new(""),
		Headers: &map[string]string{},
		Tags:    &[]string{},
	}})
	require.NoError(t, err)

	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, "local-bin", got.MCP.Command)
	assert.Empty(t, got.MCP.URL)
	assert.Empty(t, got.MCP.Headers)
	assert.Empty(t, got.MCP.Tags)
	assert.Equal(t, "remote notes", got.MCP.Notes, "an unnamed field survives a clearing set")
}

// Clearing an already-absent field is not a change: an empty value and an
// absent field are the same stored state.
func TestApplyMCPEdits_ClearingAnAbsentFieldIsNotAChange(t *testing.T) {
	b := &bundles.Bundle{MCP: map[string]bundles.BundleMCP{"srv": {Command: "bin"}}}
	changes := applyMCPEdits(b, map[string]BundleMCPInput{"srv": {Args: &[]string{}, Env: &map[string]string{}}}, nil, nil)
	assert.Empty(t, changes)
}

func TestCreateBundle_RemoteMCPRoundTripsThroughTheTree(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "with-remote", MCPServers: map[string]BundleMCPInput{"srv": remoteMCPInput()},
	})
	require.NoError(t, err)
	assertRemoteMCP(t, readBackBundle(t, result.Path).MCP["srv"])
}

func TestUpdateBundle_AddedRemoteMCPRoundTripsThroughTheTree(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
		Name: "b", AddMCPServers: map[string]BundleMCPInput{"srv": remoteMCPInput()},
	})
	require.NoError(t, err)
	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assertRemoteMCP(t, got.MCP)
}

// The request is JSON-shaped for frontends that decode it from the wire: an
// absent key keeps the field and a present empty value clears it, exactly as
// the Go API does.
func TestUpdateBundleRequest_JSONAbsentKeepsAndEmptyClears(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{
		Name: "b", SetMCPServers: map[string]BundleMCPInput{"srv": remoteMCPInput()},
	})
	require.NoError(t, err)

	var req UpdateBundleRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"b","set_mcp_servers":{"srv":{"command":"bin","url":"","tags":[]}}}`), &req))
	_, err = UpdateBundle(ctx, cfg, req)
	require.NoError(t, err)

	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, "bin", got.MCP.Command)
	assert.Empty(t, got.MCP.URL)
	assert.Empty(t, got.MCP.Tags)
	assert.Equal(t, map[string]string{"Authorization": "Bearer ${env:TOKEN}"}, got.MCP.Headers, "an absent key is kept")

	// A marshalled input decodes to the same patch: nil stays absent.
	in := BundleMCPInput{URL: new("")}
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	var back BundleMCPInput
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, in, back)
}

// served_by is patched like every other field.
func TestSetBundleMCP_PatchesServedBy(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "srv", MCP: BundleMCPInput{
		ServedBy: new(wire.ServedBySessionEndpoint), Notes: new("n"),
	}})
	require.NoError(t, err)
	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "srv", MCP: BundleMCPInput{Notes: new("n2")}})
	require.NoError(t, err)

	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assert.Equal(t, wire.ServedBySessionEndpoint, got.MCP.ServedBy)
	assert.Equal(t, "n2", got.MCP.Notes)
}

// A set on a name the bundle lacks creates the entry: set is an upsert.
func TestSetBundleMCP_CreatesAMissingEntry(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "fresh", MCP: remoteMCPInput()})
	require.NoError(t, err)
	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "fresh"})
	require.NoError(t, err)
	assertRemoteMCP(t, got.MCP)
}

// The loader refuses an entry with no target or two, so a set that would
// produce one is refused before it is saved: saving it would leave a bundle
// that no longer loads.
func TestSetBundleMCP_RefusesAnEntryTheLoaderWouldRefuse(t *testing.T) {
	ctx := context.Background()
	cfg := newItemTestBundle(t)
	_, err := SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "srv", MCP: remoteMCPInput()})
	require.NoError(t, err)

	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "srv", MCP: BundleMCPInput{URL: new("")}})
	require.ErrorIs(t, err, wire.ErrMCPServerNoTarget)
	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "srv", MCP: BundleMCPInput{Command: new("bin")}})
	require.ErrorIs(t, err, wire.ErrMCPServerTwoTargets)
	_, err = SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: "b", Name: "new", MCP: BundleMCPInput{Notes: new("n")}})
	require.ErrorIs(t, err, wire.ErrMCPServerNoTarget)

	got, err := GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "srv"})
	require.NoError(t, err)
	assertRemoteMCP(t, got.MCP)
	_, err = GetBundleMCP(ctx, cfg, GetBundleMCPRequest{Bundle: "b", Name: "new"})
	require.ErrorIs(t, err, ErrItemNotFound, "a refused set creates nothing")
}

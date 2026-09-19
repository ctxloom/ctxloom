package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// premisedCorpusConfig stages a project whose corpus carries one premised and
// one unconditional fragment, and returns a config that reads it.
func premisedCorpusConfig(t *testing.T) *config.Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundleDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	doc := `version: "1.0"
fragments:
  gamma:
    premise: You are about to remove a worktree.
    content: GAMMA-BODY
  always:
    content: ALWAYS-BODY
`
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "premised.yaml"), []byte(doc), 0o644))
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// readFragmentsResource reads ctxloom://fragments through a REGISTERED server
// over an in-memory transport, not by calling a handler directly. The defect
// this guards against is a handler that exists but was never bound to the URI
// — the registration is the thing under test, and a direct call cannot see it.
func readFragmentsResource(t *testing.T, cfg *config.Config) map[string]any {
	t.Helper()
	s := &ctxServer{cfg: cfg}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerResources(server)

	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: resourceFragmentsURI})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "application/yaml", res.Contents[0].MIMEType)

	var got map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(res.Contents[0].Text), &got))
	return got
}

// The catalog an agent is pointed at must carry the guidance it selects by.
// Server instructions name ctxloom://fragments as the place to read premises;
// an agent arriving there with only the rows would be choosing with none of
// the three properties measurement showed to carry selection — the same
// blind-consumer failure the CLI's structured payload already closes.
//
// Asserted against operations.PremiseSelectionInstruction, never a phrase
// lifted out of it: the wording is expected to be revised, and a copied literal
// keeps passing after the revision while checking nothing.
func TestFragmentsResource_PayloadCarriesTheSelectionInstruction(t *testing.T) {
	testsupport.Isolate(t)
	got := readFragmentsResource(t, premisedCorpusConfig(t))

	assert.Equal(t, operations.PremiseSelectionInstruction(), got["instruction"],
		"the resource must serve the measured selection wording verbatim, from the one source function")
}

// Adding the instruction must not cost the listing anything it already served:
// the rows, their premises and qualified refs, and the count are what a
// consumer was reading before and keeps reading.
func TestFragmentsResource_PayloadStillCarriesTheListing(t *testing.T) {
	testsupport.Isolate(t)
	got := readFragmentsResource(t, premisedCorpusConfig(t))

	rows, ok := got["fragments"].([]any)
	require.True(t, ok, "fragments must remain a list, got %T", got["fragments"])
	// Built-in bundles ride along with the staged corpus, so the count is
	// pinned to the list it describes rather than to a fixture-sized literal.
	assert.EqualValues(t, len(rows), got["count"], "count must still report every fragment listed")
	byName := map[string]map[string]any{}
	for _, r := range rows {
		row, ok := r.(map[string]any)
		require.True(t, ok)
		byName[row["name"].(string)] = row
	}
	require.Contains(t, byName, "gamma")
	require.Contains(t, byName, "always")
	assert.Equal(t, "You are about to remove a worktree.", byName["gamma"]["premise"],
		"the premise is the field a consumer selects on and must ride the descriptor")
	assert.Equal(t, "premised#fragments/gamma", byName["gamma"]["ref"],
		"the qualified ref is what a selection quotes back")
	assert.NotContains(t, byName["gamma"], "content",
		"bodies come from ctxloom://fragments/{name}, for the chosen ones only")
}

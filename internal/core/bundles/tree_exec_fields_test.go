package bundles

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// requireEveryFieldSet fails when a fixture leaves a field zero, so a field
// added to the bundle type cannot pass the round trips below unexercised.
func requireEveryFieldSet(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	for i := 0; i < rv.NumField(); i++ {
		require.False(t, rv.Field(i).IsZero(), "fixture %T leaves %s unset; set it so the round trip covers it", v, rv.Type().Field(i).Name)
	}
}

// The forward mapping (TreeMCP) and the read (addMCP) are inverses over EVERY
// field of BundleMCP: a field one direction forgets is lost the first time a
// tree is saved or loaded.
func TestTreeMCP_RoundTripsEveryField(t *testing.T) {
	want := BundleMCP{
		Command:      "srv",
		Args:         []string{"--flag"},
		Env:          map[string]string{"A": "1"},
		URL:          "https://mcp.example.com/mcp",
		Headers:      map[string]string{"Authorization": "Bearer ${TOKEN}"},
		ServedBy:     "session-endpoint",
		Tags:         []string{"ctxloom:link_id=pg"},
		Notes:        "notes",
		Installation: "install",
	}
	requireEveryFieldSet(t, want)
	r := &reader{out: &Bundle{}}
	r.addMCP(TreeMCP("remote", want))
	assert.Equal(t, want, r.out.MCP["remote"])
}

// TreeHook and finishHooks are inverses over every field of BundleHook.
func TestTreeHook_RoundTripsEveryField(t *testing.T) {
	order := 7
	want := BundleHook{
		Matcher:         "Bash",
		Command:         "ctxloom",
		Args:            []string{"hook", "session-bind"},
		Type:            "command",
		Prompt:          "prompt",
		Timeout:         5,
		Async:           true,
		PreToolFallback: true,
		Tags:            []string{"ctxloom:link_id=pg"},
		Order:           &order,
	}
	requireEveryFieldSet(t, want)
	r := &reader{out: &Bundle{}, hooks: map[string][]content.Hook{}}
	h := TreeHook(HookEventSessionStart, "guard", want)
	r.hooks[h.Event] = append(r.hooks[h.Event], h)
	r.finishHooks()
	require.Len(t, r.out.Hooks.SessionStart, 1)
	assert.Equal(t, want, r.out.Hooks.SessionStart[0])
}

const remoteTreeDoc = `version: "1.0"
mcp:
  remote:
    url: https://mcp.example.com/mcp
    headers:
      Authorization: Bearer t0ken
    tags: [ctxloom:link_id=remote]
hooks:
  session_start:
    - command: ctxloom
      type: command
      tags: [ctxloom:link_id=remote]
`

// A remote MCP server and a tagged hook survive the whole trip: authored,
// saved to a tree on disk, loaded back by the bundle loader.
func TestLoader_ATreeBundleCarriesRemoteMCPAndTags(t *testing.T) {
	tmpDir := t.TempDir()
	writeTree(t, afero.NewOsFs(), seedBundleRoot(t, tmpDir, paths.LayoutV2), "kit", remoteTreeDoc)
	b, err := NewLoader(NewProjectReader(nil, []string{tmpDir})).Load("kit")
	require.NoError(t, err)

	srv := b.MCP["remote"]
	assert.Equal(t, "https://mcp.example.com/mcp", srv.URL)
	assert.Equal(t, map[string]string{"Authorization": "Bearer t0ken"}, srv.Headers)
	assert.Equal(t, []string{"ctxloom:link_id=remote"}, srv.Tags)
	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Equal(t, []string{"ctxloom:link_id=remote"}, b.Hooks.SessionStart[0].Tags)
}

// The tree item fields are one format generation: a tree written now declares
// it, and an envelope of the generation before it migrates with the marker
// step and nothing else.
func TestExecItemFieldsGeneration_IsWhereTheStepLands(t *testing.T) {
	_, ok := envelopeSteps[execItemFieldsGeneration-1-envelopeKind.Oldest()].(execItemFieldsStep)
	assert.True(t, ok)
}

func TestTreeEnvelope_StampsTheExecItemFieldsGeneration(t *testing.T) {
	raw, err := TreeEnvelope(&Bundle{Version: "1.0.0"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(raw), fmt.Sprintf("%s: %d\n", schemaver.Key, execItemFieldsGeneration)),
		"a tree written now declares the generation whose items may carry url, headers and tags:\n%s", raw)
}

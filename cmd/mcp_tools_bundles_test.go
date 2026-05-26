package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// newTestCtxServer wires a ctxServer with a real-filesystem cfg seeded
// with a remotes.yaml that push_bundle dry-run can resolve against.
// Bundle operations write via os.WriteFile, so an afero memfs can't
// observe them; t.TempDir() provides per-test isolation.
func newTestCtxServer(t *testing.T) (*ctxServer, string) {
	t.Helper()
	tmp := t.TempDir()
	appDir := filepath.Join(tmp, ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.BundlesPath(appDir), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"), []byte(`default: personal
remotes:
  personal:
    url: https://github.com/example/personal-bundles
    version: v1
`), 0644))
	return &ctxServer{cfg: &config.Config{AppPaths: []string{appDir}}}, appDir
}

// newTestSDKClient stands up a real SDK server + in-memory transport pair
// with bundle tools registered, and returns a connected client session.
// Used by schema-validation tests that need the full SDK plumbing — the
// schema is enforced by the SDK at the transport boundary, not in the
// handler bodies, so a direct method call wouldn't catch a missing
// `required` derivation.
func newTestSDKClient(t *testing.T, s *ctxServer) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom-test", Version: "test"}, nil)
	s.registerBundleTools(server)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	// Server must be connected before client (per SDK contract).
	_, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestHandleCreateBundle_WritesFile exercises the dispatch path end-to-end
// against the operations layer: typed input → operations.CreateBundle →
// bundle file on disk. The "no_distill: true" entry keeps this from
// requiring an LLM plugin.
func TestHandleCreateBundle_WritesFile(t *testing.T) {
	s, appDir := newTestCtxServer(t)

	_, result, err := s.handleCreateBundle(context.Background(), nil, createBundleInput{
		Name:        "my-bundle",
		Description: "From MCP",
		Tags:        []string{"test"},
		Fragments: map[string]operations.BundleFragmentInput{
			"intro": {Content: "hi", NoDistill: true},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "created", result.Status)
	assert.Equal(t, "my-bundle", result.Name)

	expectedPath := filepath.Join(paths.BundlesPath(appDir), "my-bundle.yaml")
	_, err = os.Stat(expectedPath)
	require.NoError(t, err, "bundle file should land at expected location")
}

// TestHandleUpdateBundle_PointerOptionalSemantics verifies the legacy
// invariant that's easy to silently break: a typed update with NO
// pointer fields set (omitting set_description, omitting set_version,
// no set_fragments) should be a no-op, returning "no_changes". The
// same update with set_description="new" should write the change and
// return "updated". This guards the *string / *bool optional plumbing.
func TestHandleUpdateBundle_PointerOptionalSemantics(t *testing.T) {
	s, _ := newTestCtxServer(t)

	_, _, err := s.handleCreateBundle(context.Background(), nil, createBundleInput{Name: "seed"})
	require.NoError(t, err)

	// All pointers nil → no-op.
	_, result, err := s.handleUpdateBundle(context.Background(), nil, updateBundleInput{Name: "seed"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "no_changes", result.Status,
		"empty update with all pointer fields nil must not write")

	// SetDescription non-nil → updated.
	newDesc := "new"
	_, result, err = s.handleUpdateBundle(context.Background(), nil, updateBundleInput{
		Name:           "seed",
		SetDescription: &newDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "updated", result.Status,
		"setting a pointer field must trigger an update")
}

// TestHandlePushBundle_DryRun exercises the push handler without a real
// remote or Publisher. The dry-run path produces a "preview" status and
// surfaces enough metadata for the client to display what would be
// pushed.
func TestHandlePushBundle_DryRun(t *testing.T) {
	s, appDir := newTestCtxServer(t)

	_, _, err := s.handleCreateBundle(context.Background(), nil, createBundleInput{Name: "to-push"})
	require.NoError(t, err)
	bundlePath := filepath.Join(paths.BundlesPath(appDir), "to-push.yaml")

	_, result, err := s.handlePushBundle(context.Background(), nil, pushBundleInput{
		Path:    bundlePath,
		DryRun:  true,
		Message: "Add to-push",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "preview", result.Status)
	assert.Equal(t, "personal", result.Remote)
	assert.Equal(t, "ctxloom/v1/bundles/to-push.yaml", result.TargetPath)
	assert.NotEmpty(t, result.Preview)
}

// TestHandleDeleteBundle_RemovesFile checks the dispatch path and the
// filesystem side-effect together. Operations.DeleteBundle has its own
// tests, but this asserts our handler wires them correctly.
func TestHandleDeleteBundle_RemovesFile(t *testing.T) {
	s, appDir := newTestCtxServer(t)

	_, _, err := s.handleCreateBundle(context.Background(), nil, createBundleInput{Name: "doomed"})
	require.NoError(t, err)
	bundlePath := filepath.Join(paths.BundlesPath(appDir), "doomed.yaml")
	_, err = os.Stat(bundlePath)
	require.NoError(t, err, "precondition: bundle should exist")

	_, result, err := s.handleDeleteBundle(context.Background(), nil, deleteBundleInput{Name: "doomed"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deleted", result.Status)

	_, err = os.Stat(bundlePath)
	assert.True(t, os.IsNotExist(err), "bundle file should be gone after handler runs")
}

// TestBundleTools_Registered drives tools/list through a real client and
// asserts every bundle tool registered in registerBundleTools appears.
// If a future change forgets to wire a new bundle tool into the register
// function, this fails. The SDK's reflection only sees what AddTool was
// called for, so this is the only place that catches "tool defined but
// not registered" errors.
func TestBundleTools_Registered(t *testing.T) {
	s, _ := newTestCtxServer(t)
	session := newTestSDKClient(t, s)

	result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	require.NoError(t, err)

	got := make(map[string]bool)
	for _, tool := range result.Tools {
		got[tool.Name] = true
	}

	for _, name := range []string{"create_bundle", "update_bundle", "delete_bundle", "push_bundle"} {
		assert.True(t, got[name], "tool %q should be registered via registerBundleTools", name)
	}
}

// TestBundleTools_RequiredFields verifies the SDK's schema reflection
// correctly marks fields without `omitempty` as required. If someone
// adds omitempty to Name on createBundleInput thinking "it's optional
// anyway", the schema silently drops the required constraint — this
// catches that.
//
// Tool.InputSchema is typed `any` (the SDK accepts either a typed schema
// or a json.RawMessage); after round-tripping through the in-memory
// transport it comes back as a map[string]any. We pull the "required"
// array out by hand and normalize the element type, since JSON-decoded
// arrays land as []any rather than []string.
func TestBundleTools_RequiredFields(t *testing.T) {
	s, _ := newTestCtxServer(t)
	session := newTestSDKClient(t, s)

	result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	require.NoError(t, err)

	wantRequired := map[string][]string{
		"create_bundle": {"name"},
		"update_bundle": {"name"},
		"delete_bundle": {"name"},
		"push_bundle":   {"path"},
	}

	for _, tool := range result.Tools {
		want, ok := wantRequired[tool.Name]
		if !ok {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			schema, ok := tool.InputSchema.(map[string]any)
			require.True(t, ok, "InputSchema should round-trip as map[string]any, got %T", tool.InputSchema)

			rawRequired, _ := schema["required"].([]any)
			got := make([]string, 0, len(rawRequired))
			for _, v := range rawRequired {
				if s, ok := v.(string); ok {
					got = append(got, s)
				}
			}
			assert.Equal(t, want, got,
				"required fields must come from non-omitempty json tags on the input struct")
		})
	}
}

// TestBundleTools_SchemaRejectsMissingName exercises the SDK's
// transport-layer schema validation: calling create_bundle with empty
// arguments must produce isError=true with the SDK's standard
// "missing properties" message. Same wording is asserted by the
// integration test against the real binary — duplicating it here keeps
// the assertion close to the code that triggers it.
func TestBundleTools_SchemaRejectsMissingName(t *testing.T) {
	s, _ := newTestCtxServer(t)
	session := newTestSDKClient(t, s)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_bundle",
		Arguments: map[string]any{},
	})
	require.NoError(t, err, "schema rejection should surface via result.isError, not a transport error")
	require.NotNil(t, result)
	assert.True(t, result.IsError, "missing required field must produce isError=true")

	require.NotEmpty(t, result.Content, "isError result must carry an explanation")
	textContent, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok, "first content block should be text")
	assert.True(t,
		strings.Contains(textContent.Text, `required: missing properties: ["name"]`),
		"error must name the missing 'name' property in the SDK's standard wording, got: %s",
		textContent.Text)
}

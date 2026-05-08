package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// newTestServer wires an mcpServer with a real-filesystem cfg.
// Bundle operations write via os.WriteFile so afero memfs can't observe;
// tempdirs provide isolation.
func newTestServer(t *testing.T) (*mcpServer, string) {
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
	return &mcpServer{cfg: &config.Config{AppPaths: []string{appDir}}}, appDir
}

// TestMCP_LocalToolsList_IncludesBundleTools verifies the four new bundle
// tools are surfaced to clients alongside the existing profile tools.
func TestMCP_LocalToolsList_IncludesBundleTools(t *testing.T) {
	s, _ := newTestServer(t)
	tools := s.getLocalTools()

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	for _, expected := range []string{"create_bundle", "update_bundle", "delete_bundle", "push_bundle"} {
		assert.True(t, names[expected], "tool %q should be in getLocalTools()", expected)
	}
}

// TestMCP_BundleToolSchemas_HaveRequiredFields — sanity-check the tool
// schemas for required fields the AI relies on.
func TestMCP_BundleToolSchemas_HaveRequiredFields(t *testing.T) {
	s, _ := newTestServer(t)
	tools := s.getLocalTools()

	required := map[string][]string{
		"create_bundle": {"name"},
		"update_bundle": {"name"},
		"delete_bundle": {"name"},
		"push_bundle":   {"path"},
	}

	for _, tool := range tools {
		want, ok := required[tool.Name]
		if !ok {
			continue
		}
		req, _ := tool.InputSchema["required"].([]string)
		assert.Equal(t, want, req, "tool %q required fields", tool.Name)
	}
}

// TestMCP_CreateBundleTool_DispatchesToOperations exercises the JSON →
// operations.CreateBundle path end-to-end.
func TestMCP_CreateBundleTool_DispatchesToOperations(t *testing.T) {
	s, appDir := newTestServer(t)

	args := json.RawMessage(`{
		"name": "my-bundle",
		"description": "From MCP",
		"tags": ["test"],
		"fragments": {"intro": {"content": "hi", "no_distill": true}}
	}`)

	out, err := s.toolCreateBundle(context.Background(), args)
	require.NoError(t, err)

	resp := out.(map[string]interface{})
	assert.Equal(t, "created", resp["status"])
	assert.Equal(t, "my-bundle", resp["name"])

	expectedPath := filepath.Join(paths.BundlesPath(appDir), "my-bundle.yaml")
	_, err = os.Stat(expectedPath)
	require.NoError(t, err, "bundle file should land at expected location")
}

// TestMCP_UpdateBundleTool_PassesPointerOptionalFields — confirms that
// JSON omitting optional fields produces nil pointers (no_changes), and
// providing them sets the corresponding fields.
func TestMCP_UpdateBundleTool_PassesPointerOptionalFields(t *testing.T) {
	s, _ := newTestServer(t)

	// Seed a bundle.
	_, err := s.toolCreateBundle(context.Background(), json.RawMessage(`{"name":"seed"}`))
	require.NoError(t, err)

	// Empty update → "no_changes".
	out, err := s.toolUpdateBundle(context.Background(), json.RawMessage(`{"name":"seed"}`))
	require.NoError(t, err)
	resp := out.(map[string]interface{})
	assert.Equal(t, "no_changes", resp["status"])

	// Set description → "updated".
	out, err = s.toolUpdateBundle(context.Background(), json.RawMessage(`{"name":"seed","set_description":"new"}`))
	require.NoError(t, err)
	resp = out.(map[string]interface{})
	assert.Equal(t, "updated", resp["status"])
}

// TestMCP_PushBundleTool_DryRunRoundTrips — end-to-end MCP path for push,
// dry-run only (no Publisher dependency, no network).
func TestMCP_PushBundleTool_DryRunRoundTrips(t *testing.T) {
	s, appDir := newTestServer(t)

	// Seed a bundle on disk.
	_, err := s.toolCreateBundle(context.Background(), json.RawMessage(`{"name":"to-push"}`))
	require.NoError(t, err)
	bundlePath := filepath.Join(paths.BundlesPath(appDir), "to-push.yaml")

	args, _ := json.Marshal(map[string]interface{}{
		"path":    bundlePath,
		"dry_run": true,
		"message": "Add to-push",
	})
	out, err := s.toolPushBundle(context.Background(), args)
	require.NoError(t, err)
	resp := out.(map[string]interface{})

	assert.Equal(t, "preview", resp["status"])
	assert.Equal(t, "personal", resp["remote"])
	assert.Equal(t, "ctxloom/v1/bundles/to-push.yaml", resp["target_path"])
	assert.NotEmpty(t, resp["preview"])
}

// TestMCP_BundleToolSchemas_AreValidJSONSchema compiles each bundle tool's
// InputSchema through the JSON Schema validator. Catches malformed schemas
// before they hit a real client (where AI clients tend to silently drop
// tools with bad schemas).
func TestMCP_BundleToolSchemas_AreValidJSONSchema(t *testing.T) {
	s, _ := newTestServer(t)
	tools := s.getLocalTools()

	bundleNames := map[string]bool{
		"create_bundle": true, "update_bundle": true,
		"delete_bundle": true, "push_bundle": true,
	}

	for _, tool := range tools {
		if !bundleNames[tool.Name] {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			data, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err, "schema must marshal to JSON")

			compiler := jsonschema.NewCompiler()
			require.NoError(t, compiler.AddResource("schema.json", strings.NewReader(string(data))))
			schema, err := compiler.Compile("schema.json")
			require.NoError(t, err, "schema must compile as valid JSON Schema")
			require.NotNil(t, schema)
		})
	}
}

// TestMCP_BundleToolSchemas_RejectInvalidArgs checks that the schemas
// actually reject argument shapes they should — e.g. missing required name.
func TestMCP_BundleToolSchemas_RejectInvalidArgs(t *testing.T) {
	s, _ := newTestServer(t)
	tools := s.getLocalTools()

	cases := map[string]struct {
		bad string // JSON args expected to fail validation
	}{
		"create_bundle": {bad: `{}`},                 // missing name
		"update_bundle": {bad: `{}`},                 // missing name
		"delete_bundle": {bad: `{}`},                 // missing name
		"push_bundle":   {bad: `{"create_pr":true}`}, // missing path
	}

	for _, tool := range tools {
		c, ok := cases[tool.Name]
		if !ok {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			data, _ := json.Marshal(tool.InputSchema)
			compiler := jsonschema.NewCompiler()
			require.NoError(t, compiler.AddResource("schema.json", strings.NewReader(string(data))))
			schema, err := compiler.Compile("schema.json")
			require.NoError(t, err)

			var parsed interface{}
			require.NoError(t, json.Unmarshal([]byte(c.bad), &parsed))
			err = schema.Validate(parsed)
			assert.Error(t, err, "invalid args should fail schema validation")
		})
	}
}

func TestMCP_DeleteBundleTool(t *testing.T) {
	s, appDir := newTestServer(t)

	_, err := s.toolCreateBundle(context.Background(), json.RawMessage(`{"name":"doomed"}`))
	require.NoError(t, err)
	bundlePath := filepath.Join(paths.BundlesPath(appDir), "doomed.yaml")
	_, err = os.Stat(bundlePath)
	require.NoError(t, err)

	out, err := s.toolDeleteBundle(context.Background(), json.RawMessage(`{"name":"doomed"}`))
	require.NoError(t, err)
	resp := out.(map[string]interface{})
	assert.Equal(t, "deleted", resp["status"])

	_, err = os.Stat(bundlePath)
	assert.True(t, os.IsNotExist(err))
}

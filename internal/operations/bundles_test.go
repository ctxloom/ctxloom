package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// setupBundleTestDir creates a real-filesystem .ctxloom layout for bundle tests.
// We use a real tempdir (not afero memmap) because bundles.Bundle.Save writes
// via os.WriteFile, so memmap can't observe it.
func setupBundleTestDir(t *testing.T) (appDir string, cfg *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	appDir = filepath.Join(tmp, ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.BundlesPath(appDir), 0755))
	cfg = &config.Config{AppPaths: []string{appDir}}
	return appDir, cfg
}

// TestCreateBundle_SkeletonOnly drives the minimum-viable shape: name only,
// expect a bundle YAML written to cache/bundles/<name>.yaml with Version 1.0.0
// and no fragments/prompts/mcp.
func TestCreateBundle_SkeletonOnly(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "test-bundle",
	})
	require.NoError(t, err)
	assert.Equal(t, "created", result.Status)
	assert.Equal(t, "test-bundle", result.Name)

	expectedPath := filepath.Join(paths.BundlesPath(appDir), "test-bundle.yaml")
	assert.Equal(t, expectedPath, result.Path)

	data, err := os.ReadFile(expectedPath)
	require.NoError(t, err, "bundle file should be written")

	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "1.0.0", got.Version, "default version")
	assert.Empty(t, got.Fragments)
	assert.Empty(t, got.Prompts)
	assert.Empty(t, got.MCP)
}

func TestCreateBundle_AlreadyExists(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "dup"})
	require.NoError(t, err)

	_, err = CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "dup"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateBundle_NameRequired(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

// TestCreateBundle_WithFragments_NoDistill verifies fragments are stored
// verbatim when no_distill is set. Distillation behavior is exercised in L3;
// here we ensure the fragment-input → BundleFragment mapping is correct.
func TestCreateBundle_WithFragments_NoDistill(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "with-frags",
		Fragments: map[string]BundleFragmentInput{
			"intro": {
				Content:   "Hello world",
				Tags:      []string{"basic"},
				NoDistill: true,
			},
		},
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	frag, ok := got.Fragments["intro"]
	require.True(t, ok, "fragment should be present")
	assert.Equal(t, "Hello world", frag.Content)
	assert.Equal(t, []string{"basic"}, frag.Tags)
	assert.True(t, frag.NoDistill)
	assert.Empty(t, frag.Distilled, "no_distill skips distillation")
	assert.Empty(t, frag.DistilledBy)
}

func TestCreateBundle_WithPrompts(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "with-prompts",
		Prompts: map[string]BundlePromptInput{
			"review": {
				Content:     "Review this code for X.",
				Description: "Code review prompt",
				Tags:        []string{"review"},
				NoDistill:   true,
			},
		},
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	prompt, ok := got.Prompts["review"]
	require.True(t, ok)
	assert.Equal(t, "Review this code for X.", prompt.Content)
	assert.Equal(t, "Code review prompt", prompt.Description)
	assert.Equal(t, []string{"review"}, prompt.Tags)
}

func TestCreateBundle_WithMCPServers(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "with-mcp",
		MCPServers: map[string]BundleMCPInput{
			"tree-sitter": {
				Command: "tree-sitter-mcp",
				Args:    []string{"--lang", "rust"},
				Env:     map[string]string{"DEBUG": "1"},
			},
		},
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	mcp, ok := got.MCP["tree-sitter"]
	require.True(t, ok)
	assert.Equal(t, "tree-sitter-mcp", mcp.Command)
	assert.Equal(t, []string{"--lang", "rust"}, mcp.Args)
	assert.Equal(t, map[string]string{"DEBUG": "1"}, mcp.Env)
}

// recordingDistiller captures Distill invocations for tests; the operations
// layer accepts any Distiller, with nil meaning "skip distillation". The MCP
// layer wires up the real LLM-backed distiller at the boundary.
type recordingDistiller struct {
	calls       []distillCall
	returnValue string
	returnModel string
	returnErr   error
}

type distillCall struct {
	Name    string
	Content string
}

func (d *recordingDistiller) Distill(_ context.Context, name, content string) (string, string, error) {
	d.calls = append(d.calls, distillCall{Name: name, Content: content})
	return d.returnValue, d.returnModel, d.returnErr
}

// TestCreateBundle_DistillsFragmentByDefault verifies that when a Distiller is
// provided and no_distill is unset, fragments are distilled and the result is
// captured into the saved bundle (Distilled, DistilledBy, ContentHash).
func TestCreateBundle_DistillsFragmentByDefault(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	d := &recordingDistiller{returnValue: "DISTILLED", returnModel: "mock-model"}

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "with-distill",
		Distiller: d,
		Fragments: map[string]BundleFragmentInput{
			"intro": {Content: "Long original content that gets compressed."},
		},
	})
	require.NoError(t, err)
	require.Len(t, d.calls, 1, "distiller should be called once for the fragment")
	assert.Equal(t, "intro", d.calls[0].Name)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	frag := got.Fragments["intro"]
	assert.Equal(t, "DISTILLED", frag.Distilled)
	assert.Equal(t, "mock-model", frag.DistilledBy)
	assert.NotEmpty(t, frag.ContentHash, "hash should be set so NeedsDistill knows distill is current")
}

// TestCreateBundle_DistillFailureWarnsAndContinues — a distill error must not
// fail the create. Bundle saves with raw content and empty distilled fields,
// matching the project's fault-tolerance philosophy (CLAUDE.md).
func TestCreateBundle_DistillFailureWarnsAndContinues(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	d := &recordingDistiller{returnErr: assert.AnError}

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "distill-fails",
		Distiller: d,
		Fragments: map[string]BundleFragmentInput{
			"intro": {Content: "raw"},
		},
	})
	require.NoError(t, err, "distill failure must not fail the create")

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	frag := got.Fragments["intro"]
	assert.Equal(t, "raw", frag.Content)
	assert.Empty(t, frag.Distilled)
	assert.Empty(t, frag.DistilledBy)
}

// TestCreateBundle_DistillSkippedWhenNoDistillTrue — per-fragment opt-out.
func TestCreateBundle_DistillSkippedWhenNoDistillTrue(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	d := &recordingDistiller{returnValue: "X", returnModel: "m"}

	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "no-distill",
		Distiller: d,
		Fragments: map[string]BundleFragmentInput{
			"a": {Content: "skip me", NoDistill: true},
			"b": {Content: "distill me"},
		},
	})
	require.NoError(t, err)
	require.Len(t, d.calls, 1, "only the non-no_distill fragment should be distilled")
	assert.Equal(t, "b", d.calls[0].Name)
}

// stringPtr / boolPtr — pointer helpers to express "set this field" vs "leave
// it alone" in UpdateBundleRequest. Mirrors the *string pattern in
// UpdateProfileRequest.
func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

// createSeedBundle is shared L4 setup: produces a bundle on disk that
// UpdateBundle tests can mutate.
func createSeedBundle(t *testing.T, cfg *config.Config, name string) {
	t.Helper()
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:        name,
		Description: "seed",
		Tags:        []string{"alpha"},
		Fragments: map[string]BundleFragmentInput{
			"intro": {Content: "intro content", NoDistill: true},
		},
	})
	require.NoError(t, err)
}

func TestUpdateBundle_NotFound(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{Name: "missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestUpdateBundle_SetDescription uses a *string to distinguish "set to empty"
// from "leave alone". Empty pointer means no change.
func TestUpdateBundle_SetDescription(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	result, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:           "seed",
		SetDescription: stringPtr("updated description"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "updated description")

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "updated description", got.Description)
}

// TestUpdateBundle_AddRemoveTags exercises tag set semantics: adding an
// existing tag is a no-op (no duplicate), removing an absent tag is a no-op.
func TestUpdateBundle_AddRemoveTags(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed") // seed has tags: ["alpha"]

	result, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:       "seed",
		AddTags:    []string{"alpha", "beta"}, // alpha already present → no dup
		RemoveTags: []string{"absent"},        // absent → no-op
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	assert.ElementsMatch(t, []string{"alpha", "beta"}, got.Tags)
}

// TestUpdateBundle_SetFragmentNew_Distills — adding a fragment via SetFragments
// goes through the distiller (matching CreateBundle behavior).
func TestUpdateBundle_SetFragmentNew_Distills(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	d := &recordingDistiller{returnValue: "DISTILLED-NEW", returnModel: "m"}

	result, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:      "seed",
		Distiller: d,
		SetFragments: map[string]BundleFragmentInput{
			"new-frag": {Content: "fresh content"},
		},
	})
	require.NoError(t, err)
	require.Len(t, d.calls, 1)
	assert.Equal(t, "new-frag", d.calls[0].Name)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "DISTILLED-NEW", got.Fragments["new-frag"].Distilled)
}

// TestUpdateBundle_SetFragmentExisting_Redistills — overwriting a fragment's
// content triggers re-distillation.
func TestUpdateBundle_SetFragmentExisting_Redistills(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed") // intro has NoDistill=true; we override

	d := &recordingDistiller{returnValue: "DISTILLED-NEW", returnModel: "m"}
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:      "seed",
		Distiller: d,
		SetFragments: map[string]BundleFragmentInput{
			"intro": {Content: "rewritten content"}, // NoDistill defaults false
		},
	})
	require.NoError(t, err)
	require.Len(t, d.calls, 1)
	assert.Equal(t, "rewritten content", d.calls[0].Content,
		"distiller should see the new content")
}

func TestUpdateBundle_RemoveFragment(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	result, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:            "seed",
		RemoveFragments: []string{"intro", "ghost"}, // ghost absent → no-op
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.NotContains(t, got.Fragments, "intro")
}

// TestUpdateBundle_DistillOptOutWholesale — distill: false at the request
// level skips ALL fragment distillation, even those without NoDistill.
func TestUpdateBundle_DistillOptOutWholesale(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	d := &recordingDistiller{returnValue: "X", returnModel: "m"}
	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:      "seed",
		Distill:   boolPtr(false),
		Distiller: d,
		SetFragments: map[string]BundleFragmentInput{
			"a": {Content: "raw a"},
			"b": {Content: "raw b"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, d.calls, "wholesale opt-out should bypass distill entirely")
}

// TestUpdateBundle_NoChanges_ReturnsNoChangesStatus — empty request returns
// status "no_changes" so callers can distinguish from real updates.
func TestUpdateBundle_NoChanges_ReturnsNoChangesStatus(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	result, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "seed",
	})
	require.NoError(t, err)
	assert.Equal(t, "no_changes", result.Status)
	assert.Empty(t, result.Changes)
}

// TestUpdateBundle_PromptAndMCPMutations — single happy-path covering both
// prompt and MCP add/remove (parity with fragment paths already exercised).
func TestUpdateBundle_PromptAndMCPMutations(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "seed")

	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "seed",
		SetPrompts: map[string]BundlePromptInput{
			"p1": {Content: "prompt content", NoDistill: true},
		},
		SetMCPServers: map[string]BundleMCPInput{
			"server1": {Command: "cmd"},
		},
	})
	require.NoError(t, err)

	// Now remove what we just added.
	result, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:             "seed",
		RemovePrompts:    []string{"p1"},
		RemoveMCPServers: []string{"server1"},
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))

	assert.NotContains(t, got.Prompts, "p1")
	assert.NotContains(t, got.MCP, "server1")
}

func TestDeleteBundle_Success(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	createSeedBundle(t, cfg, "doomed")

	result, err := DeleteBundle(context.Background(), cfg, DeleteBundleRequest{Name: "doomed"})
	require.NoError(t, err)
	assert.Equal(t, "deleted", result.Status)
	assert.Equal(t, "doomed", result.Name)

	_, statErr := os.Stat(result.Path)
	assert.True(t, os.IsNotExist(statErr), "bundle file should be gone")
}

func TestDeleteBundle_NotFound(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	_, err := DeleteBundle(context.Background(), cfg, DeleteBundleRequest{Name: "missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestCreateBundle_WithDescriptionTagsAuthor verifies that optional metadata
// fields round-trip through the saved YAML.
func TestCreateBundle_WithDescriptionTagsAuthor(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	result, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:        "metadata-bundle",
		Description: "Pinned for review",
		Version:     "2.5.0",
		Tags:        []string{"rust", "test"},
		Author:      "alice",
	})
	require.NoError(t, err)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)

	var got bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, "2.5.0", got.Version, "explicit version overrides default")
	assert.Equal(t, "Pinned for review", got.Description)
	assert.Equal(t, []string{"rust", "test"}, got.Tags)
	assert.Equal(t, "alice", got.Author)
}

package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
)

// regenTestApp creates a project layout (appDir with a bundles dir) and a
// separate workDir, returning both.
func regenTestApp(t *testing.T) (appDir, workDir string) {
	t.Helper()
	tmp := t.TempDir()
	appDir = filepath.Join(tmp, ".ctxloom")
	require.NoError(t, os.MkdirAll(authoredV1(appDir), 0o755))
	workDir = filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	return appDir, workDir
}

func writeRegenBundle(t *testing.T, appDir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(
		filepath.Join(authoredV1(appDir), name+".yaml"), []byte(content), 0o644))
}

// TestRegenerateContext_AppliesExcludeFragments pins regenerateContext to the
// AssembleContext collection path: a tag-matched fragment the profile excludes
// must NOT appear in the regenerated context file. The old parallel logic
// appended tag matches with no ExcludeFragments filtering, so user-excluded
// fragments were still injected at SessionStart.
func TestRegenerateContext_AppliesExcludeFragments(t *testing.T) {
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  keep-me:
    tags: ["security"]
    content: "KEEP-CONTENT"
  banned:
    tags: ["security"]
    content: "BANNED-CONTENT"
`)

	cfg := cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"default": {
			SelectTags:       []string{"security"},
			ExcludeFragments: []string{"banned"},
		},
	}, config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
	})

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Contains(t, content, "KEEP-CONTENT")
	assert.NotContains(t, content, "BANNED-CONTENT",
		"a fragment excluded by the profile must not be injected at SessionStart")
}

// TestRegenerateContext_UsesDefaultAgentProfiles pins that the regenerate path
// reads the default AGENT's composed profiles (Config.DefaultAgentProfiles —
// profiles.defaults and its home-inheritance were retired) and regenerates a
// non-empty injected context from them.
func TestRegenerateContext_UsesDefaultAgentProfiles(t *testing.T) {
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["go"]
    content: "DEFAULT-AGENT-CONTENT"
`)
	// TWO directory profiles, so nothing masks the default-agent selection.
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "devprof.yaml"),
		[]byte("description: dev default\nselect_tags: [go]\nbundles: [dev]\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "other.yaml"),
		[]byte("description: unrelated\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"devprof"}}},
	})

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash,
		"the default agent's profiles must produce a non-empty injected context")

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Contains(t, content, "DEFAULT-AGENT-CONTENT")
}

// TestRegenerateContext_NoDuplicateFragmentContent pins the dedupe half of the
// parity fix: a fragment reachable both via the profile's tags and via its
// bundle expansion must be written once. The old code appended tag matches
// under BARE names while bundle expansion used canonical qualified names,
// defeating dedupeFragmentRefs.
func TestRegenerateContext_NoDuplicateFragmentContent(t *testing.T) {
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["go"]
    content: "UNIQUE-MARKER"
`)

	cfg := cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		// Both the tag AND the whole bundle reference the same fragment.
		"default": {SelectTags: []string{"go"}, Bundles: []string{"dev"}},
	}, config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
	})

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(content, "UNIQUE-MARKER"),
		"a fragment reachable via tag and bundle must be injected once")
}

// TestUpdateProfile_ValidationFailureIsRejected pins the validate-then-mutate
// ordering: a bad AddParents validation fails the update up front, before any
// profile write.
func TestUpdateProfile_ValidationFailureIsRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/base.yaml",
		[]byte("description: base\n"), 0o644))
	loader := profiles.NewLoader([]string{"/profiles"}, profiles.WithFS(fs))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{"/app"}})

	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		AddParents: []string{"missing-parent"},
		Loader:     loader,
	})
	require.Error(t, err)
}

// TestRegenerateContext_WithholdsPremisedFragments pins regenerateContext to
// the premise filter AssembleContext applies, and it is the same class of bug
// as TestRegenerateContext_AppliesExcludeFragments above: this path
// hand-rolls its own copy of the ingest loop, so every selection rule added to
// AssembleContext has to be added here too or the two silently disagree.
//
// A premised fragment is CONDITIONAL — it is withheld from unconditional
// assembly and offered to the agent to ask for by name. Injecting it at
// SessionStart delivers, unconditionally, the exact content the mechanism
// exists to hold back, and it does so on the path the agent never chose. The
// premise commit added the filter to AssembleContext and never touched this
// function.
//
// It asserts the EFFECT — the bytes of the regenerated context file — and
// pins BOTH sides of the rule: the premiseless fragment must still be there,
// so a filter that simply withheld everything cannot pass. The second half
// asserts the two paths AGREE, which is the invariant regenerateContext's own
// doc states ("This function's output MUST match AssembleContext").
func TestRegenerateContext_WithholdsPremisedFragments(t *testing.T) {
	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  always:
    tags: ["security"]
    content: "UNCONDITIONAL-CONTENT"
  conditional:
    tags: ["security"]
    premise: "You are about to remove a worktree."
    content: "PREMISED-CONTENT"
`)

	cfg := cfgWithDirProfiles(t, afero.NewOsFs(), appDir, map[string]config.Profile{
		"default": {SelectTags: []string{"security"}},
	}, config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
	})

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Contains(t, content, "UNCONDITIONAL-CONTENT",
		"a fragment with no premise applies unconditionally and must still be injected")
	assert.NotContains(t, content, "PREMISED-CONTENT",
		"a premised fragment is withheld from unconditional assembly; injecting it at "+
			"SessionStart delivers the content the premise mechanism exists to hold back")

	// The other half of the invariant: the launch path must reach the same
	// verdict on the same corpus, and must OFFER what it withheld.
	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profiles: []string{"default"}})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "UNCONDITIONAL-CONTENT")
	assert.NotContains(t, res.Context, "PREMISED-CONTENT",
		"AssembleContext is the reference behaviour regenerateContext must match")
	require.Len(t, res.PremiseIndex, 1,
		"the withheld fragment must be OFFERED on the launch path, not merely dropped")
	// The index names the CANONICAL qualified ref, which is also the key the
	// filter withholds on -- not the bare fragment name.
	assert.Equal(t, "ctxloom+local:dev#fragments/conditional", res.PremiseIndex[0].Name)
}

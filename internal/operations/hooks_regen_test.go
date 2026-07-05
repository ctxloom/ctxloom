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

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// regenTestApp creates a project layout (appDir with a bundles dir) and a
// separate workDir, returning both.
func regenTestApp(t *testing.T) (appDir, workDir string) {
	t.Helper()
	tmp := t.TempDir()
	appDir = filepath.Join(tmp, ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "cache", "bundles"), 0o755))
	workDir = filepath.Join(tmp, "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	return appDir, workDir
}

func writeRegenBundle(t *testing.T, appDir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(
		filepath.Join(appDir, "cache", "bundles", name+".yaml"), []byte(content), 0o644))
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

	cfg := &config.Config{
		AppPaths: []string{appDir},
		Profiles: config.ProfilesConfig{
			Defaults: []string{"default"},
			Definitions: map[string]config.Profile{
				"default": {
					SelectTags:       []string{"security"},
					ExcludeFragments: []string{"banned"},
				},
			},
		},
	}

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Contains(t, content, "KEEP-CONTENT")
	assert.NotContains(t, content, "BANNED-CONTENT",
		"a fragment excluded by the profile must not be injected at SessionStart")
}

// TestRegenerateContext_InheritsHomeDefaults pins the EnsureDefaultProfiles
// call: ApplyHooks reloads a fresh config, so a project that inherits its
// defaults.profiles from the HOME config must still regenerate a non-empty
// context — the old code iterated cfg.GetDefaultProfiles() without the
// inheritance step and wrote nothing.
func TestRegenerateContext_InheritsHomeDefaults(t *testing.T) {
	home := testsupport.Isolate(t)
	homeApp := filepath.Join(home, ".ctxloom")
	require.NoError(t, os.MkdirAll(homeApp, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(homeApp, "config.yaml"),
		[]byte("profiles:\n  defaults:\n    - homeprof\n"), 0o644))

	appDir, workDir := regenTestApp(t)
	writeRegenBundle(t, appDir, "dev", `version: "1.0"
fragments:
  rules:
    tags: ["go"]
    content: "HOME-PROFILE-CONTENT"
`)
	// TWO directory profiles, so the single-installed-profile fallback cannot
	// mask a missing inheritance step.
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "homeprof.yaml"),
		[]byte("description: home default\nselect_tags: [go]\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "other.yaml"),
		[]byte("description: unrelated\n"), 0o644))

	cfg := &config.Config{AppPaths: []string{appDir}}

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash,
		"home-inherited default profiles must produce a non-empty injected context")

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Contains(t, content, "HOME-PROFILE-CONTENT")

	// And the inheritance stays run-only even here.
	assert.Empty(t, cfg.ExplicitDefaultProfiles(),
		"inherited defaults must not become explicit project defaults")
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

	cfg := &config.Config{
		AppPaths: []string{appDir},
		Profiles: config.ProfilesConfig{
			Defaults: []string{"default"},
			Definitions: map[string]config.Profile{
				// Both the tag AND the whole bundle reference the same fragment.
				"default": {SelectTags: []string{"go"}, Bundles: []string{"dev"}},
			},
		},
	}

	hash, err := regenerateContext(cfg, workDir, nil)
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	content, err := agent.ReadContextFile(workDir, hash)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(content, "UNIQUE-MARKER"),
		"a fragment reachable via tag and bundle must be injected once")
}

// TestUpdateProfile_ValidationFailureLeavesConfigUntouched pins the
// validate-then-mutate ordering: a failed AddParents validation must leave the
// in-memory default-profile change unapplied, or a later unrelated cfg.Save()
// would persist it.
func TestUpdateProfile_ValidationFailureLeavesConfigUntouched(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/base.yaml",
		[]byte("description: base\n"), 0o644))
	loader := profiles.NewLoader([]string{"/profiles"}, profiles.WithFS(fs))
	cfg := &config.Config{AppPaths: []string{"/app"}}

	def := true
	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		Default:    &def,
		AddParents: []string{"missing-parent"},
		Loader:     loader,
	})
	require.Error(t, err)
	assert.False(t, cfg.Profiles.IsDefaultProfile("base"),
		"a failed update must not leave the default-flag mutation in cfg")
}

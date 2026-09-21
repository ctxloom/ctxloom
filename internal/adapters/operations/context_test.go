// Context assembly tests verify the core ctxloom functionality of combining
// fragments, profiles, and variables into a single context document for
// AI consumption. These tests ensure that context is assembled correctly
// from multiple sources and that variable substitution works as expected.
package operations

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/resources"
)

// mockProfileLoader is a mock ProfileLoader for testing.
type mockProfileLoader struct {
	resolveFunc func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error)
}

func (m *mockProfileLoader) ResolveProfile(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(name, visited)
	}
	return nil, errors.New("profile not found")
}

// =============================================================================
// AssembleContextRequest Tests
//
// These tests verify that request objects correctly capture user intent for
// context assembly, enabling proper fragment selection and variable binding.
// =============================================================================

// TestAssembleContextRequest_Defaults verifies that a zero-value request has
// sensible defaults (empty collections, no profile). This ensures callers
// can create requests incrementally without unexpected behavior.
func TestAssembleContextRequest_Defaults(t *testing.T) {
	req := AssembleContextRequest{}

	assert.Empty(t, req.Profile)
	assert.Nil(t, req.Fragments)
	assert.Nil(t, req.Tags)
}

func TestAssembleContextRequest_WithProfile(t *testing.T) {
	req := AssembleContextRequest{
		Profile: "my-profile",
	}

	assert.Equal(t, "my-profile", req.Profile)
}

func TestAssembleContextRequest_WithFragments(t *testing.T) {
	req := AssembleContextRequest{
		Fragments: []string{"frag1", "frag2"},
	}

	assert.Equal(t, []string{"frag1", "frag2"}, req.Fragments)
}

func TestAssembleContextRequest_WithTags(t *testing.T) {
	req := AssembleContextRequest{
		Tags: []string{"go", "testing"},
	}

	assert.Equal(t, []string{"go", "testing"}, req.Tags)
}

func TestAssembleContextRequest_Combined(t *testing.T) {
	req := AssembleContextRequest{
		Profile:   "main",
		Fragments: []string{"frag1"},
		Tags:      []string{"important"},
	}

	assert.Equal(t, "main", req.Profile)
	assert.Len(t, req.Fragments, 1)
	assert.Len(t, req.Tags, 1)
}

func TestAssembleContextResult_Fields(t *testing.T) {
	result := AssembleContextResult{
		Profiles:        []string{"my-profile"},
		FragmentsLoaded: []string{"frag1", "frag2"},
		Context:         "Full assembled context here",
	}

	assert.Equal(t, []string{"my-profile"}, result.Profiles)
	assert.Equal(t, []string{"frag1", "frag2"}, result.FragmentsLoaded)
	assert.Contains(t, result.Context, "assembled context")
}

func TestAssembleContextResult_Empty(t *testing.T) {
	result := AssembleContextResult{
		Context:         "",
		Profiles:        []string{},
		FragmentsLoaded: []string{},
	}

	assert.Empty(t, result.Context)
	assert.Empty(t, result.Profiles)
	assert.Empty(t, result.FragmentsLoaded)
}

func TestAssembleContextResult_MultipleProfiles(t *testing.T) {
	result := AssembleContextResult{
		Profiles:        []string{"base", "dev", "local"},
		FragmentsLoaded: []string{"common", "dev-tools"},
		Context:         "content",
	}

	assert.Len(t, result.Profiles, 3)
	assert.Len(t, result.FragmentsLoaded, 2)
}

// withProfileDefs returns a config carrying cfg's fields plus defs, with defs
// written as DIRECTORY profiles into the config's own filesystem.
//
// It used to add them to the inline `profiles.definitions` map. That arm is
// retired: a profile is a file, so the definitions are marshalled into
// <appDir>/profiles/<name>.yaml through cfg.FS() — which is the memfs the
// caller is already driving, wherever there is one, so a memfs test stays a
// memfs test.
//
// The config is still REBUILT rather than mutated. ToFixture deep-copies, so
// writing into a fixture taken off a live config was always a no-op; that
// remains true and is why this returns a new config rather than amending one.
func withProfileDefs(t *testing.T, cfg *config.Config, defs map[string]config.Profile) *config.Config {
	t.Helper()
	f := cfg.ToFixture()
	// Write where the config can READ. An injected fs is authoritative. With
	// none, the config reads the OS filesystem — unless its appDir is one of
	// the synthetic memfs paths these tests use (testBaseDir is "/project/…",
	// which cannot be created and must not be attempted), in which case the
	// config gets a memfs of its own. Those fixtures take their bundles from an
	// injected pipeline, so the filesystem only ever holds the profile.
	appDir := ""
	if len(f.AppPaths) > 0 {
		appDir = f.AppPaths[0]
	} else {
		appDir = filepath.Join(t.TempDir(), paths.AppDirName)
		f.AppPaths = append(f.AppPaths, appDir)
	}

	// Write where the config can READ. An injected fs is authoritative. With
	// none, the config reads the OS filesystem — unless its appDir is one of
	// the synthetic memfs paths these tests use (testBaseDir is "/project/…",
	// which cannot be created and must not be attempted), in which case the
	// config gets a memfs of its own. Those fixtures take their bundles from an
	// injected pipeline, so the filesystem only ever holds the profile.
	fs := cfg.FS()
	if fs == nil {
		fs = afero.NewOsFs()
		if _, err := fs.Stat(filepath.Dir(appDir)); err != nil {
			fs = afero.NewMemMapFs()
		}
	}
	seed := make(map[string]any, len(defs))
	for name, p := range defs {
		seed[name] = p
	}
	testsupport.WriteDirProfiles(t, fs, appDir, seed)
	out := gatedFixture(f)
	out.SetFS(fs)
	return out
}

// installProfileDefs is DELETED. It rebuilt a Config in place (`*cfg =
// *rebuilt`) so a mid-run test double could make a profile definition appear in
// a config the sync loop was already holding.
//
// That simulated a channel production cannot use: config-defined profiles are
// fixed when the config loads, since nothing rewrites .ctxloom/config.yaml
// during a sync. Real revelation happens because a pull writes BYTES TO DISK
// and the next collect pass lists the profiles directory again through a fresh
// loader. The doubles now write the file (see revealProfileOnDisk in
// sync_test.go), which is both faithful and what their own doc comments always
// claimed they did.
//
// Deleting it also removed the last value-copy of Config outside a trivial
// table test, which is what lets companionSeed be a value sync.Once field —
// making Config non-copyable, so `go vet` now REFUSES to let this pattern
// return.

// ========== Loader-based integration tests ==========

func setupContextTestFS(t *testing.T) (afero.Fs, *bundles.Loader) {
	t.Helper()
	// Isolate HOME so nothing in the assembly path can read the developer's real
	// ~/.ctxloom/config.yaml — keeping these tests deterministic regardless of the
	// machine they run on.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()

	// Create bundles directory
	_ = fs.MkdirAll(authoredV1(testBaseDir), 0755)

	// Create test bundle with fragments
	bundleContent := `version: "1.0"
description: Test bundle for context
fragments:
  security-rules:
    tags: ["security", "rules"]
    content: |
      ## Security Rules
      - Always validate input
      - Never trust user data
  go-patterns:
    tags: ["go", "patterns"]
    content: |
      ## Go Patterns
      - Use interfaces for dependencies
      - Handle errors explicitly
  testing-guidelines:
    tags: ["testing", "tdd"]
    content: |
      ## Testing Guidelines
      - Write tests first
      - Test edge cases
  variable-content:
    tags: ["variables"]
    content: |
      Project: {{project_name}}
      Version: {{version}}
`
	_ = afero.WriteFile(fs, authoredV1(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644)

	loader := bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))
	return fs, loader
}

func TestAssembleContext_WithTags(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:     []string{"security"},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
	assert.Contains(t, result.Context, "Security Rules")
}

// TestAssembleContext_TagMatchingNothingIsReported proves an explicit -t tag
// that matches zero fragments is surfaced via MissingTags —
// previously AssembleContext returned Context: "" with a nil error and no
// warning at all, indistinguishable from "no tags were asked for".
func TestAssembleContext_TagMatchingNothingIsReported(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:     []string{"no-such-tag-anywhere"},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"no-such-tag-anywhere"}, result.MissingTags,
		"a tag selection that matches nothing must be named in MissingTags")
}

// TestAssembleContext_TagThatMatchesIsNotReportedMissing is the control: a
// real match must never appear in MissingTags.
func TestAssembleContext_TagThatMatchesIsNotReportedMissing(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:     []string{"security"},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Empty(t, result.MissingTags)
}

func TestAssembleContext_WithFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{"dev#fragments/go-patterns"},
		Pipeline:  opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Len(t, result.FragmentsLoaded, 1)
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_MultipleFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{
			"dev#fragments/security-rules",
			"dev#fragments/go-patterns",
		},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Len(t, result.FragmentsLoaded, 2)
	assert.Contains(t, result.Context, "Security Rules")
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_DeduplicatesFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{
			"dev#fragments/security-rules",
			"dev#fragments/security-rules", // Duplicate
		},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// Should deduplicate.
	assert.Len(t, result.FragmentsLoaded, 1)
}

func TestAssembleContext_WithProfileFromConfig(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"go-dev": {
			Description: "Go developer profile",
			SelectTags:  []string{"go"},
			Fragments:   []config.FragmentRef{{Name: "dev#fragments/testing-guidelines"}},
		},
	}, config.Fixture{})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "go-dev",
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"go-dev"}, result.Profiles)
	// Should include fragments from select_tags AND direct fragments
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
}

// TestAssembleContext_ProfileTags_DoNotSelectContent pins that a profile's
// `tags:` are descriptive only — they must never pull fragments in by tag.
// Regression for the coordinator/finder bloat: a bundle's descriptive tag,
// inherited by every fragment, would otherwise drag the whole bundle in.
func TestAssembleContext_ProfileTags_DoNotSelectContent(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"go-dev": {
			Description: "Go developer profile",
			Tags:        []string{"go"}, // descriptive only
		},
	}, config.Fixture{})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "go-dev",
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Empty(t, result.FragmentsLoaded, "a profile's tags describe it; they never select content")
	assert.NotContains(t, result.Context, "Go Patterns")
}

// TestAssembleContext_ProfileSelectTags_SelectContent pins that `select_tags:`
// selects fragment content by tag — the role `tags:` used to play.
func TestAssembleContext_ProfileSelectTags_SelectContent(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"go-dev": {
			Description: "Go developer profile",
			SelectTags:  []string{"go"},
		},
	}, config.Fixture{})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "go-dev",
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_ProfileLLMSurfaces(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"go-dev": {
			LLM:       "agy-code",
			Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
		},
		"plain": {
			Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
		},
	}, config.Fixture{})

	withLLM, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "go-dev", Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)
	assert.Equal(t, "agy-code", withLLM.ProfileLLM)

	withoutLLM, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "plain", Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)
	assert.Empty(t, withoutLLM.ProfileLLM)
}

func TestAssembleContext_ProfileWithVariables(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"project": {
			Description: "Project profile",
			Fragments:   []config.FragmentRef{{Name: "dev#fragments/variable-content"}},
			Variables: map[string]string{
				"project_name": "MyProject",
				"version":      "1.0.0",
			},
		},
	}, config.Fixture{})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "project",
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// Variables should be substituted
	assert.Contains(t, result.Context, "MyProject")
	assert.Contains(t, result.Context, "1.0.0")
}

// TestAssembleContext_UndefinedVariableWarns pins the substitution-warning
// WIRING bug: substituteVariables has always computed the "undefined
// variable" message, but AssembleContext (via loadAssembledContext) used to
// pass a no-op warnFunc, so the message was computed and thrown away. This
// proves the warning now reaches the user through the standard clidiag line,
// matching the promise in docs/concepts/fragments.md ("An undefined variable
// renders empty and produces a warning").
func TestAssembleContext_UndefinedVariableWarns(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"project-partial": {
			Fragments: []config.FragmentRef{{Name: "dev#fragments/variable-content"}},
			// project_name is bound; version is deliberately left undefined.
			Variables: map[string]string{"project_name": "MyProject"},
		},
	}, config.Fixture{})

	var result *AssembleContextResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Profile:  "project-partial",
			Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err)
	})

	assert.Contains(t, result.Context, "MyProject")
	assert.Contains(t, stderr, "ctxloom: warning: undefined variable: {{version}}")
}

// TestAssembleContext_DefinedVariablesDoNotWarn is the negative case: when
// every referenced variable is bound, no warning fires at all.
func TestAssembleContext_DefinedVariablesDoNotWarn(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"project-full": {
			Fragments: []config.FragmentRef{{Name: "dev#fragments/variable-content"}},
			Variables: map[string]string{"project_name": "MyProject", "version": "9.9.9"},
		},
	}, config.Fixture{})

	stderr := captureStderr(t, func() {
		_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Profile:  "project-full",
			Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err)
	})

	assert.Empty(t, stderr, "no warning when every referenced variable is bound")
}

// TestAssembleContext_TemplateParseFailureWarnsAndReturnsContentUnchanged
// covers the other warnFunc path substituteVariables exercises: a fragment
// whose mustache markup itself fails to parse (here, a close tag with no
// matching open). The broken content is returned VERBATIM (fault tolerance:
// never silently dropped) and the parse failure warns exactly like the
// undefined-variable case.
func TestAssembleContext_TemplateParseFailureWarnsAndReturnsContentUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(authoredV1(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Test bundle with a broken template
fragments:
  broken-template:
    content: |
      {{/unopened}}
`
	require.NoError(t, afero.WriteFile(fs, authoredV1(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644))
	loader := bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))

	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"broken": {Fragments: []config.FragmentRef{{Name: "dev#fragments/broken-template"}}},
	}, config.Fixture{})

	var result *AssembleContextResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Profile:  "broken",
			Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err)
	})

	assert.Contains(t, result.Context, "{{/unopened}}", "a template that fails to parse is returned unchanged, not swallowed")
	assert.Contains(t, stderr, "ctxloom: warning: failed to parse template:")
}

// TestAssembleContext_UndefinedVariableWarningDedupesAcrossRepeatedCalls
// guards against spam: AssembleContext is called repeatedly in a live
// session (once per conversation turn — internal/adapters/cli/run.go, acp_cmd.go), so
// a fragment with one undefined variable must not print a fresh warning line
// on every single call. The dedup is process-wide (clidiag.WarnOnce, the
// same mechanism strictness.FailOnce already uses for exactly this
// "re-fires per subsystem" shape), so the SECOND assembly in a process
// produces no additional output for an identical message.
//
// Uses a dedicated fragment/variable name (not the shared "variable-content"
// fixture other tests in this file also render) so this test's dedup-key
// occupancy in clidiag's process-global onceSeen set can never race another
// test's expectation depending on test execution order.
func TestAssembleContext_UndefinedVariableWarningDedupesAcrossRepeatedCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(authoredV1(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Test bundle for the dedup case
fragments:
  dedup-fragment:
    content: |
      Repeat check: {{dedup_check_variable}}
`
	require.NoError(t, afero.WriteFile(fs, authoredV1(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644))
	loader := bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))

	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"project-dedup": {Fragments: []config.FragmentRef{{Name: "dev#fragments/dedup-fragment"}}},
	}, config.Fixture{})

	assemble := func() string {
		return captureStderr(t, func() {
			_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
				Profile:  "project-dedup",
				Pipeline: opPipe(cfg, loader),
			})
			require.NoError(t, err)
		})
	}

	first := assemble()
	second := assemble()

	assert.Contains(t, first, "ctxloom: warning: undefined variable: {{dedup_check_variable}}")
	assert.Empty(t, second, "identical warning must not repeat on a second assembly of the same content")
}

// TestAssembleContext_UndefinedVariableWarningNamesFragment closes the
// attribution gap left by TestAssembleContext_UndefinedVariableWarns: when
// SEVERAL fragments assemble together, "undefined variable: {{version}}"
// alone doesn't say which of them to fix. Two fragments are assembled here —
// one clean, one with an undefined variable — and the warning must name the
// OFFENDING fragment specifically, not the clean one, so an author with a
// multi-fragment profile can go straight to the right file instead of
// grepping every assembled fragment for the placeholder.
func TestAssembleContext_UndefinedVariableWarningNamesFragment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(authoredV1(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Test bundle for warning attribution
fragments:
  attribution-clean:
    content: |
      Nothing templated here.
  attribution-leaky:
    content: |
      Leaky: {{attribution_check_variable}}
`
	require.NoError(t, afero.WriteFile(fs, authoredV1(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644))
	loader := bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))

	cfg := cfgWithDirProfiles(t, fs, testBaseDir, map[string]config.Profile{
		"attribution": {Fragments: []config.FragmentRef{
			{Name: "dev#fragments/attribution-clean"},
			{Name: "dev#fragments/attribution-leaky"},
		}},
	}, config.Fixture{})

	stderr := captureStderr(t, func() {
		_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
			Profile:  "attribution",
			Pipeline: opPipe(cfg, loader),
		})
		require.NoError(t, err)
	})

	assert.Contains(t, stderr, "ctxloom: warning: undefined variable: {{attribution_check_variable}}")
	assert.Contains(t, stderr, `dev#fragments/attribution-leaky`,
		"the warning must name the fragment the undefined variable actually came from")
	assert.NotContains(t, stderr, `dev#fragments/attribution-clean`,
		"the warning must not implicate the fragment that had nothing wrong")
}

func TestAssembleContext_EmptyRequest(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// Empty request with no default profiles and no companion loadout in the
	// loader: nothing is assembled (a companion's fragments would be — see
	// TestAssembleContext_DeliversCompanionFragmentUnconditionally).
	assert.Empty(t, result.Profiles)
	assert.Empty(t, result.FragmentsLoaded)
	assert.Empty(t, result.Context)
}

// TestAssembleContext_InjectsCompanionLoadoutFragments verifies the
// always-on fragment injection that used to come from the embedded
// ltk/taskloom builtin bundles now comes from their LOADOUTS (S8): a
// companion discovered on PATH that emits `loadout --format json` has its
// fragments appended to assembled context (the loader here carries a nil
// gate — setupContextTestFS's convention — so this proves the injection
// wiring itself; gating is proven separately in
// internal/core/config's TestResolveBuiltinBundleFragments_IncludesCompanionFragments_Gated).
func TestAssembleContext_InjectsCompanionLoadoutFragments(t *testing.T) {
	defer companions.AdmitEveryDiscoveredCompanionForTesting()()
	ltkEnvelope, err := signing.EncodeLoadoutEnvelope(
		testsupport.RunLoadout("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: |\n      llm-tool-killer briefing\n"), nil, "")
	require.NoError(t, err)
	taskloomEnvelope, err := signing.EncodeLoadoutEnvelope(
		testsupport.RunLoadout("version: \"1.0.0\"\nfragments:\n  taskloom:\n    content: |\n      taskloom briefing\n"), nil, "")
	require.NoError(t, err)

	t.Run("companions present → fragments injected", func(t *testing.T) {
		_, _ = setupContextTestFS(t)
		// A fresh Config per sub-test: companion probing is memoized once per
		// Config's lifetime, so sharing one across sub-tests with different
		// fakes would silently reuse the first sub-test's cached result.
		cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

		restoreLook := companions.SetLookPathForTesting(func(bin string) (string, error) {
			return "/fake/" + bin, nil // every companion is "installed"
		})
		defer restoreLook()
		restoreProbe := companions.SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
			switch path {
			case "/fake/ltk":
				return ltkEnvelope, nil
			case "/fake/taskloom":
				return taskloomEnvelope, nil
			default:
				return nil, exec.ErrNotFound
			}
		})
		defer restoreProbe()

		cfg = published(t, cfg)
		// The injected stage reads the generation's own catalog: companion
		// fragments come from the loader the pipeline reads, never a side
		// channel, so a stage over a project-only loader would deliver none.
		result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, cfg.BundleLoader())})
		require.NoError(t, err)
		assert.Contains(t, result.Context, "llm-tool-killer briefing")
		assert.Contains(t, result.Context, "taskloom briefing")
		assert.Contains(t, result.FragmentsLoaded, "ctxloom+companion:ltk#fragments/ltk")
		assert.Contains(t, result.FragmentsLoaded, "ctxloom+companion:taskloom#fragments/taskloom")
	})

	t.Run("companion absent (loadout probe fails) → that companion's fragments skipped", func(t *testing.T) {
		_, _ = setupContextTestFS(t)
		cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

		restoreLook := companions.SetLookPathForTesting(func(bin string) (string, error) {
			if bin == "ltk" {
				return "", exec.ErrNotFound // ltk not installed
			}
			return "/fake/" + bin, nil
		})
		defer restoreLook()
		restoreProbe := companions.SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
			if path == "/fake/taskloom" {
				return taskloomEnvelope, nil
			}
			return nil, exec.ErrNotFound
		})
		defer restoreProbe()

		cfg = published(t, cfg)
		result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, cfg.BundleLoader())})
		require.NoError(t, err)
		assert.NotContains(t, result.FragmentsLoaded, "ctxloom+companion:ltk#fragments/ltk", "absent companion is skipped")
		assert.Contains(t, result.FragmentsLoaded, "ctxloom+companion:taskloom#fragments/taskloom", "present companion still injects")
	})
}

// companionIsolationFragmentRef is the stable identity of the fixture
// companion's isolation fragment (isolationCompanion), shared by every test
// in this package that must account for its unconditional delivery alongside
// loader-resolved content.
const companionIsolationFragmentRef = "ctxloom+companion:isolation#fragments/isolation-axes"

// TestAssembleContext_DeliversCompanionFragmentUnconditionally proves the
// wiring end to end: a companion loadout's fragment reaches the ASSEMBLED
// context through the actual AssembleContext path with an EMPTY request (no
// profile/fragments/tags), so the only way its text can appear is the
// unconditional companion delivery. A companion whose content never reached
// assembly would pass every other test here while delivering zero bytes to a
// real session — the silent-no-op failure mode this codebase has shipped.
func TestAssembleContext_DeliversCompanionFragmentUnconditionally(t *testing.T) {
	_, _ = setupContextTestFS(t)
	body := companionIsolationContent(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	loader := ingestLoader(t, afero.NewMemMapFs())

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)

	assert.Contains(t, result.Context, body,
		"assembled context must contain the companion fragment's exact bytes")
	assert.Contains(t, result.FragmentsLoaded, companionIsolationFragmentRef)
}

// TestAssembleContext_ExcludesCtxloomInitCommandBody is the OTHER half of the
// init-as-skill slice 3 load-bearing proof (see
// internal/lm/backends.TestLoadCommandExports_CtxloomInitAlwaysPresent for the
// "invocable" half): ctxloom's five-phase setup body
// (resources/commands/ctxloom-init.md) must NEVER be part of an ordinary
// session's always-on ASSEMBLED CONTEXT, no matter how bare the request is.
// AssembleContext only ever folds in FRAGMENTS (profile fragments, explicit
// asks, tag matches, and the unconditional builtin-BUNDLE fragments like
// isolation.yaml's isolation-axes) — it has no code path that reads
// resources/commands/ at all, so this is a structural guarantee, not a flag
// that could be flipped. This test still exercises the REAL embedded body
// (not a stand-in string) so a future refactor that accidentally wires
// commands into context assembly would be caught here rather than silently
// taxing every session forever.
func TestAssembleContext_ExcludesCtxloomInitCommandBody(t *testing.T) {
	body, err := resources.GetBuiltinCommandBody("ctxloom-init")
	require.NoError(t, err, "resources/commands/ctxloom-init.md must be embedded")
	require.Contains(t, body, "Phase 2", "sanity: this is really the five-phase body")

	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	// Deliberately bare: no profile, no fragments, no tags — the same
	// zero-ask shape TestAssembleContext_InjectsBuiltinIsolationFragment uses
	// to prove the OPPOSITE fact about the isolation fragment.
	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)

	assert.NotContains(t, result.Context, "Phase 2 — Companions",
		"ctxloom-init's setup body must never be injected into always-on assembled context (it is a COMMAND, invoked on demand — not a fragment)")
	assert.NotContains(t, result.Context, strings.TrimSpace(body),
		"the setup body's exact bytes must not appear in assembled context at all")
}

func TestAssembleContext_CombineTagsAndFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:      []string{"security"},
		Fragments: []string{"dev#fragments/go-patterns"},
		Pipeline:  opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// Should have both tag-matched and explicit fragments
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 2)
	assert.Contains(t, result.Context, "Security Rules")
	assert.Contains(t, result.Context, "Go Patterns")
}

// ========== Directory-based profile resolution tests ==========

func TestAssembleContext_ProfileFromDirectory(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, nil, config.Fixture{})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			if name == "dir-profile" {
				return &profiles.ResolvedProfile{
					Tags:      []string{"security"},
					Bundles:   []string{"dev#fragments/go-patterns"},
					Variables: map[string]string{"project_name": "FromDirProfile"},
				}, nil
			}
			return nil, errors.New("not found")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "dir-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"dir-profile"}, result.Profiles)
	// Should include fragments from both tags and direct bundles
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
}

// TestAssembleContext_ProfileResolvesThroughTheInjectedLoader pins that
// AssembleContext honours ProfileLoaderFunc rather than reaching for a loader
// of its own.
//
// This used to be TestAssembleContext_ProfileFallbackToDirectory, asserting
// that a config-defined profile WON and the directory loader was never
// consulted — it failed the test if the loader was called at all. The inline
// arm is retired, so there is no precedence left to assert: the loader is the
// only resolution path, and what is worth pinning is that the INJECTED one is
// the one used.
func TestAssembleContext_ProfileResolvesThroughTheInjectedLoader(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, nil, config.Fixture{})

	called := 0
	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			called++
			if name != "injected-profile" {
				return nil, errors.New("not found")
			}
			return &profiles.ResolvedProfile{Tags: []string{"go"}}, nil
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "injected-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"injected-profile"}, result.Profiles)
	assert.Positive(t, called,
		"the injected loader must be the one consulted; zero calls would mean AssembleContext built its own")
}

func TestAssembleContext_UnknownProfileError(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, nil, config.Fixture{})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			return nil, errors.New("not found in directory")
		},
	}

	_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "nonexistent-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile nonexistent-profile")
}

// TestAssembleContext_UnresolvableDefaultProfileDegrades verifies the
// fault-tolerance split: an unresolvable profile picked up from configured
// defaults warns and is skipped (startup must not block), while the explicit
// --profile path above stays a hard error.
func TestAssembleContext_UnresolvableDefaultProfileDegrades(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{
		AppPaths:     []string{testBaseDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"https://github.com/example/repo@profiles/missing"}}},
	})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			return nil, errors.New("profile not found")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	// The unresolvable default profile degrades to nothing.
	assert.Empty(t, result.FragmentsLoaded)
	assert.Empty(t, result.Context)
}

func TestAssembleContext_DirectoryProfileWithVariables(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, nil, config.Fixture{})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			if name == "var-profile" {
				return &profiles.ResolvedProfile{
					Bundles: []string{"dev#fragments/variable-content"},
					Variables: map[string]string{
						"project_name": "DirProject",
						"version":      "2.0.0",
					},
				}, nil
			}
			return nil, errors.New("not found")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "var-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Contains(t, result.Context, "DirProject")
	assert.Contains(t, result.Context, "2.0.0")
}

// TestAssembleContext_DirectoryProfileExcludesFragments is the regression test
// for directory-profile exclusions being silently ignored: ResolvedProfile now
// carries exclude_fragments through resolution, and the bundle-expansion seam
// in resolveProfile filters them — an excluded fragment from an inherited
// bundle must not land in the assembled context.
func TestAssembleContext_DirectoryProfileExcludesFragments(t *testing.T) {
	fs, loader := setupContextTestFS(t)
	cfg := cfgWithDirProfiles(t, fs, testBaseDir, nil, config.Fixture{})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			if name == "excluding-profile" {
				return &profiles.ResolvedProfile{
					Bundles:          []string{"dev"}, // whole bundle: all four fragments
					ExcludeFragments: []string{"go-patterns"},
				}, nil
			}
			return nil, errors.New("not found")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "excluding-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Contains(t, result.Context, "Security Rules", "non-excluded fragments still load")
	assert.NotContains(t, result.Context, "Go Patterns", "excluded fragment must not be assembled")
	assert.NotContains(t, result.FragmentsLoaded, "dev#fragments/go-patterns")
}

// TestAssembleContext_DirectoryProfileExcludesTaggedFragment verifies that
// exclusions also win over tag-matched fragments — a profile that pulls
// fragments in by tag can still name-exclude one of them ("exclusions always
// win"). Request-level tags remain unfiltered; only profile-pushed content is.
func TestAssembleContext_DirectoryProfileExcludesTaggedFragment(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := gatedFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
	})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			if name == "tagged-profile" {
				return &profiles.ResolvedProfile{
					SelectTags:       []string{"security", "go"},
					ExcludeFragments: []string{"security-rules"},
				}, nil
			}
			return nil, errors.New("not found")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "tagged-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Contains(t, result.Context, "Go Patterns", "non-excluded tag match still loads")
	assert.NotContains(t, result.Context, "Security Rules", "excluded tag match must be dropped")
}

// =============================================================================
// Bookend Sorting Tests
// =============================================================================

// TestContextConsumer_ResolvesDeliveryFromWhatIsWritten is the contract of the
// ONE place static-vs-dynamic is decided. A caller states what it is writing —
// a live session, or a materialized surface for a named engine — and never a
// mode; the mode falls out here, from whether anything behind that surface can
// pull a withheld fragment later.
func TestContextConsumer_ResolvesDeliveryFromWhatIsWritten(t *testing.T) {
	static, err := ContextConsumer{}.static()
	require.NoError(t, err)
	assert.False(t, static, "a live session can pull, so premised fragments are withheld and indexed")

	static, err = MaterializedFor("mock").static()
	require.NoError(t, err)
	assert.False(t, static, "an engine with a skills surface re-delivers withheld fragments as skills, so the context itself stays dynamic")

	static, err = MaterializedFor("mock-noskills").static()
	require.NoError(t, err)
	assert.True(t, static, "an engine with no skills surface has nothing behind a materialized file that can pull, so the fragments go into the context")

	_, err = MaterializedFor("no-such-engine").static()
	require.Error(t, err, "a surface for an engine that does not exist cannot be resolved; silently treating it as skill-less would dump every premised fragment into a file nobody asked for")

	_, err = MaterializedFor("").static()
	require.Error(t, err, "'materialized for nobody' is not a live session in disguise")
}

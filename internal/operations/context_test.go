// Context assembly tests verify the core ctxloom functionality of combining
// fragments, profiles, and variables into a single context document for
// AI consumption. These tests ensure that context is assembled correctly
// from multiple sources and that variable substitution works as expected.
package operations

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbroglie/mustache"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/signing"
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

// withProfileDefs returns a config carrying cfg's fields plus defs.
//
// A Fixture used to alias the Config's own containers, so tests injected
// profile definitions by writing straight into
// `cfg.ToFixture().Profiles.Definitions`. ToFixture now deep-copies, honouring
// the "every call yields a separately-owned value" contract its doc always
// claimed — which is exactly why that write is now a no-op and adding a
// definition to an already-built config means REBUILDING it.
func withProfileDefs(cfg *config.Config, defs map[string]config.Profile) *config.Config {
	f := cfg.ToFixture()
	if f.Profiles.Definitions == nil {
		f.Profiles.Definitions = map[string]config.Profile{}
	}
	for name, p := range defs {
		f.Profiles.Definitions[name] = p
	}
	return config.NewFixture(f)
}

// installProfileDefs is withProfileDefs for the case where the config is
// already held by the code under test (a mid-run test double that must make a
// definition appear): it rebuilds in place, re-injecting fs since a Fixture
// does not carry one.
func installProfileDefs(cfg *config.Config, fs afero.Fs, defs map[string]config.Profile) {
	rebuilt := withProfileDefs(cfg, defs)
	rebuilt.SetFS(fs)
	*cfg = *rebuilt
}

// ========== Loader-based integration tests ==========

func setupContextTestFS(t *testing.T) (afero.Fs, *bundles.Loader) {
	t.Helper()
	// Isolate HOME so nothing in the assembly path can read the developer's real
	// ~/.ctxloom/config.yaml — keeping these tests deterministic regardless of the
	// machine they run on.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()

	// Create bundles directory
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

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
	_ = afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644)

	loader := bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, bundles.WithFS(fs))
	return fs, loader
}

func TestAssembleContext_WithTags(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:     []string{"security"},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.Empty(t, result.MissingTags)
}

func TestAssembleContext_WithFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{"dev#fragments/go-patterns"},
		Pipeline:  opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// +1 for the always-on builtin isolation fragment (builtinIsolationFragmentRef).
	assert.Len(t, result.FragmentsLoaded, 2)
	assert.Contains(t, result.FragmentsLoaded, builtinIsolationFragmentRef)
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_MultipleFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{
			"dev#fragments/security-rules",
			"dev#fragments/go-patterns",
		},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// +1 for the always-on builtin isolation fragment (builtinIsolationFragmentRef).
	assert.Len(t, result.FragmentsLoaded, 3)
	assert.Contains(t, result.FragmentsLoaded, builtinIsolationFragmentRef)
	assert.Contains(t, result.Context, "Security Rules")
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_DeduplicatesFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{
			"dev#fragments/security-rules",
			"dev#fragments/security-rules", // Duplicate
		},
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// Should deduplicate. +1 for the always-on builtin isolation fragment
	// (builtinIsolationFragmentRef).
	assert.Len(t, result.FragmentsLoaded, 2)
	assert.Contains(t, result.FragmentsLoaded, builtinIsolationFragmentRef)
}

func TestAssembleContext_WithProfileFromConfig(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-dev": {
				Description: "Go developer profile",
				SelectTags:  []string{"go"},
				Fragments:   []config.FragmentRef{{Name: "dev#fragments/testing-guidelines"}},
			},
		}},
	})

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
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-dev": {
				Description: "Go developer profile",
				Tags:        []string{"go"}, // descriptive only
			},
		}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "go-dev",
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// The always-on builtin isolation fragment (builtinIsolationFragmentRef)
	// injects regardless of profile tag selection; it's the only expected entry.
	assert.Equal(t, []string{builtinIsolationFragmentRef}, result.FragmentsLoaded)
	assert.NotContains(t, result.Context, "Go Patterns")
}

// TestAssembleContext_ProfileSelectTags_SelectContent pins that `select_tags:`
// selects fragment content by tag — the role `tags:` used to play.
func TestAssembleContext_ProfileSelectTags_SelectContent(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-dev": {
				Description: "Go developer profile",
				SelectTags:  []string{"go"},
			},
		}},
	})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "go-dev",
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_ProfileLLMSurfaces(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-dev": {
				LLM:       "agy-code",
				Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
			},
			"plain": {
				Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}},
			},
		}},
	})

	withLLM, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "go-dev", Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)
	assert.Equal(t, "agy-code", withLLM.ProfileLLM)

	withoutLLM, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Profile: "plain", Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)
	assert.Empty(t, withoutLLM.ProfileLLM)
}

func TestAssembleContext_ProfileWithVariables(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"project": {
				Description: "Project profile",
				Fragments:   []config.FragmentRef{{Name: "dev#fragments/variable-content"}},
				Variables: map[string]string{
					"project_name": "MyProject",
					"version":      "1.0.0",
				},
			},
		}},
	})

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
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"project-partial": {
				Fragments: []config.FragmentRef{{Name: "dev#fragments/variable-content"}},
				// project_name is bound; version is deliberately left undefined.
				Variables: map[string]string{"project_name": "MyProject"},
			},
		}},
	})

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
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"project-full": {
				Fragments: []config.FragmentRef{{Name: "dev#fragments/variable-content"}},
				Variables: map[string]string{"project_name": "MyProject", "version": "9.9.9"},
			},
		}},
	})

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
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Test bundle with a broken template
fragments:
  broken-template:
    content: |
      {{/unopened}}
`
	require.NoError(t, afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644))
	loader := bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, bundles.WithFS(fs))

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"broken": {Fragments: []config.FragmentRef{{Name: "dev#fragments/broken-template"}}},
		}},
	})

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
// session (once per conversation turn — internal/cli/run.go, acp_cmd.go), so
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
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Test bundle for the dedup case
fragments:
  dedup-fragment:
    content: |
      Repeat check: {{dedup_check_variable}}
`
	require.NoError(t, afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644))
	loader := bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, bundles.WithFS(fs))

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"project-dedup": {Fragments: []config.FragmentRef{{Name: "dev#fragments/dedup-fragment"}}},
		}},
	})

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
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))

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
	require.NoError(t, afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev.yaml", []byte(bundleContent), 0644))
	loader := bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, bundles.WithFS(fs))

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"attribution": {Fragments: []config.FragmentRef{
				{Name: "dev#fragments/attribution-clean"},
				{Name: "dev#fragments/attribution-leaky"},
			}},
		}},
	})

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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Pipeline: opPipe(cfg, loader),
	})

	require.NoError(t, err)
	// Empty request with no default profiles returns no PROFILE-sourced
	// content, but the always-on builtin isolation fragment still injects
	// unconditionally (see TestAssembleContext_InjectsBuiltinIsolationFragment).
	assert.Empty(t, result.Profiles)
	assert.Equal(t, []string{builtinIsolationFragmentRef}, result.FragmentsLoaded)
	assert.NotEmpty(t, result.Context)
}

// TestAssembleContext_InjectsCompanionLoadoutFragments verifies the
// always-on fragment injection that used to come from the embedded
// ltk/taskloom builtin bundles now comes from their LOADOUTS (S8): a
// companion discovered on PATH that emits `loadout --format json` has its
// fragments appended to assembled context (the loader here carries a nil
// gate — setupContextTestFS's convention — so this proves the injection
// wiring itself; gating is proven separately in
// internal/config's TestResolveBuiltinBundleFragments_IncludesCompanionFragments_Gated).
func TestAssembleContext_InjectsCompanionLoadoutFragments(t *testing.T) {
	ltkEnvelope, err := signing.EncodeLoadoutEnvelope(
		[]byte("version: \"1.0.0\"\nfragments:\n  ltk:\n    content: |\n      llm-tool-killer briefing\n"), nil, "")
	require.NoError(t, err)
	taskloomEnvelope, err := signing.EncodeLoadoutEnvelope(
		[]byte("version: \"1.0.0\"\nfragments:\n  taskloom:\n    content: |\n      taskloom briefing\n"), nil, "")
	require.NoError(t, err)

	t.Run("companions present → fragments injected", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		// A fresh Config per sub-test: companion probing is memoized once per
		// Config's lifetime, so sharing one across sub-tests with different
		// fakes would silently reuse the first sub-test's cached result.
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

		restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
			return "/fake/" + bin, nil // every companion is "installed"
		})
		defer restoreLook()
		restoreProbe := config.SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
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

		result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, loader)})
		require.NoError(t, err)
		assert.Contains(t, result.Context, "llm-tool-killer briefing")
		assert.Contains(t, result.Context, "taskloom briefing")
		assert.Contains(t, result.FragmentsLoaded, "ctxloom:companion@ltk#fragments/ltk")
		assert.Contains(t, result.FragmentsLoaded, "ctxloom:companion@taskloom#fragments/taskloom")
	})

	t.Run("companion absent (loadout probe fails) → that companion's fragments skipped", func(t *testing.T) {
		_, loader := setupContextTestFS(t)
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

		restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
			if bin == "ltk" {
				return "", exec.ErrNotFound // ltk not installed
			}
			return "/fake/" + bin, nil
		})
		defer restoreLook()
		restoreProbe := config.SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
			if path == "/fake/taskloom" {
				return taskloomEnvelope, nil
			}
			return nil, exec.ErrNotFound
		})
		defer restoreProbe()

		result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, loader)})
		require.NoError(t, err)
		assert.NotContains(t, result.FragmentsLoaded, "ctxloom:companion@ltk#fragments/ltk", "absent companion is skipped")
		assert.Contains(t, result.FragmentsLoaded, "ctxloom:companion@taskloom#fragments/taskloom", "present companion still injects")
	})
}

// builtinIsolationFragmentRef is the stable identity of the always-on
// isolation fragment (resources/builtin_bundles/isolation.yaml), shared by
// every test in this package that must account for its unconditional
// injection alongside loader-resolved content.
const builtinIsolationFragmentRef = "builtin:isolation#fragments/isolation-axes"

// TestAssembleContext_InjectsBuiltinIsolationFragment proves ctxloom's first
// genuinely-embedded builtin bundle (resources/builtin_bundles/isolation.yaml)
// actually reaches assembled context, unconditionally — not merely that the
// mechanism exists (README.md's claim; this test is what makes that claim
// verified rather than trusted). It reads the REAL
// resources.GetBuiltinBundle("isolation") — embedded in the binary via
// resources/embed.go's `//go:embed all:builtin_bundles` — through the actual
// AssembleContext path, with an EMPTY request (no profile/fragments/tags), so
// the only way this fragment's text can appear is the unconditional builtin
// append (appendBuiltinFragments in context.go). A builtin that ships but is
// never wired to inject would pass every other test in this file while still
// delivering zero bytes to a real session — exactly the silent-no-op failure
// mode this codebase has shipped before.
func TestAssembleContext_InjectsBuiltinIsolationFragment(t *testing.T) {
	raw, err := resources.GetBuiltinBundle("isolation")
	require.NoError(t, err, "resources/builtin_bundles/isolation.yaml must be embedded")

	var b bundles.Bundle
	require.NoError(t, yaml.Unmarshal(raw, &b))
	frag, ok := b.Fragments["isolation-axes"]
	require.True(t, ok, "isolation.yaml must ship the isolation-axes fragment")
	require.NotEmpty(t, frag.Content)

	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{Pipeline: opPipe(cfg, loader)})
	require.NoError(t, err)

	assert.Contains(t, result.Context, strings.TrimSpace(frag.Content),
		"assembled context must contain the real embedded isolation fragment's exact bytes")
	assert.Contains(t, result.FragmentsLoaded, builtinIsolationFragmentRef)

	// Ground truth: the specific facts the fragment must carry, pinned
	// directly (not just "non-empty content made it through") so a future
	// edit that keeps SOME content but drops the substance still fails.
	for _, want := range []string{"runtime", "workspace", "host", "container", "worktree"} {
		assert.Contains(t, result.Context, want)
	}
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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

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

func TestSubstituteVariables_Basic(t *testing.T) {
	content := "Hello {{name}}, welcome to {{place}}!"
	vars := map[string]string{
		"name":  "World",
		"place": "ctxloom",
	}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Equal(t, "Hello World, welcome to ctxloom!", result)
}

func TestSubstituteVariables_MissingVariable(t *testing.T) {
	content := "Hello {{name}}!"
	vars := map[string]string{} // No vars defined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "undefined variable")
}

func TestSubstituteVariables_EmptyContent(t *testing.T) {
	content := ""
	vars := map[string]string{"name": "test"}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Empty(t, result)
}

func TestSubstituteVariables_NoVariables(t *testing.T) {
	content := "No variables here"
	vars := map[string]string{}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Equal(t, "No variables here", result)
}

func TestSubstituteVariables_InvalidTemplate(t *testing.T) {
	// Unclosed section tag causes parse error
	content := "{{#section}}content without closing"
	vars := map[string]string{}

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	result := substituteVariables(content, vars, warnFunc)
	// Should return original content on parse error
	assert.Equal(t, content, result)
	// Should have logged a warning
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "failed to parse")
}

func TestSubstituteVariables_SectionTag(t *testing.T) {
	// Test section tags (type 3) - these also reference variables
	content := "{{#show}}visible{{/show}}"
	vars := map[string]string{} // No vars - "show" is undefined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about undefined "show" variable
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "undefined variable")
	assert.Contains(t, warnings[0], "show")
}

func TestSubstituteVariables_RawVariable(t *testing.T) {
	// Test raw variable tags (type 2) - {{{var}}} or {{&var}}
	content := "Raw: {{{name}}}"
	vars := map[string]string{"name": "<b>bold</b>"}

	result := substituteVariables(content, vars, func(s string) {})
	// Raw variables should not escape HTML
	assert.Contains(t, result, "<b>bold</b>")
}

func TestSubstituteVariables_NestedSectionWithVariables(t *testing.T) {
	// Test nested section tags with child variables - exercises recursive checkTags
	// The section "outer" is undefined, which triggers the recursive check
	content := "{{#outer}}Hello {{inner_name}}!{{/outer}}"
	vars := map[string]string{} // No vars defined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about at least the section variable
	require.GreaterOrEqual(t, len(warnings), 1, "should warn about undefined variables")
	assert.Contains(t, warnings[0], "outer")
}

func TestSubstituteVariables_InvertedSection(t *testing.T) {
	// Test inverted section tags (type 4) - {{^var}}content{{/var}}
	content := "{{^missing}}default content{{/missing}}"
	vars := map[string]string{} // "missing" is undefined

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about undefined "missing" variable
	assert.GreaterOrEqual(t, len(warnings), 1)
	assert.Contains(t, warnings[0], "missing")
}

func TestSubstituteVariables_DeeplyNestedSections(t *testing.T) {
	// Test deeply nested sections with variables at multiple levels
	// Exercises the recursive checkTags path for nested sections
	content := "{{#level1}}A{{#level2}}B{{deep_var}}{{/level2}}{{/level1}}"
	vars := map[string]string{}

	var warnings []string
	warnFunc := func(s string) {
		warnings = append(warnings, s)
	}

	substituteVariables(content, vars, warnFunc)
	// Should warn about at least the outermost undefined section
	require.GreaterOrEqual(t, len(warnings), 1, "should warn about undefined variables")
	assert.Contains(t, warnings[0], "level1")
}

// TestSubstituteVariables_UndefinedPlainVariableRendersVerbatim is the
// payload-asserting proof of the fixed contract: the
// real-world bug found via a live ACP client (Nori) was that a fragment
// documenting another {{...}}-flavored syntax (here, justfile's `{{TOP}}` /
// `{{ARGS}}` / `{{justfile_directory()}}`) collided with fragment
// templating — every such tag looks like a ctxloom variable reference, and
// the old contract ("undefined variable renders as empty") silently
// stripped the literal documentation text on its way to the engine. The
// DECIDED fix: an unresolved plain variable tag now renders as the literal
// text the author wrote, instead of vanishing. This asserts the actual
// rendered BYTES (not merely "no error" and not merely "a warning fired")
// so a regression that starts blanking the text again — even with the
// warning intact — fails this test.
//
// This was formerly TestSubstituteVariables_LiteralJustSyntaxIsCorrupted,
// which characterized the OLD (corrupting) behavior as by-design; that
// characterization is exactly what this task changes.
//
// The Set Delimiter escape (TestSubstituteVariables_SetDelimiterEscapesLiteralMustache
// below) remains the more surgical tool — it silences the warning entirely —
// but verbatim rendering is now the default safety net for authors who
// didn't use it.
func TestSubstituteVariables_UndefinedPlainVariableRendersVerbatim(t *testing.T) {
	content := "Run `just build`:\n```just\nbuild:\n    go build {{ARGS}}\n```\nSee {{TOP}} and {{justfile_directory()}}."
	vars := map[string]string{} // no ctxloom variables bound; this is unescaped prose

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	// The fix: literal justfile syntax survives byte-for-byte in the
	// assembled output instead of vanishing.
	assert.Contains(t, result, "{{ARGS}}", "undefined plain variable must render verbatim, not vanish")
	assert.Contains(t, result, "{{TOP}}", "undefined plain variable must render verbatim, not vanish")
	assert.Contains(t, result, "{{justfile_directory()}}", "undefined plain variable must render verbatim, not vanish")
	assert.Equal(t,
		"Run `just build`:\n```just\nbuild:\n    go build {{ARGS}}\n```\nSee {{TOP}} and {{justfile_directory()}}.",
		result,
		"rendered output must be byte-identical to the input when nothing resolves")

	// The warnings are STILL real signal, not silenced by the verbatim fix:
	// they name exactly the tags that stayed unresolved, so the author can
	// still find and fix (or intentionally escape) them.
	assert.GreaterOrEqual(t, len(warnings), 3)
	joined := warnings[0] + warnings[1] + warnings[2]
	assert.Contains(t, joined, "ARGS")
	assert.Contains(t, joined, "TOP")
	assert.Contains(t, joined, "justfile_directory()")
}

// TestSubstituteVariables_SectionIdiomUnchangedByVerbatimFix PINS the
// section/inverted-section presence-toggle idiom against the verbatim-render
// fix above: an undefined SECTION name must still omit its body (falsy, key
// absent from the data map, exactly as before) and an undefined INVERTED
// SECTION name must still emit its body (falsy triggers the inverse), even
// though the fix now seeds literal text into the data map for undefined
// PLAIN variable tags. This is the documented, sanctioned idiom
// (`{{#DEBUG}}...{{/DEBUG}}`, `{{^DEBUG}}...{{/DEBUG}}`) used across existing
// profiles; seeding a literal (truthy, non-empty) string for a section name
// would silently flip every optional block's polarity. If this regresses,
// every profile relying on the idiom breaks silently.
func TestSubstituteVariables_SectionIdiomUnchangedByVerbatimFix(t *testing.T) {
	content := "{{#DEBUG}}debug on{{/DEBUG}}{{^DEBUG}}debug off{{/DEBUG}}"
	vars := map[string]string{} // DEBUG undefined

	result := substituteVariables(content, vars, func(s string) {})

	// Section body omitted (undefined section is falsy, same as before).
	assert.NotContains(t, result, "debug on")
	// Inverted-section body emitted (undefined section is falsy, so the
	// inverse still fires, same as before).
	assert.Contains(t, result, "debug off")
	// Crucially: the section name itself must NOT leak into the output as
	// literal "{{DEBUG}}" text either — that would mean the seeding
	// mistakenly treated the section tag as a plain variable.
	assert.NotContains(t, result, "{{DEBUG}}")
	assert.NotContains(t, result, "{{#DEBUG}}")
	assert.NotContains(t, result, "{{^DEBUG}}")
}

// TestSubstituteVariables_NameUsedAsBothSectionAndPlainVariableStaysFalsy
// documents the deliberate, defensive edge-case choice: if one name is used
// BOTH as a section/inverted-section tag AND as a plain variable tag
// somewhere in the same fragment, and it's undefined, the verbatim-render
// seeding is skipped for that name entirely — protecting the section idiom
// takes priority over verbatim rendering for the plain-variable use. This is
// a rare collision (typically a fragment would only use a name one way), but
// getting it wrong would risk flipping a section's polarity, which is the
// one behavior this task must never change.
func TestSubstituteVariables_NameUsedAsBothSectionAndPlainVariableStaysFalsy(t *testing.T) {
	content := "{{#DEBUG}}on{{/DEBUG}} plain:{{DEBUG}}"
	vars := map[string]string{} // DEBUG undefined

	result := substituteVariables(content, vars, func(s string) {})

	// The section is still falsy/omitted (idiom protected).
	assert.NotContains(t, result, "on")
	// The plain-variable occurrence is NOT seeded literal in this collision
	// case (it renders empty, same as the historical default) — a
	// conservative tradeoff documented here so it can't silently change.
	assert.NotContains(t, result, "plain:{{DEBUG}}")
	assert.Contains(t, result, "plain:")
}

// TestSubstituteVariables_SetDelimiterEscapesLiteralMustache is the
// payload-asserting proof that fragment authors already have a supported,
// zero-code-change way to write literal `{{...}}` syntax: Mustache's
// standard Set Delimiter tag (`{{=<% %>=}}` ... `<%={{ }}=%>`), which the
// underlying cbroglie/mustache library implements today. Content inside the
// escaped block is treated as plain text — never extracted as a Tag by
// checkTags, never warned about, and reaches the engine byte-for-byte —
// while a real ctxloom variable outside the escaped block still substitutes
// normally. This is the fix landed for this bug: documentation
// (docs/guides/templating.md) pointing fragment authors at working,
// pre-existing library behavior, proven here so a future change to
// checkTags/substituteVariables or a mustache library swap can't silently
// take this escape hatch away again.
func TestSubstituteVariables_SetDelimiterEscapesLiteralMustache(t *testing.T) {
	content := "{{=<% %>=}}\n" +
		"Use `{{ARGS}}` to forward recipe arguments; `{{justfile_directory()}}`\n" +
		"is scoped to the local file, not a composing parent. See {{TOP}}.\n" +
		"<%={{ }}=%>\n" +
		"Real variable: {{name}}"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	// The literal justfile syntax survives byte-for-byte inside the escaped block.
	assert.Contains(t, result, "Use `{{ARGS}}` to forward recipe arguments; `{{justfile_directory()}}`")
	assert.Contains(t, result, "is scoped to the local file, not a composing parent. See {{TOP}}.")
	// The real ctxloom variable outside the escape still substitutes.
	assert.Contains(t, result, "Real variable: World")
	// No spurious warnings: the escaped tags were never treated as undefined
	// ctxloom variables in the first place.
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_RealVariableOutsideEscapedBlockStillSubstitutes
// isolates the specific claim docs/guides/templating.md makes about the Set
// Delimiter escape ("Real ctxloom variables outside the escaped block still
// substitute normally"): a real variable placed both before and after an
// escaped block substitutes in both places, while the escaped block's own
// content never does, in one fragment.
func TestSubstituteVariables_RealVariableOutsideEscapedBlockStillSubstitutes(t *testing.T) {
	content := "Before: {{name}}\n" +
		"{{=<% %>=}}\n" +
		"literal {{name}} stays literal\n" +
		"<%={{ }}=%>\n" +
		"After: {{name}}"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	assert.Contains(t, result, "Before: World")
	assert.Contains(t, result, "literal {{name}} stays literal")
	assert.Contains(t, result, "After: World")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_MultipleSetDelimiterBlocksInOneFragment proves the
// escape is not a one-shot toggle: a fragment can open and close the Set
// Delimiter tag more than once, with real substitution resuming correctly in
// between and after every escaped block.
func TestSubstituteVariables_MultipleSetDelimiterBlocksInOneFragment(t *testing.T) {
	content := "{{=<% %>=}}\n" +
		"first literal {{ARGS}}\n" +
		"<%={{ }}=%>\n" +
		"middle real: {{name}}\n" +
		"{{=<% %>=}}\n" +
		"second literal {{TOP}}\n" +
		"<%={{ }}=%>\n" +
		"end"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	assert.Contains(t, result, "first literal {{ARGS}}")
	assert.Contains(t, result, "middle real: World")
	assert.Contains(t, result, "second literal {{TOP}}")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_SetDelimiterEscapesEvenRealVariableNames proves the
// escape is a lexical switch, not a name-based one: a literal tag inside an
// escaped block that happens to spell a genuinely bound ctxloom variable name
// is NOT substituted (it is never tokenized as a tag at all), while the exact
// same name outside the block substitutes normally. This matters because a
// fragment documenting another {{...}}-flavored language can easily collide
// on a short, common name (ARGS, NAME, ID); the escape must win regardless.
func TestSubstituteVariables_SetDelimiterEscapesEvenRealVariableNames(t *testing.T) {
	content := "{{=<% %>=}}\n" +
		"literal {{ARGS}}\n" +
		"<%={{ }}=%>\n" +
		"real: {{ARGS}}"
	vars := map[string]string{"ARGS": "real-value"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	assert.Contains(t, result, "literal {{ARGS}}")
	assert.Contains(t, result, "real: real-value")
	assert.Empty(t, warnings)
}

// TestSubstituteVariables_UnclosedSetDelimiterSwallowsRestOfFragment
// characterizes a genuinely surprising failure mode of a MALFORMED escape: a
// `{{=<% %>=}}` opening tag with no matching `<%={{ }}=%>` closing tag does
// not error, and does not merely leave the rest of the raw text untouched —
// it silently disables ALL further variable substitution for the remainder
// of the fragment, including a real, bound ctxloom variable that appears
// after the unclosed tag. That variable is left as literal `{{name}}` text
// in the rendered output (not substituted, not warned about, not blanked —
// just silently skipped), while content BEFORE the unclosed tag still
// substitutes normally. A fragment author who forgets the closing tag gets
// no signal that every variable reference downstream of the typo stopped
// working. This is a foot-gun in the standard library behavior, not
// something ctxloom's code introduces — documented here so a future change
// cannot silently make it worse (e.g. by starting to blank that trailing
// content instead of leaving it literal) without this test forcing a look.
func TestSubstituteVariables_UnclosedSetDelimiterSwallowsRestOfFragment(t *testing.T) {
	content := "Before: {{name}}\n" +
		"{{=<% %>=}}\n" +
		"literal {{ARGS}}\n" +
		"After (never re-escaped): {{name}}"
	vars := map[string]string{"name": "World"}

	var warnings []string
	result := substituteVariables(content, vars, func(s string) { warnings = append(warnings, s) })

	// Content before the malformed tag is unaffected.
	assert.Contains(t, result, "Before: World")
	// Content after the unclosed tag — including a real, bound variable
	// reference — is left as literal, unsubstituted text: the missing close
	// tag never handed control back to normal Mustache parsing.
	assert.Contains(t, result, "literal {{ARGS}}")
	assert.Contains(t, result, "After (never re-escaped): {{name}}")
	assert.NotContains(t, result, "After (never re-escaped): World")
	// No warning fires for the swallowed {{name}} reference: this is not
	// treated as an undefined variable, so checkTags never sees a tag to
	// warn about in the first place — the failure is invisible by default.
	assert.Empty(t, warnings)
}

// TestShippedFragments_NoUnescapedForeignMustache is a guard against the
// corruption class characterized by
// TestSubstituteVariables_LiteralJustSyntaxIsCorrupted recurring in a real,
// shipped fragment: it scans every fragment this repo actually ships and
// fails if any fragment's content contains a `{{...}}`-shaped tag, outside a
// Set Delimiter escape block, that does not resolve to a known ctxloom
// variable.
//
// Coverage — this checks exactly, and only, the fragment content this repo
// compiles in or ships alongside its own binaries:
//   - every embedded builtin bundle (resources/builtin_bundles/*.yaml, via
//     resources.ListBuiltinBundles/GetBuiltinBundle)
//   - the two standalone companion loadouts (cmd/taskloom/loadout.yaml,
//     cmd/ltk/loadout.yaml), which ship the same bundle document shape
//
// It deliberately does NOT cover, and cannot cover from inside this repo:
//   - remote-pulled bundle content (ctxloom-default or any other remote a
//     project's profile references) — those bytes live outside this repo
//     and are not available at test time
//   - a consuming project's own .ctxloom/ fragments and profiles
//   - `commands:` content, which never passes through substituteVariables
//     (it uses the separate, positional-arg-rewrite export mechanism
//     documented in docs/guides/templating.md's "Commands are different"
//     note)
//
// shippedFragmentKnownVariables is the allowlist of ctxloom variable names a
// shipped fragment is genuinely allowed to reference. It is empty today: no
// fragment this repo ships declares or depends on a profile variable. If a
// future shipped fragment legitimately needs one, add its name here
// deliberately — do not delete or loosen this check to make it pass.
func TestShippedFragments_NoUnescapedForeignMustache(t *testing.T) {
	shippedFragmentKnownVariables := map[string]bool{}

	type fragmentSource struct {
		label   string
		content string
	}
	var sources []fragmentSource

	collect := func(label string, raw []byte) {
		bundle, err := bundles.ParseBundle(raw)
		require.NoError(t, err, "%s: must parse as a bundle document", label)
		for name, frag := range bundle.Fragments {
			sources = append(sources, fragmentSource{
				label:   label + "#fragments/" + name,
				content: frag.Content,
			})
		}
	}

	builtinNames, err := resources.ListBuiltinBundles()
	require.NoError(t, err)
	require.NotEmpty(t, builtinNames, "no builtin bundles found — this guard would silently check nothing")
	for _, name := range builtinNames {
		raw, err := resources.GetBuiltinBundle(name)
		require.NoError(t, err)
		collect("builtin_bundles/"+name, raw)
	}

	for _, rel := range []string{
		filepath.Join("..", "..", "cmd", "taskloom", "loadout.yaml"),
		filepath.Join("..", "..", "cmd", "ltk", "loadout.yaml"),
	} {
		path := filepath.Join(thisDir(), rel)
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "companion loadout must exist: %s", path)
		collect(rel, raw)
	}

	require.NotEmpty(t, sources, "no fragment sources discovered — this guard would silently check nothing")

	knownVars := make(map[string]string, len(shippedFragmentKnownVariables))
	for name := range shippedFragmentKnownVariables {
		knownVars[name] = "x"
	}

	for _, src := range sources {
		src := src
		t.Run(src.label, func(t *testing.T) {
			tmpl, err := mustache.ParseString(src.content)
			require.NoError(t, err, "fragment content failed to parse as a mustache template")

			var undefined []string
			checkTags(tmpl.Tags(), knownVars, collections.NewSet[string](), func(msg string) {
				undefined = append(undefined, msg)
			})

			assert.Empty(t, undefined,
				"fragment %q references an un-escaped {{...}} tag that is not a known ctxloom "+
					"variable — if this is literal prose for another {{}}-flavored syntax, wrap it in "+
					"Mustache's Set Delimiter escape ({{=<% %>=}} ... <%={{ }}=%>); if it is a genuine "+
					"new ctxloom variable this fragment now depends on, add it to "+
					"shippedFragmentKnownVariables deliberately", src.label)
		})
	}
}

// ========== Directory-based profile resolution tests ==========

func TestAssembleContext_ProfileFromDirectory(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}}, // Empty config profiles
	})

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

func TestAssembleContext_ProfileFallbackToDirectory(t *testing.T) {
	// Test that config-based resolution is tried first
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"config-profile": {
				Description: "Profile from config",
				Tags:        []string{"go"},
			},
		}},
	})

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			// This should NOT be called for config-profile
			t.Errorf("ProfileLoader.ResolveProfile called unexpectedly for %s", name)
			return nil, errors.New("unexpected call")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile:  "config-profile",
		Pipeline: opPipe(cfg, loader),
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"config-profile"}, result.Profiles)
}

func TestAssembleContext_UnknownProfileError(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}}, // Empty
	})

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
	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{testBaseDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"https://github.com/example/repo@profiles/missing"}}},
		Profiles:     config.ProfilesConfig{Definitions: map[string]config.Profile{}},
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
	// The unresolvable default profile degrades to nothing, but the always-on
	// builtin isolation fragment still injects unconditionally.
	assert.Equal(t, []string{builtinIsolationFragmentRef}, result.FragmentsLoaded)
	assert.NotEmpty(t, result.Context)
}

func TestAssembleContext_DirectoryProfileWithVariables(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}}, // Empty
	})

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
	_, loader := setupContextTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}}, // Empty config profiles
	})

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
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
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

func TestSortFragmentsByPriority_BookendStrategy(t *testing.T) {
	tests := []struct {
		name     string
		input    []config.FragmentRef
		expected []string
	}{
		{
			name:     "empty",
			input:    []config.FragmentRef{},
			expected: nil,
		},
		{
			name:     "single fragment",
			input:    []config.FragmentRef{{Name: "a", Priority: 5}},
			expected: []string{"a"},
		},
		{
			name:     "two fragments",
			input:    []config.FragmentRef{{Name: "low", Priority: 1}, {Name: "high", Priority: 10}},
			expected: []string{"high", "low"}, // Sorted by priority desc
		},
		{
			name: "three fragments - bookend",
			input: []config.FragmentRef{
				{Name: "low", Priority: 1},
				{Name: "med", Priority: 5},
				{Name: "high", Priority: 10},
			},
			// Bookend: [highest, middle..., second-highest]
			// high(10) at start, med(5) at end, low(1) in middle
			expected: []string{"high", "low", "med"},
		},
		{
			name: "five fragments - full bookend",
			input: []config.FragmentRef{
				{Name: "e", Priority: 1},
				{Name: "d", Priority: 2},
				{Name: "c", Priority: 3},
				{Name: "b", Priority: 4},
				{Name: "a", Priority: 5},
			},
			// Sorted desc: a(5), b(4), c(3), d(2), e(1)
			// Bookend: a at start, b at end, c,d,e fill middle
			expected: []string{"a", "c", "d", "e", "b"},
		},
		{
			name: "same priority - stable order",
			input: []config.FragmentRef{
				{Name: "first", Priority: 0},
				{Name: "second", Priority: 0},
				{Name: "third", Priority: 0},
			},
			// All same priority, bookend still applies
			expected: []string{"first", "third", "second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, r := range sortFragmentsByPriority(tt.input) {
				got = append(got, r.Name)
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDedupeFragmentRefs_KeepsHighestPriority(t *testing.T) {
	input := []config.FragmentRef{
		{Name: "a", Priority: 5},
		{Name: "b", Priority: 3},
		{Name: "a", Priority: 10}, // Higher priority for 'a'
		{Name: "c", Priority: 1},
		{Name: "b", Priority: 2}, // Lower priority for 'b', should be ignored
	}

	result := dedupeFragmentRefs(input)

	// Should have 3 unique fragments
	assert.Len(t, result, 3)

	// Find each and check priorities
	priorities := make(map[string]int)
	for _, f := range result {
		priorities[f.Name] = f.Priority
	}

	assert.Equal(t, 10, priorities["a"], "should keep higher priority for 'a'")
	assert.Equal(t, 3, priorities["b"], "should keep first (higher) priority for 'b'")
	assert.Equal(t, 1, priorities["c"])
}

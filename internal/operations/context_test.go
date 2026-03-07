// Context assembly tests verify the core SCM functionality of combining
// fragments, profiles, and variables into a single context document for
// AI consumption. These tests ensure that context is assembled correctly
// from multiple sources and that variable substitution works as expected.
package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/profiles"
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

// ========== Loader-based integration tests ==========

func setupContextTestFS(t *testing.T) (afero.Fs, *bundles.Loader) {
	t.Helper()
	fs := afero.NewMemMapFs()

	// Create bundles directory
	_ = fs.MkdirAll("/project/.scm/bundles", 0755)

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
	_ = afero.WriteFile(fs, "/project/.scm/bundles/dev.yaml", []byte(bundleContent), 0644)

	loader := bundles.NewLoader([]string{"/project/.scm/bundles"}, false, bundles.WithFS(fs))
	return fs, loader
}

func TestAssembleContext_WithTags(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:   []string{"security"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
	assert.Contains(t, result.Context, "Security Rules")
}

func TestAssembleContext_WithFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{"dev#fragments/go-patterns"},
		Loader:    loader,
	})

	require.NoError(t, err)
	assert.Len(t, result.FragmentsLoaded, 1)
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_MultipleFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{
			"dev#fragments/security-rules",
			"dev#fragments/go-patterns",
		},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Len(t, result.FragmentsLoaded, 2)
	assert.Contains(t, result.Context, "Security Rules")
	assert.Contains(t, result.Context, "Go Patterns")
}

func TestAssembleContext_DeduplicatesFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{
			"dev#fragments/security-rules",
			"dev#fragments/security-rules", // Duplicate
		},
		Loader: loader,
	})

	require.NoError(t, err)
	// Should deduplicate
	assert.Len(t, result.FragmentsLoaded, 1)
}

func TestAssembleContext_WithProfileFromConfig(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"go-dev": {
				Description: "Go developer profile",
				Tags:        []string{"go"},
				Fragments:   []string{"dev#fragments/testing-guidelines"},
			},
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile: "go-dev",
		Loader:  loader,
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"go-dev"}, result.Profiles)
	// Should include fragments from tags AND direct fragments
	assert.GreaterOrEqual(t, len(result.FragmentsLoaded), 1)
}

func TestAssembleContext_ProfileWithVariables(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"project": {
				Description: "Project profile",
				Fragments:   []string{"dev#fragments/variable-content"},
				Variables: map[string]string{
					"project_name": "MyProject",
					"version":      "1.0.0",
				},
			},
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile: "project",
		Loader:  loader,
	})

	require.NoError(t, err)
	// Variables should be substituted
	assert.Contains(t, result.Context, "MyProject")
	assert.Contains(t, result.Context, "1.0.0")
}

func TestAssembleContext_EmptyRequest(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Loader: loader,
	})

	require.NoError(t, err)
	// Empty request with no default profiles returns empty context
	assert.Empty(t, result.Profiles)
	assert.Empty(t, result.FragmentsLoaded)
	assert.Empty(t, result.Context)
}

func TestAssembleContext_CombineTagsAndFragments(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Tags:      []string{"security"},
		Fragments: []string{"dev#fragments/go-patterns"},
		Loader:    loader,
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
		"place": "SCM",
	}

	result := substituteVariables(content, vars, func(s string) {})
	assert.Equal(t, "Hello World, welcome to SCM!", result)
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

// ========== Directory-based profile resolution tests ==========

func TestAssembleContext_ProfileFromDirectory(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{}, // Empty config profiles
	}

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
		Profile: "dir-profile",
		Loader:  loader,
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
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"config-profile": {
				Description: "Profile from config",
				Tags:        []string{"go"},
			},
		},
	}

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			// This should NOT be called for config-profile
			t.Errorf("ProfileLoader.ResolveProfile called unexpectedly for %s", name)
			return nil, errors.New("unexpected call")
		},
	}

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile: "config-profile",
		Loader:  loader,
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"config-profile"}, result.Profiles)
}

func TestAssembleContext_UnknownProfileError(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{}, // Empty
	}

	mockLoader := &mockProfileLoader{
		resolveFunc: func(name string, visited map[string]bool) (*profiles.ResolvedProfile, error) {
			return nil, errors.New("not found in directory")
		},
	}

	_, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile: "nonexistent-profile",
		Loader:  loader,
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown profile")
}

func TestAssembleContext_DirectoryProfileWithVariables(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{}, // Empty
	}

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
		Profile: "var-profile",
		Loader:  loader,
		ProfileLoaderFunc: func() ProfileLoader {
			return mockLoader
		},
	})

	require.NoError(t, err)
	assert.Contains(t, result.Context, "DirProject")
	assert.Contains(t, result.Context, "2.0.0")
}

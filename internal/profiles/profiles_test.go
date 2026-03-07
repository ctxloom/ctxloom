// Package profiles tests verify profile parsing, resolution, and inheritance.
//
// Profiles are named collections of bundles, tags, and variables that define
// what context gets loaded for an AI session. They support inheritance through
// the `parents` field, enabling composition and reuse.
//
// # Content Reference Format
//
// SCM uses a flexible reference format to identify bundles and their contents:
//
//	"bundle-name"                       → Local bundle
//	"remote/bundle-name"                → Remote bundle via short name
//	"https://github.com/user/repo"      → Remote bundle via URL
//	"bundle#fragments/name"             → Specific fragment within bundle
//	"bundle#prompts/name"               → Specific prompt within bundle
//	"bundle#mcp/name"                   → Specific MCP server within bundle
//
// # Profile Inheritance
//
// Profiles can inherit from parents using the `parents` field:
//   - Bundles are accumulated (child adds to parent's bundles)
//   - Tags are accumulated (child adds to parent's tags)
//   - Variables are merged (child overrides parent values)
//   - Circular references are detected and rejected
//
// # Test Injection Patterns
//
// Tests use two approaches for filesystem injection:
//   - Real filesystem with t.TempDir() for integration tests
//   - afero.MemMapFs with WithFS() option for unit tests
package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ContentRef Parsing Tests
// =============================================================================
//
// These tests verify that content references are correctly parsed into their
// component parts. The parser must handle local bundles, remote shortnames,
// full URLs (HTTPS and SSH), and item specifiers (#fragments/name, etc.)

// TestParseContentRef verifies parsing of various reference formats.
func TestParseContentRef(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ContentRef
	}{
		{
			name:  "simple bundle",
			input: "go-development",
			expected: ContentRef{
				Raw:    "go-development",
				Bundle: "go-development",
			},
		},
		{
			name:  "bundle with fragment",
			input: "go-development#fragments/testing",
			expected: ContentRef{
				Raw:      "go-development#fragments/testing",
				Bundle:   "go-development",
				ItemType: "fragments",
				ItemName: "testing",
			},
		},
		{
			name:  "bundle with prompt",
			input: "go-development#prompts/review",
			expected: ContentRef{
				Raw:      "go-development#prompts/review",
				Bundle:   "go-development",
				ItemType: "prompts",
				ItemName: "review",
			},
		},
		{
			name:  "bundle with mcp",
			input: "go-development#mcp/tasks",
			expected: ContentRef{
				Raw:      "go-development#mcp/tasks",
				Bundle:   "go-development",
				ItemType: "mcp",
				ItemName: "tasks",
			},
		},
		{
			name:  "remote/bundle",
			input: "github/go-development",
			expected: ContentRef{
				Raw:    "github/go-development",
				Remote: "github",
				Bundle: "go-development",
			},
		},
		{
			name:  "remote/bundle with fragment",
			input: "github/go-development#fragments/testing",
			expected: ContentRef{
				Raw:      "github/go-development#fragments/testing",
				Remote:   "github",
				Bundle:   "go-development",
				ItemType: "fragments",
				ItemName: "testing",
			},
		},
		{
			name:  "https URL",
			input: "https://github.com/user/scm-github",
			expected: ContentRef{
				Raw:    "https://github.com/user/scm-github",
				Remote: "https://github.com/user/scm-github",
				Bundle: "scm-github",
				IsURL:  true,
			},
		},
		{
			name:  "https URL with .git",
			input: "https://github.com/user/scm-github.git",
			expected: ContentRef{
				Raw:    "https://github.com/user/scm-github.git",
				Remote: "https://github.com/user/scm-github.git",
				Bundle: "scm-github",
				IsURL:  true,
			},
		},
		{
			name:  "https URL with fragment",
			input: "https://github.com/user/scm-github#fragments/testing",
			expected: ContentRef{
				Raw:      "https://github.com/user/scm-github#fragments/testing",
				Remote:   "https://github.com/user/scm-github",
				Bundle:   "scm-github",
				ItemType: "fragments",
				ItemName: "testing",
				IsURL:    true,
			},
		},
		{
			name:  "git@ SSH URL",
			input: "git@github.com:user/scm-github",
			expected: ContentRef{
				Raw:    "git@github.com:user/scm-github",
				Remote: "git@github.com:user/scm-github",
				Bundle: "scm-github",
				IsURL:  true,
			},
		},
		{
			name:  "git@ SSH URL with .git",
			input: "git@github.com:user/scm-github.git",
			expected: ContentRef{
				Raw:    "git@github.com:user/scm-github.git",
				Remote: "git@github.com:user/scm-github.git",
				Bundle: "scm-github",
				IsURL:  true,
			},
		},
		{
			name:  "git@ SSH URL with fragment",
			input: "git@github.com:user/scm-github#fragments/testing",
			expected: ContentRef{
				Raw:      "git@github.com:user/scm-github#fragments/testing",
				Remote:   "git@github.com:user/scm-github",
				Bundle:   "scm-github",
				ItemType: "fragments",
				ItemName: "testing",
				IsURL:    true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseContentRef(tt.input)

			if got.Raw != tt.expected.Raw {
				t.Errorf("Raw: got %q, want %q", got.Raw, tt.expected.Raw)
			}
			if got.Remote != tt.expected.Remote {
				t.Errorf("Remote: got %q, want %q", got.Remote, tt.expected.Remote)
			}
			if got.Bundle != tt.expected.Bundle {
				t.Errorf("Bundle: got %q, want %q", got.Bundle, tt.expected.Bundle)
			}
			if got.ItemType != tt.expected.ItemType {
				t.Errorf("ItemType: got %q, want %q", got.ItemType, tt.expected.ItemType)
			}
			if got.ItemName != tt.expected.ItemName {
				t.Errorf("ItemName: got %q, want %q", got.ItemName, tt.expected.ItemName)
			}
			if got.IsURL != tt.expected.IsURL {
				t.Errorf("IsURL: got %v, want %v", got.IsURL, tt.expected.IsURL)
			}
		})
	}
}

// TestContentRefMethods verifies the ContentRef helper methods for type checking.
// These methods are convenience wrappers for determining what type of content
// a reference points to.
func TestContentRefMethods(t *testing.T) {
	tests := []struct {
		input      string
		isBundle   bool
		isFragment bool
		isPrompt   bool
		isMCP      bool
		bundlePath string
	}{
		{"go-dev", true, false, false, false, "go-dev"},
		{"go-dev#fragments/test", false, true, false, false, "go-dev"},
		{"go-dev#prompts/review", false, false, true, false, "go-dev"},
		{"go-dev#mcp/server", false, false, false, true, "go-dev"},
		{"github/go-dev", true, false, false, false, "github/go-dev"},
		{"github/go-dev#fragments/test", false, true, false, false, "github/go-dev"},
		{"https://github.com/user/repo#mcp/server", false, false, false, true, "https://github.com/user/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ref := ParseContentRef(tt.input)

			if ref.IsBundle() != tt.isBundle {
				t.Errorf("IsBundle: got %v, want %v", ref.IsBundle(), tt.isBundle)
			}
			if ref.IsFragment() != tt.isFragment {
				t.Errorf("IsFragment: got %v, want %v", ref.IsFragment(), tt.isFragment)
			}
			if ref.IsPrompt() != tt.isPrompt {
				t.Errorf("IsPrompt: got %v, want %v", ref.IsPrompt(), tt.isPrompt)
			}
			if ref.IsMCP() != tt.isMCP {
				t.Errorf("IsMCP: got %v, want %v", ref.IsMCP(), tt.isMCP)
			}
			if ref.BundlePath() != tt.bundlePath {
				t.Errorf("BundlePath: got %q, want %q", ref.BundlePath(), tt.bundlePath)
			}
		})
	}
}

// =============================================================================
// ContentRef.LocalBundlePath Tests
// =============================================================================

func TestContentRef_LocalBundlePath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple bundle", "go-dev", "go-dev"},
		{"remote/bundle", "github/go-dev", "github/go-dev"},
		{"https URL", "https://github.com/user/repo", "github.com/user/repo/repo"},
		// Note: @version in URL gets extracted as part of bundle name by extractBundleFromURL
		// LocalBundlePath strips @version from URL path but bundle name retains it
		{"https URL with @version", "https://github.com/user/repo@v1", "github.com/user/repo/repo@v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := ParseContentRef(tt.input)
			assert.Equal(t, tt.want, ref.LocalBundlePath())
		})
	}
}

// =============================================================================
// Loader Tests
// =============================================================================
//
// The Loader provides CRUD operations for profile YAML files. It searches
// through multiple directories (SCM paths) and handles both .yaml and .yml
// extensions.

// TestNewLoader verifies that the loader stores the provided directories.
func TestNewLoader(t *testing.T) {
	dirs := []string{"/path1", "/path2"}
	loader := NewLoader(dirs)
	assert.Equal(t, dirs, loader.dirs)
}

func TestLoader_List(t *testing.T) {
	tmpDir := t.TempDir()

	// Create profile files
	profile1 := `description: Profile 1
bundles:
  - bundle1
`
	profile2 := `description: Profile 2
default: true
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "profile1.yaml"), []byte(profile1), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "profile2.yaml"), []byte(profile2), 0644))

	loader := NewLoader([]string{tmpDir})
	profiles, err := loader.List()
	require.NoError(t, err)

	assert.Len(t, profiles, 2)
	// Should be sorted by name
	assert.Equal(t, "profile1", profiles[0].Name)
	assert.Equal(t, "profile2", profiles[1].Name)
}

// TestLoader_List_WithSubdirectories verifies profile naming with nested paths.
//
// NON-OBVIOUS: When profiles are in subdirectories (e.g., vendor/profile.yaml),
// the profile name includes the path (e.g., "vendor/remote"). This allows
// namespacing of profiles by source/vendor without conflicts.
func TestLoader_List_WithSubdirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure
	subDir := filepath.Join(tmpDir, "vendor")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "local.yaml"), []byte("description: local"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "remote.yaml"), []byte("description: remote"), 0644))

	loader := NewLoader([]string{tmpDir})
	profiles, err := loader.List()
	require.NoError(t, err)

	assert.Len(t, profiles, 2)

	// Check profile names include subdirectory path
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	assert.Contains(t, names, "local")
	assert.Contains(t, names, "vendor/remote")
}

func TestLoader_List_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	loader := NewLoader([]string{tmpDir})
	profiles, err := loader.List()
	require.NoError(t, err)
	assert.Empty(t, profiles)
}

func TestLoader_List_NonexistentDir(t *testing.T) {
	loader := NewLoader([]string{"/nonexistent/path"})
	profiles, err := loader.List()
	require.NoError(t, err)
	assert.Empty(t, profiles)
}

func TestLoader_Load(t *testing.T) {
	tmpDir := t.TempDir()

	profileContent := `description: Test profile
bundles:
  - bundle1
  - bundle2
tags:
  - golang
variables:
  lang: go
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test-profile.yaml"), []byte(profileContent), 0644))

	loader := NewLoader([]string{tmpDir})
	profile, err := loader.Load("test-profile")
	require.NoError(t, err)

	assert.Equal(t, "test-profile", profile.Name)
	assert.Equal(t, "Test profile", profile.Description)
	assert.Equal(t, []string{"bundle1", "bundle2"}, profile.Bundles)
	assert.Equal(t, []string{"golang"}, profile.Tags)
	assert.Equal(t, "go", profile.Variables["lang"])
}

func TestLoader_Load_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewLoader([]string{tmpDir})

	_, err := loader.Load("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLoader_Load_YmlExtension(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "profile.yml"), []byte("description: YML file"), 0644))

	loader := NewLoader([]string{tmpDir})
	profile, err := loader.Load("profile")
	require.NoError(t, err)
	assert.Equal(t, "YML file", profile.Description)
}

func TestLoader_Exists(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "exists.yaml"), []byte(""), 0644))

	loader := NewLoader([]string{tmpDir})

	assert.True(t, loader.Exists("exists"))
	assert.False(t, loader.Exists("not-exists"))
}

func TestLoader_Save(t *testing.T) {
	tmpDir := t.TempDir()

	loader := NewLoader([]string{tmpDir})
	profile := &Profile{
		Name:        "new-profile",
		Description: "A new profile",
		Bundles:     []string{"bundle1"},
		Tags:        []string{"test"},
	}

	err := loader.Save(profile)
	require.NoError(t, err)

	// Verify file was created
	assert.FileExists(t, filepath.Join(tmpDir, "new-profile.yaml"))

	// Verify we can load it back
	loaded, err := loader.Load("new-profile")
	require.NoError(t, err)
	assert.Equal(t, "A new profile", loaded.Description)
	assert.Equal(t, []string{"bundle1"}, loaded.Bundles)
}

func TestLoader_Save_NoDirs(t *testing.T) {
	loader := NewLoader([]string{})
	err := loader.Save(&Profile{Name: "test"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no profiles directory")
}

func TestLoader_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "to-delete.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte(""), 0644))

	loader := NewLoader([]string{tmpDir})

	err := loader.Delete("to-delete")
	require.NoError(t, err)

	assert.NoFileExists(t, profilePath)
}

func TestLoader_Delete_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewLoader([]string{tmpDir})

	err := loader.Delete("nonexistent")
	assert.Error(t, err)
}

func TestLoader_GetDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "default-profile.yaml"), []byte("default: true"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "regular-profile.yaml"), []byte("default: false"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "another-default.yaml"), []byte("default: true"), 0644))

	loader := NewLoader([]string{tmpDir})
	defaults := loader.GetDefaults()

	assert.Len(t, defaults, 2)
	assert.Contains(t, defaults, "another-default")
	assert.Contains(t, defaults, "default-profile")
}

// =============================================================================
// GetProfileDirs Tests
// =============================================================================

func TestGetProfileDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create profiles subdirectory in tmpDir
	profilesDir := filepath.Join(tmpDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	dirs := GetProfileDirs([]string{tmpDir})

	assert.Len(t, dirs, 1)
	assert.Equal(t, profilesDir, dirs[0])
}

func TestGetProfileDirs_NoProfilesDir(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := GetProfileDirs([]string{tmpDir})
	assert.Empty(t, dirs)
}

// =============================================================================
// ResolveProfile Tests
// =============================================================================
//
// ResolveProfile flattens the inheritance tree into a single effective profile.
// This is where parent bundles/tags/variables are merged with child values.

// TestLoader_ResolveProfile verifies the inheritance merge behavior.
//
// Key semantics:
//   - Bundles: Child bundles APPEND to parent bundles (parent first)
//   - Tags: Child tags APPEND to parent tags
//   - Variables: Child values OVERRIDE parent values (last wins)
func TestLoader_ResolveProfile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create parent profile
	parent := `description: Parent profile
bundles:
  - parent-bundle
tags:
  - parent-tag
variables:
  inherited: parent-value
  override_me: parent-value
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "parent.yaml"), []byte(parent), 0644))

	// Create child profile
	child := `description: Child profile
parents:
  - parent
bundles:
  - child-bundle
tags:
  - child-tag
variables:
  override_me: child-value
  new_var: child-only
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(child), 0644))

	loader := NewLoader([]string{tmpDir})
	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)

	// Should have bundles from both parent and child
	assert.Contains(t, resolved.Bundles, "parent-bundle")
	assert.Contains(t, resolved.Bundles, "child-bundle")

	// Should have tags from both
	assert.Contains(t, resolved.Tags, "parent-tag")
	assert.Contains(t, resolved.Tags, "child-tag")

	// Child should override parent variable
	assert.Equal(t, "child-value", resolved.Variables["override_me"])
	// Should inherit parent variable
	assert.Equal(t, "parent-value", resolved.Variables["inherited"])
	// Should have child-only variable
	assert.Equal(t, "child-only", resolved.Variables["new_var"])
}

// TestLoader_ResolveProfile_CircularReference verifies circular dependency detection.
//
// This is a safety check - without it, a circular reference like A→B→A would
// cause infinite recursion. The resolver tracks visited profiles and fails
// if it encounters one it's already processing.
func TestLoader_ResolveProfile_CircularReference(t *testing.T) {
	tmpDir := t.TempDir()

	profileA := `parents:
  - b
`
	profileB := `parents:
  - a
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "a.yaml"), []byte(profileA), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "b.yaml"), []byte(profileB), 0644))

	loader := NewLoader([]string{tmpDir})
	_, err := loader.ResolveProfile("a", nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circular")
}

func TestLoader_ResolveProfile_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewLoader([]string{tmpDir})

	_, err := loader.ResolveProfile("nonexistent", nil)
	assert.Error(t, err)
}

func TestLoader_ResolveProfile_ParentNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	profile := `parents:
  - nonexistent-parent
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(profile), 0644))

	loader := NewLoader([]string{tmpDir})
	_, err := loader.ResolveProfile("child", nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve parent")
}

// =============================================================================
// ResolvedProfile.Merge Tests
// =============================================================================
//
// Merge combines two resolved profiles. This is used when multiple profiles
// are active simultaneously (e.g., multiple default profiles).

// TestResolvedProfile_Merge verifies the merge semantics.
//
// NON-OBVIOUS: For variables, the FIRST profile wins (r1.Merge(r2) keeps r1's
// value for shared keys). This differs from parent inheritance where child
// overrides parent. The distinction:
//   - Inheritance: child → parent (child wins)
//   - Merge: profile1 + profile2 (first wins)
func TestResolvedProfile_Merge(t *testing.T) {
	r1 := &ResolvedProfile{
		Bundles:   []string{"b1"},
		Tags:      []string{"t1"},
		Variables: map[string]string{"v1": "value1", "shared": "r1"},
	}

	r2 := &ResolvedProfile{
		Bundles:   []string{"b2", "b1"}, // b1 is duplicate
		Tags:      []string{"t2"},
		Variables: map[string]string{"v2": "value2", "shared": "r2"},
	}

	r1.Merge(r2)

	// Bundles should be deduplicated
	assert.Equal(t, []string{"b1", "b2"}, r1.Bundles)
	// Tags combined
	assert.Equal(t, []string{"t1", "t2"}, r1.Tags)
	// Variables: r1 keeps its value for "shared" (first wins for variables during merge)
	assert.Equal(t, "r1", r1.Variables["shared"])
	assert.Equal(t, "value1", r1.Variables["v1"])
	assert.Equal(t, "value2", r1.Variables["v2"])
}

// =============================================================================
// appendUnique Tests
// =============================================================================

func TestAppendUnique(t *testing.T) {
	tests := []struct {
		name   string
		slice  []string
		items  []string
		want   []string
	}{
		{"empty slice", []string{}, []string{"a", "b"}, []string{"a", "b"}},
		{"no duplicates", []string{"a"}, []string{"b", "c"}, []string{"a", "b", "c"}},
		{"with duplicates", []string{"a", "b"}, []string{"b", "c", "a"}, []string{"a", "b", "c"}},
		{"all duplicates", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendUnique(tt.slice, tt.items...)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// WithFS Tests
// =============================================================================

func TestWithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/profiles", 0755))
	require.NoError(t, afero.WriteFile(fs, "/profiles/test.yaml", []byte("description: test"), 0644))

	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	// Verify it uses the custom FS
	profile, err := loader.Load("test")
	require.NoError(t, err)
	assert.Equal(t, "test", profile.Name)
	assert.Equal(t, "test", profile.Description)
}

func TestNewLoader_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	loader := NewLoader([]string{"/test"}, WithFS(fs))

	assert.NotNil(t, loader)
	assert.Equal(t, fs, loader.fs)
}

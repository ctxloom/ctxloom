// Package profiles tests verify profile parsing, resolution, and inheritance.
//
// Profiles are named collections of bundles, tags, and variables that define
// what context gets loaded for an AI session. They support inheritance through
// the `parents` field, enabling composition and reuse.
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

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Loader Tests
// =============================================================================
//
// The Loader provides CRUD operations for profile YAML files. It searches
// through multiple directories (ctxloom paths) and handles both .yaml and .yml
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

	// Create profiles subdirectory in persistent dir
	profilesDir := paths.ProfilesPath(tmpDir)
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

// TestLoader_ResolveProfile_DiamondInheritance verifies diamond inheritance works correctly.
//
// Diamond inheritance occurs when:
//
//	   A
//	  / \
//	 B   C
//	  \ /
//	   D
//
// Both B and C inherit from D. Without proper visited tracking, the resolver
// would incorrectly detect a circular reference when resolving D through C
// after already resolving D through B.
//
// This tests that the resolver clones the visited set for each parent branch,
// allowing shared ancestors to be resolved independently.
func TestLoader_ResolveProfile_DiamondInheritance(t *testing.T) {
	tmpDir := t.TempDir()

	// D is the shared ancestor
	profileD := `description: Base profile D
bundles:
  - d-bundle
tags:
  - d-tag
variables:
  from_d: d-value
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "d.yaml"), []byte(profileD), 0644))

	// B inherits from D
	profileB := `description: Profile B
parents:
  - d
bundles:
  - b-bundle
tags:
  - b-tag
variables:
  from_b: b-value
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "b.yaml"), []byte(profileB), 0644))

	// C inherits from D
	profileC := `description: Profile C
parents:
  - d
bundles:
  - c-bundle
tags:
  - c-tag
variables:
  from_c: c-value
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "c.yaml"), []byte(profileC), 0644))

	// A inherits from both B and C (diamond)
	profileA := `description: Profile A
parents:
  - b
  - c
bundles:
  - a-bundle
tags:
  - a-tag
variables:
  from_a: a-value
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "a.yaml"), []byte(profileA), 0644))

	loader := NewLoader([]string{tmpDir})

	// This should succeed - not falsely detect circular reference
	resolved, err := loader.ResolveProfile("a", nil)
	require.NoError(t, err)

	// Should have bundles from all profiles
	assert.Contains(t, resolved.Bundles, "d-bundle")
	assert.Contains(t, resolved.Bundles, "b-bundle")
	assert.Contains(t, resolved.Bundles, "c-bundle")
	assert.Contains(t, resolved.Bundles, "a-bundle")

	// Should have tags from all profiles
	assert.Contains(t, resolved.Tags, "d-tag")
	assert.Contains(t, resolved.Tags, "b-tag")
	assert.Contains(t, resolved.Tags, "c-tag")
	assert.Contains(t, resolved.Tags, "a-tag")

	// Should have variables from all profiles
	assert.Equal(t, "d-value", resolved.Variables["from_d"])
	assert.Equal(t, "b-value", resolved.Variables["from_b"])
	assert.Equal(t, "c-value", resolved.Variables["from_c"])
	assert.Equal(t, "a-value", resolved.Variables["from_a"])
}

// TestLoader_ResolveProfile_DepthLimit verifies that deeply nested inheritance is rejected.
//
// This prevents stack overflow from malformed configurations with extremely
// deep inheritance chains.
func TestLoader_ResolveProfile_DepthLimit(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a chain deeper than maxProfileDepth (64)
	// We'll create 70 profiles: p0 -> p1 -> p2 -> ... -> p69
	for i := 0; i < 70; i++ {
		var content string
		if i == 0 {
			content = "description: Base profile"
		} else {
			content = "parents:\n  - p" + string(rune('0'+((i-1)/10))) + string(rune('0'+((i-1)%10)))
		}
		filename := "p" + string(rune('0'+(i/10))) + string(rune('0'+(i%10))) + ".yaml"
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644))
	}

	loader := NewLoader([]string{tmpDir})

	// Resolving p69 requires 70 levels of depth, exceeding the limit of 64
	_, err := loader.ResolveProfile("p69", nil)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "depth exceeds maximum")
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

// =============================================================================
// toLocalProfileName Tests
// =============================================================================

func TestToLocalProfileName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name unchanged",
			input:    "my-profile",
			expected: "my-profile",
		},
		{
			name:     "remote/name unchanged",
			input:    "github/go-developer",
			expected: "github/go-developer",
		},
		{
			name:     "https URL converted to local path",
			input:    "https://github.com/owner/repo@v1/profiles/go-developer",
			expected: "github.com/owner/repo/go-developer",
		},
		{
			name:     "git@ SSH URL converted to local path",
			input:    "git@github.com:owner/repo@v1/profiles/go-developer",
			expected: "github.com/owner/repo/go-developer",
		},
		{
			name:     "file:// URL converted to local path",
			input:    "file:///home/user/ctxloom-content@v1/profiles/test",
			expected: "user/ctxloom-content/test",
		},
		{
			name:     "nested path in URL",
			input:    "https://github.com/org/subgroup/repo@v1/profiles/base",
			expected: "github.com/org/subgroup/repo/base",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toLocalProfileName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// =============================================================================
// URL Parent Resolution Tests
// =============================================================================

// TestLoader_ResolveProfile_URLParent verifies that URL parent references are
// resolved to their local storage paths.
//
// When a profile has a parent like:
//   - https://github.com/owner/repo@v1/profiles/base
//
// The resolver should look for the profile at:
//   - .ctxloom/persistent/profiles/github.com/owner/repo/base.yaml
func TestLoader_ResolveProfile_URLParent(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create directories
	require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles", 0755))
	require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles/github.com/owner/repo", 0755))

	// Create the "remote" parent profile (as if synced from URL)
	baseProfile := `description: Base Go profile
bundles:
  - go-tools
tags:
  - golang
variables:
  go_version: "1.21"
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/github.com/owner/repo/go-base.yaml",
		[]byte(baseProfile), 0644))

	// Create child profile that references the parent via URL
	childProfile := `description: Project profile
parents:
  - https://github.com/owner/repo@v1/profiles/go-base
bundles:
  - project-tools
variables:
  project_name: my-project
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/project-dev.yaml",
		[]byte(childProfile), 0644))

	loader := NewLoader([]string{"/project/.ctxloom/persistent/profiles"}, WithFS(fs))

	// Resolve the child profile
	resolved, err := loader.ResolveProfile("project-dev", nil)
	require.NoError(t, err)

	// Should have bundles from both profiles
	assert.Contains(t, resolved.Bundles, "go-tools")
	assert.Contains(t, resolved.Bundles, "project-tools")

	// Should have tags from parent
	assert.Contains(t, resolved.Tags, "golang")

	// Should have variables from both (child overrides parent)
	assert.Equal(t, "1.21", resolved.Variables["go_version"])
	assert.Equal(t, "my-project", resolved.Variables["project_name"])
}

// TestLoader_ResolveProfile_URLParentNotSynced verifies error when URL parent
// hasn't been synced locally.
func TestLoader_ResolveProfile_URLParentNotSynced(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles", 0755))

	// Create child profile that references an unsynced parent
	childProfile := `parents:
  - https://github.com/nonexistent/repo@v1/profiles/missing
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/child.yaml",
		[]byte(childProfile), 0644))

	loader := NewLoader([]string{"/project/.ctxloom/persistent/profiles"}, WithFS(fs))

	_, err := loader.ResolveProfile("child", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve parent")
}

// TestLoader_ResolveProfile_MixedParents verifies resolution with both local
// and URL parent references.
func TestLoader_ResolveProfile_MixedParents(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles", 0755))
	require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles/github.com/ctxloom/ctxloom-default", 0755))

	// Local parent
	localParent := `bundles:
  - local-tools
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/local-base.yaml",
		[]byte(localParent), 0644))

	// Remote parent (synced)
	remoteParent := `bundles:
  - remote-tools
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/github.com/ctxloom/ctxloom-default/go-base.yaml",
		[]byte(remoteParent), 0644))

	// Child with both parents
	childProfile := `parents:
  - local-base
  - https://github.com/ctxloom/ctxloom-default@v1/profiles/go-base
bundles:
  - child-tools
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/mixed.yaml",
		[]byte(childProfile), 0644))

	loader := NewLoader([]string{"/project/.ctxloom/persistent/profiles"}, WithFS(fs))

	resolved, err := loader.ResolveProfile("mixed", nil)
	require.NoError(t, err)

	assert.Contains(t, resolved.Bundles, "local-tools")
	assert.Contains(t, resolved.Bundles, "remote-tools")
	assert.Contains(t, resolved.Bundles, "child-tools")
}


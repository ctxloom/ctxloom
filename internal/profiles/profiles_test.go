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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
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

// TestLoader_Load_RemoteRefNotSeeded verifies the not-found error for a
// remote-shaped ref points at the actual remediation: remote profiles only
// resolve through the lockfile-built seed, so the miss means "not pulled",
// not "doesn't exist upstream".
func TestLoader_Load_RemoteRefNotSeeded(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewLoader([]string{tmpDir})

	_, err := loader.Load("https://github.com/owner/repo@profiles/default")
	assert.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrProfileNotFound)
	assert.Contains(t, err.Error(), "ctxloom remote pull")
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

// TestLoader_Save_SubdirName pins saving the subdir-qualified names List
// itself produces (e.g. "team/dev"): Save must create the intermediate
// directories instead of failing ENOENT on the file write.
func TestLoader_Save_SubdirName(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewLoader([]string{tmpDir})

	require.NoError(t, loader.Save(&Profile{Name: "team/dev", Description: "nested"}))
	assert.FileExists(t, filepath.Join(tmpDir, "team", "dev.yaml"))

	loaded, err := loader.Load("team/dev")
	require.NoError(t, err)
	assert.Equal(t, "nested", loaded.Description)
}

// TestLoader_Save_RejectsTraversal pins the path-traversal chokepoint: a name
// that escapes the profiles directory must be rejected, never written.
// (Bundles have ValidateBundleName; profiles join Name into a path with the
// same risk.)
func TestLoader_Save_RejectsTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	loader := NewLoader([]string{filepath.Join(tmpDir, "profiles")})

	for _, name := range []string{"../evil", "/abs/path", "a/../../evil"} {
		t.Run(name, func(t *testing.T) {
			err := loader.Save(&Profile{Name: name})
			require.Error(t, err, "name %q must be rejected", name)
		})
	}
	// Legitimate dot-prefixed names are NOT traversal.
	require.NoError(t, loader.Save(&Profile{Name: "..hidden"}))
}

// TestLoader_Save_RoundTripsYmlFile pins extension round-tripping: a profile
// loaded from a .yml file saves back to that file instead of duplicating
// itself as a sibling .yaml.
func TestLoader_Save_RoundTripsYmlFile(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "dev.yml"), []byte("description: original\n"), 0o644))

	loader := NewLoader([]string{tmpDir})
	loaded, err := loader.Load("dev")
	require.NoError(t, err)

	loaded.Description = "edited"
	require.NoError(t, loader.Save(loaded))

	assert.NoFileExists(t, filepath.Join(tmpDir, "dev.yaml"), "save must not duplicate the profile under a second extension")
	again, err := loader.Load("dev")
	require.NoError(t, err)
	assert.Equal(t, "edited", again.Description)
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

// TestLoader_ResolveProfile_Prompts verifies a directory profile's curated
// prompts: list round-trips through resolution and unions with a parent's, the
// directory-side mirror of config.Profile.Prompts (b626431) that feeds
// backends.LoadSkillExports' opt-in prompt curation.
func TestLoader_ResolveProfile_Prompts(t *testing.T) {
	tmpDir := t.TempDir()

	parent := `description: Parent profile
prompts:
  - "tools#prompts/review"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "parent.yaml"), []byte(parent), 0644))

	child := `description: Child profile
parents:
  - parent
prompts:
  - "tools#prompts/explain"
  - "tools#prompts/review"
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(child), 0644))

	loader := NewLoader([]string{tmpDir})
	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)

	// Parent prompt first (depth-first), then the child's, deduped by stored ref.
	assert.Equal(t, []string{"tools#prompts/review", "tools#prompts/explain"}, resolved.Prompts)
}

// TestLoader_ResolveProfile_LLM verifies the preferred-LLM field round-trips
// through save/load and inheritance: a child's llm overrides its parent's, and
// a child without one inherits the parent's.
func TestLoader_ResolveProfile_LLM(t *testing.T) {
	tmpDir := t.TempDir()

	parent := "llm: agy-code\nbundles:\n  - parent-bundle\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "parent.yaml"), []byte(parent), 0644))

	// Child overrides the parent's llm.
	overrider := "parents:\n  - parent\nllm: claude-fast\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "overrider.yaml"), []byte(overrider), 0644))

	// Child inherits the parent's llm (declares none of its own).
	inheritor := "parents:\n  - parent\nbundles:\n  - child-bundle\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "inheritor.yaml"), []byte(inheritor), 0644))

	loader := NewLoader([]string{tmpDir})

	overriderResolved, err := loader.ResolveProfile("overrider", nil)
	require.NoError(t, err)
	assert.Equal(t, "claude-fast", overriderResolved.LLM, "child llm should win over parent")

	inheritorResolved, err := loader.ResolveProfile("inheritor", nil)
	require.NoError(t, err)
	assert.Equal(t, "agy-code", inheritorResolved.LLM, "child should inherit parent llm")
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

// TestLoader_ResolveProfile_ParentNotFound verifies that an unresolvable parent
// is treated as a warn-and-continue: resolution succeeds, that parent branch is
// skipped, and the child's own settings still apply. This matches ctxloom's
// fault-tolerance philosophy (CLAUDE.md) — a missing parent should not block
// the user from reaching their LLM.
func TestLoader_ResolveProfile_ParentNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	profile := `parents:
  - nonexistent-parent
bundles:
  - own-bundle
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(profile), 0644))

	loader := NewLoader([]string{tmpDir})
	resolved, err := loader.ResolveProfile("child", nil)

	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, []string{"own-bundle"}, resolved.Bundles)
}

// TestLoader_ResolveProfile_CorruptParent verifies that a parent which fails to
// parse (invalid YAML) is treated like a missing parent: warn-and-continue, the
// rest of the profile still resolves. Per CLAUDE.md fault tolerance, a corrupt
// file must not block the user from reaching their LLM.
func TestLoader_ResolveProfile_CorruptParent(t *testing.T) {
	tmpDir := t.TempDir()

	// Invalid YAML (unterminated flow sequence) for the parent.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "broken.yaml"), []byte("parents: [oops\n  bad: : :\n"), 0644))
	child := `parents:
  - broken
bundles:
  - own-bundle
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(child), 0644))

	loader := NewLoader([]string{tmpDir})
	resolved, err := loader.ResolveProfile("child", nil)

	require.NoError(t, err, "a corrupt parent must degrade to warn-and-continue, not abort")
	require.NotNil(t, resolved)
	assert.Equal(t, []string{"own-bundle"}, resolved.Bundles)
}

// TestLoader_ResolveProfile_DiamondInheritance verifies diamond inheritance works correctly.
//
// Diamond inheritance occurs when:
//
//	  A
//	 / \
//	B   C
//	 \ /
//	  D
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
		Prompts:   []string{"p1"},
		Variables: map[string]string{"v1": "value1", "shared": "r1"},
	}

	r2 := &ResolvedProfile{
		Bundles:   []string{"b2", "b1"}, // b1 is duplicate
		Tags:      []string{"t2"},
		Prompts:   []string{"p2", "p1"}, // p1 is duplicate
		Variables: map[string]string{"v2": "value2", "shared": "r2"},
	}

	r1.Merge(r2)

	// Bundles should be deduplicated
	assert.Equal(t, []string{"b1", "b2"}, r1.Bundles)
	// Tags combined
	assert.Equal(t, []string{"t1", "t2"}, r1.Tags)
	// Curated prompts combined + deduped (union across active profiles).
	assert.Equal(t, []string{"p1", "p2"}, r1.Prompts)
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
		name  string
		slice []string
		items []string
		want  []string
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
			input:    "https://github.com/owner/repo@profiles/go-developer",
			expected: "github.com/owner/repo/go-developer",
		},
		{
			name:     "git@ SSH URL converted to local path",
			input:    "git@github.com:owner/repo@profiles/go-developer",
			expected: "github.com/owner/repo/go-developer",
		},
		{
			name:     "file:// URL converted to local path",
			input:    "file:///home/user/ctxloom-content@profiles/test",
			expected: "user/ctxloom-content/test",
		},
		{
			name:     "nested path in URL",
			input:    "https://github.com/org/subgroup/repo@profiles/base",
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
//   - https://github.com/owner/repo@profiles/base
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
  - https://github.com/owner/repo@profiles/go-base
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

// TestLoader_ResolveProfile_URLParentNotSynced verifies that an unsynced URL
// parent is skipped with a warning rather than aborting resolution. The child's
// own settings still apply so the user can reach their LLM even when remote
// dependencies haven't been pulled yet.
func TestLoader_ResolveProfile_URLParentNotSynced(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles", 0755))

	// Create child profile that references an unsynced parent
	childProfile := `parents:
  - https://github.com/nonexistent/repo@profiles/missing
tags:
  - own-tag
`
	require.NoError(t, afero.WriteFile(fs,
		"/project/.ctxloom/persistent/profiles/child.yaml",
		[]byte(childProfile), 0644))

	loader := NewLoader([]string{"/project/.ctxloom/persistent/profiles"}, WithFS(fs))

	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, []string{"own-tag"}, resolved.Tags)
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
  - https://github.com/ctxloom/ctxloom-default@profiles/go-base
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

// =============================================================================
// Seeded Remote Profile Tests
// =============================================================================

// seededGoBase is a remote profile as the config seed builds it: keyed and
// named by its version-less canonical ref, never materialized to disk.
func seededGoBase(canonical string) map[string]*Profile {
	return map[string]*Profile{
		canonical: {
			Name:      canonical,
			Path:      "<remote>:" + canonical + "@abc1234",
			Bundles:   []string{"go-tools"},
			Tags:      []string{"golang"},
			Variables: map[string]string{"go_version": "1.21"},
		},
	}
}

// TestLoader_Load_SeededCanonicalRef verifies that Load finds a seeded remote
// profile under its version-less canonical key, including when the requested
// ref carries a content version ("...@<sha>"). Remote profiles are never
// written to disk, so this seed lookup is the only way they resolve.
func TestLoader_Load_SeededCanonicalRef(t *testing.T) {
	const canonical = "https://github.com/owner/repo@profiles/go-base"
	loader := NewLoader([]string{t.TempDir()}, WithSeededProfiles(seededGoBase(canonical)))

	exact, err := loader.Load(canonical)
	require.NoError(t, err)
	assert.Equal(t, canonical, exact.Name)

	versioned, err := loader.Load(canonical + "@abc1234")
	require.NoError(t, err)
	assert.Equal(t, canonical, versioned.Name, "versioned ref should normalize to the seed key")
}

// TestLoader_ResolveProfile_SeededCanonicalParent is the regression test for
// remote parent profiles never resolving: the seed map keys by version-less
// canonical ref, but parent resolution used to convert the ref to its local
// materialized name ("github.com/owner/repo/go-base") — which matched neither
// the seed nor any file on disk, so the parent's content silently vanished
// behind a "not installed" warning.
func TestLoader_ResolveProfile_SeededCanonicalParent(t *testing.T) {
	const canonical = "https://github.com/owner/repo@profiles/go-base"

	for _, tc := range []struct {
		name      string
		parentRef string
	}{
		{"version-less canonical ref", canonical},
		{"version-suffixed ref", canonical + "@abc1234"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			require.NoError(t, fs.MkdirAll("/project/.ctxloom/persistent/profiles", 0755))
			child := "parents:\n  - " + tc.parentRef + "\nbundles:\n  - project-tools\n"
			require.NoError(t, afero.WriteFile(fs,
				"/project/.ctxloom/persistent/profiles/project-dev.yaml",
				[]byte(child), 0644))

			loader := NewLoader([]string{"/project/.ctxloom/persistent/profiles"},
				WithFS(fs), WithSeededProfiles(seededGoBase(canonical)))

			// Capture stderr: a regression re-introduces the
			// "parent ... not installed; skipping" warning.
			oldStderr := os.Stderr
			r, w, err := os.Pipe()
			require.NoError(t, err)
			os.Stderr = w
			resolved, resolveErr := loader.ResolveProfile("project-dev", nil)
			require.NoError(t, w.Close())
			os.Stderr = oldStderr
			captured, err := io.ReadAll(r)
			require.NoError(t, err)

			require.NoError(t, resolveErr)
			assert.NotContains(t, string(captured), "not installed",
				"seeded parent must resolve without a skip warning")

			// Parent content merged with the child's own.
			assert.Contains(t, resolved.Bundles, "go-tools")
			assert.Contains(t, resolved.Bundles, "project-tools")
			assert.Contains(t, resolved.Tags, "golang")
			assert.Equal(t, "1.21", resolved.Variables["go_version"])
		})
	}
}

// =============================================================================
// Exclusion Resolution Tests
// =============================================================================

// TestLoader_ResolveProfile_Exclusions verifies that exclude_fragments and
// exclude_mcp survive directory-profile resolution and accumulate through the
// parent chain — a child cannot un-exclude what a parent excluded, matching
// the inline config-map profile semantics.
func TestLoader_ResolveProfile_Exclusions(t *testing.T) {
	tmpDir := t.TempDir()

	parent := `bundles:
  - shared-bundle
exclude_fragments:
  - verbose-logging
exclude_mcp:
  - slow-server
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "parent.yaml"), []byte(parent), 0644))

	child := `parents:
  - parent
exclude_fragments:
  - deprecated-style
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(child), 0644))

	loader := NewLoader([]string{tmpDir})
	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"verbose-logging", "deprecated-style"}, resolved.ExcludeFragments,
		"exclusions accumulate from parent and child")
	assert.Equal(t, []string{"slow-server"}, resolved.ExcludeMCP)
}

// fragNames extracts the ordered fragment-ref names from a ResolvedProfile.
func fragNames(frags []FragmentRef) []string {
	out := make([]string, 0, len(frags))
	for _, f := range frags {
		out = append(out, f.Name)
	}
	return out
}

// preToolCommands extracts the ordered pre_tool hook commands.
func preToolCommands(h wire.HooksConfig) []string {
	out := make([]string, 0, len(h.Unified.PreTool))
	for _, hook := range h.Unified.PreTool {
		out = append(out, hook.Command)
	}
	return out
}

// TestLoader_ResolveProfile_InlineFields verifies a directory profile's inline
// fragments:/bundle_items:/hooks:/mcp: round-trip through resolution and union
// with a parent's — the directory-side mirror of config.Profile's fields that
// brings directory profiles to parity with inline profiles. Parent items come
// first (depth-first), then the child's; a "@<commit>" pin stays in the stored
// fragment Name (version-agnostic identity; split transiently downstream).
func TestLoader_ResolveProfile_InlineFields(t *testing.T) {
	tmpDir := t.TempDir()

	parent := `description: Parent
fragments:
  - base-frag
bundle_items:
  - remote/b:fragments/x
hooks:
  unified:
    pre_tool:
      - command: parent-hook
        type: command
mcp:
  servers:
    parent-srv:
      command: parent-cmd
    shared-srv:
      command: parent-shared
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "parent.yaml"), []byte(parent), 0644))

	child := `description: Child
parents:
  - parent
fragments:
  - "remote/b@c1#fragments/pinned"
  - name: pri-frag
    priority: 7
bundle_items:
  - remote/b:fragments/y
hooks:
  unified:
    pre_tool:
      - command: child-hook
        type: command
mcp:
  servers:
    child-srv:
      command: child-cmd
    shared-srv:
      command: child-shared
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(child), 0644))

	loader := NewLoader([]string{tmpDir})
	resolved, err := loader.ResolveProfile("child", nil)
	require.NoError(t, err)

	// Fragments union: parent first (depth-first), then child; the "@<commit>"
	// pin stays in the stored Name.
	assert.Equal(t, []string{"base-frag", "remote/b@c1#fragments/pinned", "pri-frag"},
		fragNames(resolved.Fragments))
	for _, f := range resolved.Fragments {
		if f.Name == "pri-frag" {
			assert.Equal(t, 7, f.Priority, "child fragment priority is preserved")
		}
	}

	// bundle_items union, parent then child.
	assert.Equal(t, []string{"remote/b:fragments/x", "remote/b:fragments/y"}, resolved.BundleItems)

	// Hooks union (parent then child).
	assert.Equal(t, []string{"parent-hook", "child-hook"}, preToolCommands(resolved.Hooks))

	// MCP: both distinct servers present; for a name in both, the child (applied
	// after parents) wins — "later wins per server name", matching the inline
	// mergeMCP semantics.
	assert.Contains(t, resolved.MCP.Servers, "parent-srv")
	assert.Contains(t, resolved.MCP.Servers, "child-srv")
	assert.Equal(t, "child-shared", resolved.MCP.Servers["shared-srv"].Command,
		"a server declared by both parent and child resolves to the child's")
}

// TestResolvedProfile_Merge_InlineFields verifies Merge unions the new
// directly-declared fields across resolved profiles (the cross-default-profile
// fold), deduping fragments by Name, unioning bundle_items, appending hooks, and
// letting a later MCP server name win — parity with how Bundles/Tags/Prompts merge.
func TestResolvedProfile_Merge_InlineFields(t *testing.T) {
	r1 := &ResolvedProfile{
		Fragments:   []FragmentRef{{Name: "f1"}},
		BundleItems: []string{"bi1"},
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "h1", Type: "command"}},
		}},
		MCP: wire.MCPConfig{Servers: map[string]wire.MCPServer{
			"s1":     {Command: "s1-cmd"},
			"shared": {Command: "from-r1"},
		}},
		Variables: map[string]string{},
	}
	r2 := &ResolvedProfile{
		Fragments:   []FragmentRef{{Name: "f2"}, {Name: "f1"}}, // f1 duplicate
		BundleItems: []string{"bi2", "bi1"},                    // bi1 duplicate
		Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "h2", Type: "command"}},
		}},
		MCP: wire.MCPConfig{Servers: map[string]wire.MCPServer{
			"s2":     {Command: "s2-cmd"},
			"shared": {Command: "from-r2"},
		}},
		Variables: map[string]string{},
	}

	r1.Merge(r2)

	assert.Equal(t, []string{"f1", "f2"}, fragNames(r1.Fragments), "fragments union, deduped by Name")
	assert.Equal(t, []string{"bi1", "bi2"}, r1.BundleItems, "bundle_items union, deduped")
	assert.Equal(t, []string{"h1", "h2"}, preToolCommands(r1.Hooks), "hooks accumulate across profiles")
	assert.Contains(t, r1.MCP.Servers, "s1")
	assert.Contains(t, r1.MCP.Servers, "s2")
	assert.Equal(t, "from-r2", r1.MCP.Servers["shared"].Command, "later (merged-in) server name wins")
}

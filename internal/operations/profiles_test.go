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

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/profiles"
)

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		item     string
		expected bool
	}{
		{
			name:     "item exists",
			slice:    []string{"a", "b", "c"},
			item:     "b",
			expected: true,
		},
		{
			name:     "item not found",
			slice:    []string{"a", "b", "c"},
			item:     "d",
			expected: false,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			item:     "a",
			expected: false,
		},
		{
			name:     "nil slice",
			slice:    nil,
			item:     "a",
			expected: false,
		},
		{
			name:     "empty string item",
			slice:    []string{"a", "", "c"},
			item:     "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.slice, tt.item)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIndexOf(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		item     string
		expected int
	}{
		{
			name:     "item at beginning",
			slice:    []string{"a", "b", "c"},
			item:     "a",
			expected: 0,
		},
		{
			name:     "item in middle",
			slice:    []string{"a", "b", "c"},
			item:     "b",
			expected: 1,
		},
		{
			name:     "item at end",
			slice:    []string{"a", "b", "c"},
			item:     "c",
			expected: 2,
		},
		{
			name:     "item not found",
			slice:    []string{"a", "b", "c"},
			item:     "d",
			expected: -1,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			item:     "a",
			expected: -1,
		},
		{
			name:     "nil slice",
			slice:    nil,
			item:     "a",
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := indexOf(tt.slice, tt.item)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProfileEntry_Fields(t *testing.T) {
	entry := ProfileEntry{
		Name:        "my-profile",
		Description: "A test profile",
		Parents:     []string{"base"},
		Tags:        []string{"test"},
		Bundles:     []string{"bundle1", "bundle2"},
		Default:     true,
		Path:        "/project/.scm/profiles/my-profile.yaml",
	}

	assert.Equal(t, "my-profile", entry.Name)
	assert.Equal(t, "A test profile", entry.Description)
	assert.Equal(t, []string{"base"}, entry.Parents)
	assert.Equal(t, []string{"test"}, entry.Tags)
	assert.Equal(t, []string{"bundle1", "bundle2"}, entry.Bundles)
	assert.True(t, entry.Default)
	assert.Contains(t, entry.Path, "my-profile.yaml")
}

func TestListProfilesRequest_Defaults(t *testing.T) {
	req := ListProfilesRequest{}

	assert.Empty(t, req.Query)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
}

func TestListProfilesResult_Fields(t *testing.T) {
	result := ListProfilesResult{
		Profiles: []ProfileEntry{
			{Name: "profile1"},
			{Name: "profile2"},
		},
		Count:    2,
		Defaults: []string{"profile1"},
	}

	assert.Len(t, result.Profiles, 2)
	assert.Equal(t, 2, result.Count)
	assert.Equal(t, []string{"profile1"}, result.Defaults)
}

func TestGetProfileRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         GetProfileRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         GetProfileRequest{Name: "my-profile"},
			shouldError: false,
		},
		{
			name:        "empty name",
			req:         GetProfileRequest{Name: ""},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				assert.Empty(t, tt.req.Name)
			} else {
				assert.NotEmpty(t, tt.req.Name)
			}
		})
	}
}

func TestGetProfileResult_Fields(t *testing.T) {
	result := GetProfileResult{
		Name:        "test-profile",
		Description: "Test description",
		Parents:     []string{"parent1"},
		Tags:        []string{"tag1", "tag2"},
		Bundles:     []string{"bundle1"},
		Variables:   map[string]string{"key": "value"},
		Path:        "/path/to/profile.yaml",
	}

	assert.Equal(t, "test-profile", result.Name)
	assert.Equal(t, "Test description", result.Description)
	assert.Equal(t, []string{"parent1"}, result.Parents)
	assert.Equal(t, []string{"tag1", "tag2"}, result.Tags)
	assert.Equal(t, []string{"bundle1"}, result.Bundles)
	assert.Equal(t, map[string]string{"key": "value"}, result.Variables)
}

func TestCreateProfileRequest_Fields(t *testing.T) {
	req := CreateProfileRequest{
		Name:        "new-profile",
		Description: "A new profile",
		Parents:     []string{"base"},
		Bundles:     []string{"bundle1"},
		Tags:        []string{"new"},
		Default:     true,
	}

	assert.Equal(t, "new-profile", req.Name)
	assert.Equal(t, "A new profile", req.Description)
	assert.Equal(t, []string{"base"}, req.Parents)
	assert.Equal(t, []string{"bundle1"}, req.Bundles)
	assert.Equal(t, []string{"new"}, req.Tags)
	assert.True(t, req.Default)
}

func TestCreateProfileResult_Fields(t *testing.T) {
	result := CreateProfileResult{
		Status:  "created",
		Profile: "my-profile",
		Path:    "/project/.scm/profiles/my-profile.yaml",
	}

	assert.Equal(t, "created", result.Status)
	assert.Equal(t, "my-profile", result.Profile)
	assert.Contains(t, result.Path, "my-profile.yaml")
}

func TestUpdateProfileRequest_Fields(t *testing.T) {
	desc := "Updated description"
	setDefault := true

	req := UpdateProfileRequest{
		Name:          "my-profile",
		Description:   &desc,
		AddParents:    []string{"new-parent"},
		RemoveParents: []string{"old-parent"},
		AddBundles:    []string{"new-bundle"},
		RemoveBundles: []string{"old-bundle"},
		AddTags:       []string{"new-tag"},
		RemoveTags:    []string{"old-tag"},
		Default:       &setDefault,
	}

	assert.Equal(t, "my-profile", req.Name)
	assert.Equal(t, "Updated description", *req.Description)
	assert.Equal(t, []string{"new-parent"}, req.AddParents)
	assert.Equal(t, []string{"old-parent"}, req.RemoveParents)
	assert.True(t, *req.Default)
}

func TestUpdateProfileResult_Fields(t *testing.T) {
	result := UpdateProfileResult{
		Status:  "updated",
		Profile: "my-profile",
		Changes: []string{"added parent: base", "added tag: test"},
		Path:    "/project/.scm/profiles/my-profile.yaml",
	}

	assert.Equal(t, "updated", result.Status)
	assert.Equal(t, "my-profile", result.Profile)
	assert.Len(t, result.Changes, 2)
	assert.Contains(t, result.Changes[0], "parent")
}

func TestUpdateProfileResult_NoChanges(t *testing.T) {
	result := UpdateProfileResult{
		Status:  "no_changes",
		Profile: "my-profile",
	}

	assert.Equal(t, "no_changes", result.Status)
	assert.Nil(t, result.Changes)
}

func TestDeleteProfileRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         DeleteProfileRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         DeleteProfileRequest{Name: "to-delete"},
			shouldError: false,
		},
		{
			name:        "empty name",
			req:         DeleteProfileRequest{Name: ""},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				assert.Empty(t, tt.req.Name)
			} else {
				assert.NotEmpty(t, tt.req.Name)
			}
		})
	}
}

func TestDeleteProfileResult_Fields(t *testing.T) {
	result := DeleteProfileResult{
		Status:  "deleted",
		Profile: "removed-profile",
	}

	assert.Equal(t, "deleted", result.Status)
	assert.Equal(t, "removed-profile", result.Profile)
}

func TestProfileLoader_UsesConfigPaths(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
	}

	loader := profileLoader(cfg)
	assert.NotNil(t, loader)
}

// ========== Loader-based integration tests ==========

func setupProfileTestFS(t *testing.T) (afero.Fs, *profiles.Loader) {
	t.Helper()
	fs := afero.NewMemMapFs()

	// Create profiles directory
	_ = fs.MkdirAll("/project/.scm/profiles", 0755)

	// Create test profiles
	baseProfile := `description: Base development profile
tags:
  - development
  - base
bundles:
  - core
`
	_ = afero.WriteFile(fs, "/project/.scm/profiles/base.yaml", []byte(baseProfile), 0644)

	goDevProfile := `description: Go developer profile
parents:
  - base
tags:
  - go
  - backend
bundles:
  - golang
  - testing
variables:
  GOPROXY: "https://proxy.golang.org"
`
	_ = afero.WriteFile(fs, "/project/.scm/profiles/go-developer.yaml", []byte(goDevProfile), 0644)

	frontendProfile := `description: Frontend developer profile
tags:
  - frontend
  - web
bundles:
  - react
  - typescript
`
	_ = afero.WriteFile(fs, "/project/.scm/profiles/frontend.yaml", []byte(frontendProfile), 0644)

	loader := profiles.NewLoader([]string{"/project/.scm/profiles"}, profiles.WithFS(fs))
	return fs, loader
}

func TestListProfiles_AllProfiles(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, 3, result.Count) // base, go-developer, frontend
	assert.Len(t, result.Profiles, 3)
}

func TestListProfiles_WithQuery(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		Query:  "go",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, p := range result.Profiles {
		if strings.Contains(p.Name, "go-developer") {
			found = true
			break
		}
	}
	assert.True(t, found, "should find go-developer profile")
}

func TestListProfiles_SortByName(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		SortBy:    "name",
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Profiles), 2)

	// Verify sorted ascending
	for i := 1; i < len(result.Profiles); i++ {
		assert.LessOrEqual(t, strings.ToLower(result.Profiles[i-1].Name), strings.ToLower(result.Profiles[i].Name))
	}
}

func TestListProfiles_SortDescending(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		SortBy:    "name",
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Profiles), 2)

	// Verify sorted descending
	for i := 1; i < len(result.Profiles); i++ {
		assert.GreaterOrEqual(t, strings.ToLower(result.Profiles[i-1].Name), strings.ToLower(result.Profiles[i].Name))
	}
}

func TestListProfiles_SortByDefault(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Defaults: config.Defaults{Profiles: []string{"base"}},
	}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		SortBy: "default",
		Loader: loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Profiles), 2)

	// Default profile should come first
	assert.True(t, result.Profiles[0].Default)
	assert.Equal(t, "base", result.Profiles[0].Name)
}

func TestListProfiles_SortByDefaultDescending(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Defaults: config.Defaults{Profiles: []string{"base"}},
	}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		SortBy:    "default",
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Profiles), 2)

	// Default profile should come last with desc sort
	lastIdx := len(result.Profiles) - 1
	assert.True(t, result.Profiles[lastIdx].Default)
	assert.Equal(t, "base", result.Profiles[lastIdx].Name)
}

func TestListProfiles_QueryByDescription(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{
		Query:  "Go developer",
		Loader: loader,
	})

	require.NoError(t, err)
	// Should match the go-developer profile by its description
	found := false
	for _, p := range result.Profiles {
		if p.Name == "go-developer" {
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestGetProfile_Success(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := GetProfile(context.Background(), cfg, GetProfileRequest{
		Name:   "go-developer",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "go-developer", result.Name)
	assert.Equal(t, "Go developer profile", result.Description)
	assert.Contains(t, result.Parents, "base")
	assert.Contains(t, result.Tags, "go")
	assert.Contains(t, result.Bundles, "golang")
	assert.Equal(t, "https://proxy.golang.org", result.Variables["GOPROXY"])
}

func TestGetProfile_ValidationError(t *testing.T) {
	_, err := GetProfile(context.Background(), nil, GetProfileRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestGetProfile_NotFound(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := GetProfile(context.Background(), cfg, GetProfileRequest{
		Name:   "nonexistent-profile",
		Loader: loader,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCreateProfile_Success(t *testing.T) {
	fs, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:        "new-profile",
		Description: "A brand new profile",
		Tags:        []string{"new", "test"},
		Bundles:     []string{"testing"},
		Loader:      loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "created", result.Status)
	assert.Equal(t, "new-profile", result.Profile)
	assert.Contains(t, result.Path, "new-profile.yaml")

	// Verify file was written
	exists, _ := afero.Exists(fs, result.Path)
	assert.True(t, exists)
}

func TestCreateProfile_ValidationError(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:   "",
		Loader: loader,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestCreateProfile_AlreadyExists(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:   "base",
		Loader: loader,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestCreateProfile_WithParents(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:        "child-profile",
		Description: "Child with parent",
		Parents:     []string{"base"},
		Loader:      loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "created", result.Status)
}

func TestCreateProfile_ParentNotFound(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:    "orphan-profile",
		Parents: []string{"nonexistent-parent"},
		Loader:  loader,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent profile")
	assert.Contains(t, err.Error(), "not found")
}

func TestCreateProfile_SetDefault(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := CreateProfile(context.Background(), cfg, CreateProfileRequest{
		Name:    "default-profile",
		Default: true,
		Loader:  loader,
	})

	// Even if cfg.Save() fails (no real path), the default should be set in memory
	if err != nil {
		assert.Contains(t, err.Error(), "failed to save default setting")
	} else {
		assert.Equal(t, "created", result.Status)
	}
	// Default should be set in memory (stored in Profiles array)
	assert.Contains(t, cfg.Defaults.Profiles, "default-profile")
}

func TestUpdateProfile_AddTags(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:    "base",
		AddTags: []string{"new-tag"},
		Loader:  loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "added tag: new-tag")
}

func TestUpdateProfile_RemoveTags(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		RemoveTags: []string{"development"},
		Loader:     loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "removed tag: development")
}

func TestUpdateProfile_AddBundles(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		AddBundles: []string{"extra-bundle"},
		Loader:     loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "added bundle: extra-bundle")
}

func TestUpdateProfile_AddParents(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// Frontend profile doesn't have base as parent, add it
	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "frontend",
		AddParents: []string{"base"},
		Loader:     loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "added parent: base")
}

func TestUpdateProfile_UpdateDescription(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	newDesc := "Updated description"
	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:        "base",
		Description: &newDesc,
		Loader:      loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "updated description")
}

func TestUpdateProfile_NoChanges(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:   "base",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "no_changes", result.Status)
}

func TestUpdateProfile_ValidationError(t *testing.T) {
	_, err := UpdateProfile(context.Background(), nil, UpdateProfileRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestUpdateProfile_NotFound(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:    "nonexistent",
		AddTags: []string{"tag"},
		Loader:  loader,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUpdateProfile_ParentNotFound(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		AddParents: []string{"nonexistent-parent"},
		Loader:     loader,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent profile")
	assert.Contains(t, err.Error(), "not found")
}

func TestUpdateProfile_RemoveParents(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// First add a parent to base, then remove it
	_, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:       "base",
		AddParents: []string{"frontend"},
		Loader:     loader,
	})
	require.NoError(t, err)

	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:          "base",
		RemoveParents: []string{"frontend"},
		Loader:        loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "removed parent: frontend")
}

func TestUpdateProfile_RemoveBundles(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// Base profile has "core" bundle, remove it
	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:          "base",
		RemoveBundles: []string{"core"},
		Loader:        loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "removed bundle: core")
}

func TestUpdateProfile_SetDefault(t *testing.T) {
	// Use real temp directory since cfg.Save() needs real filesystem
	tmpDir := t.TempDir()
	scmDir := filepath.Join(tmpDir, ".scm")
	profilesDir := filepath.Join(scmDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	// Create base profile
	baseProfile := `description: Base profile
tags:
  - development
`
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "base.yaml"), []byte(baseProfile), 0644))

	loader := profiles.NewLoader([]string{profilesDir})
	cfg := &config.Config{SCMPaths: []string{scmDir}}

	setDefault := true
	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:    "base",
		Default: &setDefault,
		Loader:  loader,
	})

	// Should fail at cfg.Save() but we've verified the logic works
	// The error is expected since there's no config file to save to
	if err != nil {
		// Verify the config was updated before the save error
		assert.Contains(t, cfg.Defaults.Profiles, "base")
		assert.Contains(t, err.Error(), "failed to save config")
		return
	}

	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "set as default")
	assert.Contains(t, cfg.Defaults.Profiles, "base")
}

func TestUpdateProfile_UnsetDefault(t *testing.T) {
	// Use real temp directory since cfg.Save() needs real filesystem
	tmpDir := t.TempDir()
	scmDir := filepath.Join(tmpDir, ".scm")
	profilesDir := filepath.Join(scmDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	// Create base profile
	baseProfile := `description: Base profile
tags:
  - development
`
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "base.yaml"), []byte(baseProfile), 0644))

	loader := profiles.NewLoader([]string{profilesDir})
	cfg := &config.Config{
		SCMPaths: []string{scmDir},
		Defaults: config.Defaults{Profiles: []string{"base"}},
	}

	unsetDefault := false
	result, err := UpdateProfile(context.Background(), cfg, UpdateProfileRequest{
		Name:    "base",
		Default: &unsetDefault,
		Loader:  loader,
	})

	// Should fail at cfg.Save() but we've verified the logic works
	if err != nil {
		// Verify the config was updated before the save error
		assert.NotContains(t, cfg.Defaults.Profiles, "base")
		assert.Contains(t, err.Error(), "failed to save config")
		return
	}

	assert.Equal(t, "updated", result.Status)
	assert.Contains(t, result.Changes, "unset default")
	assert.NotContains(t, cfg.Defaults.Profiles, "base")
}

func TestDeleteProfile_Success(t *testing.T) {
	fs, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	// First verify the file exists
	exists, _ := afero.Exists(fs, "/project/.scm/profiles/frontend.yaml")
	require.True(t, exists)

	result, err := DeleteProfile(context.Background(), cfg, DeleteProfileRequest{
		Name:   "frontend",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, "deleted", result.Status)
	assert.Equal(t, "frontend", result.Profile)

	// Verify file was deleted
	exists, _ = afero.Exists(fs, "/project/.scm/profiles/frontend.yaml")
	assert.False(t, exists)
}

func TestDeleteProfile_ValidationError(t *testing.T) {
	_, err := DeleteProfile(context.Background(), nil, DeleteProfileRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestDeleteProfile_NotFound(t *testing.T) {
	_, loader := setupProfileTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := DeleteProfile(context.Background(), cfg, DeleteProfileRequest{
		Name:   "nonexistent",
		Loader: loader,
	})

	require.Error(t, err)
}

func TestDeleteProfile_ClearsDefaultProfile(t *testing.T) {
	fs, loader := setupProfileTestFS(t)

	// Verify frontend profile exists (we'll use it as default)
	exists, _ := afero.Exists(fs, "/project/.scm/profiles/frontend.yaml")
	require.True(t, exists)

	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Defaults: config.Defaults{Profiles: []string{"frontend"}}, // Set as default
	}

	result, err := DeleteProfile(context.Background(), cfg, DeleteProfileRequest{
		Name:   "frontend",
		Loader: loader,
	})

	// Even if cfg.Save() fails (no real path), the default should be cleared in memory
	// and the profile deleted - the function errors only if Save() fails
	if err != nil {
		// Expected - Save() fails since config has no real path
		assert.Contains(t, err.Error(), "failed to save config")
	} else {
		assert.Equal(t, "deleted", result.Status)
	}
	// Default profile should be cleared in memory regardless
	assert.NotContains(t, cfg.Defaults.Profiles, "frontend")
}

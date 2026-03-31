package profiles

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeedsTransform(t *testing.T) {
	tests := []struct {
		name    string
		profile *Profile
		want    bool
	}{
		{
			name:    "empty bundles",
			profile: &Profile{Bundles: []string{}},
			want:    false,
		},
		{
			name:    "local only",
			profile: &Profile{Bundles: []string{"scm-github/core", "remote/bundle"}},
			want:    false,
		},
		{
			name:    "https URL",
			profile: &Profile{Bundles: []string{"https://github.com/alice/scm@v1/bundles/core"}},
			want:    true,
		},
		{
			name:    "http URL",
			profile: &Profile{Bundles: []string{"http://gitlab.example.com/repo@v1/bundles/core"}},
			want:    true,
		},
		{
			name:    "git@ URL",
			profile: &Profile{Bundles: []string{"git@github.com:alice/scm@v1/bundles/core"}},
			want:    true,
		},
		{
			name: "mixed",
			profile: &Profile{Bundles: []string{
				"local/bundle",
				"https://github.com/alice/scm@v1/bundles/core",
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedsTransform(tt.profile)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasLocalReferences(t *testing.T) {
	tests := []struct {
		name    string
		profile *Profile
		want    bool
	}{
		{
			name:    "empty bundles",
			profile: &Profile{Bundles: []string{}},
			want:    false,
		},
		{
			name:    "local only",
			profile: &Profile{Bundles: []string{"scm-github/core"}},
			want:    true,
		},
		{
			name:    "URLs only",
			profile: &Profile{Bundles: []string{"https://github.com/alice/scm@v1/bundles/core"}},
			want:    false,
		},
		{
			name: "mixed",
			profile: &Profile{Bundles: []string{
				"local/bundle",
				"https://github.com/alice/scm@v1/bundles/core",
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasLocalReferences(tt.profile)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTransformToLocal(t *testing.T) {
	t.Run("already local", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"local/bundle", "another/local"},
		}
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		result, updates, err := TransformToLocal(profile, registry, lockfile)
		require.NoError(t, err)
		assert.Equal(t, profile.Bundles, result.Bundles)
		assert.Empty(t, updates)
	})

	t.Run("canonical URL to local", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"https://github.com/alice/scm@v1/bundles/core-practices@v1.2.3"},
		}
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		result, updates, err := TransformToLocal(profile, registry, lockfile)
		require.NoError(t, err)
		assert.Len(t, result.Bundles, 1)
		assert.Contains(t, result.Bundles[0], "scm/core-practices")
		assert.Len(t, updates, 1)
		assert.Equal(t, "https://github.com/alice/scm", updates[0].Entry.URL)
		assert.Equal(t, "v1", updates[0].Entry.SCMVersion)
		assert.Equal(t, "v1.2.3", updates[0].Entry.RequestedVersion)
	})

	t.Run("mixed local and canonical", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{
				"local/bundle",
				"https://github.com/bob/scm@v1/bundles/utils",
			},
		}
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		result, updates, err := TransformToLocal(profile, registry, lockfile)
		require.NoError(t, err)
		assert.Len(t, result.Bundles, 2)
		assert.Equal(t, "local/bundle", result.Bundles[0])
		assert.Contains(t, result.Bundles[1], "scm/utils")
		assert.Len(t, updates, 1)
	})

	t.Run("with item specifier", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"https://github.com/alice/scm@v1/bundles/core#fragments/intro"},
		}
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		result, updates, err := TransformToLocal(profile, registry, lockfile)
		require.NoError(t, err)
		assert.Len(t, result.Bundles, 1)
		assert.Contains(t, result.Bundles[0], "#fragments/intro")
		assert.Len(t, updates, 1)
	})

	t.Run("invalid URL returns error", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"https://invalid-url-missing-version"},
		}
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		_, _, err = TransformToLocal(profile, registry, lockfile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid canonical URL")
	})
}

func TestTransformToCanonical(t *testing.T) {
	t.Run("already canonical", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"https://github.com/alice/scm@v1/bundles/core@v1.0.0"},
		}
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		result, err := TransformToCanonical(profile, lockfile)
		require.NoError(t, err)
		assert.Equal(t, profile.Bundles, result.Bundles)
	})

	t.Run("local to canonical", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"scm-github/core-practices"},
		}
		lockfile := &remote.Lockfile{
			Bundles: map[string]remote.LockEntry{
				"scm-github/core-practices": {
					URL:              "https://github.com/alice/scm",
					SCMVersion:       "v1",
					RequestedVersion: "v1.2.3",
				},
			},
			Profiles: make(map[string]remote.LockEntry),
		}

		result, err := TransformToCanonical(profile, lockfile)
		require.NoError(t, err)
		assert.Len(t, result.Bundles, 1)
		assert.Contains(t, result.Bundles[0], "https://github.com/alice/scm@v1")
	})

	t.Run("not in lockfile", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"missing/bundle"},
		}
		lockfile := &remote.Lockfile{
			Bundles:  make(map[string]remote.LockEntry),
			Profiles: make(map[string]remote.LockEntry),
		}

		_, err := TransformToCanonical(profile, lockfile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found in lockfile")
	})

	t.Run("with item specifier", func(t *testing.T) {
		profile := &Profile{
			Bundles: []string{"scm-github/bundle#fragments/core"},
		}
		lockfile := &remote.Lockfile{
			Bundles: map[string]remote.LockEntry{
				"scm-github/bundle": {
					URL:        "https://github.com/alice/scm",
					SCMVersion: "v1",
					SHA:        "abc123",
				},
			},
			Profiles: make(map[string]remote.LockEntry),
		}

		result, err := TransformToCanonical(profile, lockfile)
		require.NoError(t, err)
		assert.Contains(t, result.Bundles[0], "#fragments/core")
	})
}

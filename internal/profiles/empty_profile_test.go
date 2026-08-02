package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Save used to accept a profile with no content and write "{}\n",
// reporting success — the writer treating empty as authoritative. A profile
// that selects nothing composes nothing, and a session launched on it is the
// silent no-op this codebase is named for.
func TestSave_EmptyProfileIsRefused(t *testing.T) {
	fs := afero.NewMemMapFs()
	l := NewLoader([]string{"/proj/.ctxloom/profiles"}, WithFS(fs))

	err := l.Save(&Profile{Name: "hollow"})
	assert.Error(t, err, "a profile with no content must not be written as `{}`")

	exists, _ := afero.Exists(fs, "/proj/.ctxloom/profiles/hollow.yaml")
	assert.False(t, exists, "nothing may be written for a refused save")
}

// A profile carrying any real selection still saves.
func TestSave_ProfileWithContentStillSaves(t *testing.T) {
	fs := afero.NewMemMapFs()
	l := NewLoader([]string{"/proj/.ctxloom/profiles"}, WithFS(fs))

	require.NoError(t, l.Save(&Profile{Name: "real", Bundles: []string{"go-development"}}))
	exists, _ := afero.Exists(fs, "/proj/.ctxloom/profiles/real.yaml")
	assert.True(t, exists)
}

// A zero-byte / fully-commented-out / `{}` profile file used to load with
// err=nil into a completely empty Profile and recorded ZERO strictness
// findings — a session launched on it gets no context at all, and nothing
// anywhere said so. Chose a fail-loudly finding over a hard load error so
// enumeration (List, the pickers) still works on a half-authored profile.
func TestLoad_EmptyProfileFileIsReported(t *testing.T) {
	for name, body := range map[string]string{
		"zero bytes":   "",
		"empty map":    "{}\n",
		"all comments": "# nothing here\n# really\n",
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom/profiles/hollow.yaml", []byte(body), 0o644))
			l := NewLoader([]string{"/proj/.ctxloom/profiles"}, WithFS(fs))

			strictness.Reset()
			strictness.SetDegraded(false)
			mark := strictness.Checkpoint()
			defer strictness.Close(mark)

			_, err := l.Load("hollow")
			require.NoError(t, err, "a hollow profile must still ENUMERATE")
			assert.NotEmpty(t, strictness.Since(mark),
				"a profile that selects nothing must record a fail-loudly finding, not pass silently")
		})
	}
}

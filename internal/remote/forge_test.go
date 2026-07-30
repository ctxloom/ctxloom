package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveForge_BuiltinDefaults(t *testing.T) {
	forges := MergeForges(nil)

	tests := []struct {
		name      string
		url       string
		label     string
		wantType  ForgeType
		wantToken string
	}{
		{"github.com via host", "https://github.com/owner/repo", "", ForgeGitHub, DefaultGitHubTokenEnv},
		{"owner/repo shorthand", "owner/repo", "", ForgeGitHub, DefaultGitHubTokenEnv},
		{"non-github host falls to git", "https://gitlab.com/owner/repo", "", ForgeGitGeneric, ""},
		{"explicit git label", "https://github.com/owner/repo", "git", ForgeGitGeneric, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := resolveForge(tt.url, tt.label, forges)
			assert.Equal(t, tt.wantType, rf.Type)
			assert.Equal(t, tt.wantToken, rf.TokenEnv)
		})
	}
}

func TestResolveForge_ExplicitLabel(t *testing.T) {
	forges := MergeForges(map[string]ForgeConfig{
		"work-ghe": {Type: "github", Body: map[string]any{
			"base_url":  "https://github.mycorp.com",
			"api_url":   "https://github.mycorp.com/api/v3",
			"token_env": "GHE_TOKEN",
		}},
	})

	rf := resolveForge("https://github.mycorp.com/me/repo", "work-ghe", forges)
	assert.Equal(t, ForgeGitHub, rf.Type)
	assert.Equal(t, "https://github.mycorp.com", rf.BaseURL)
	assert.Equal(t, "https://github.mycorp.com/api/v3", rf.APIURL)
	assert.Equal(t, "GHE_TOKEN", rf.TokenEnv)
}

func TestResolveForge_HostMatch(t *testing.T) {
	forges := MergeForges(map[string]ForgeConfig{
		"work-ghe": {Type: "github", Body: map[string]any{
			"base_url":  "https://github.mycorp.com",
			"token_env": "GHE_TOKEN",
		}},
	})

	// No explicit label: host of the remote URL matches the forge base_url.
	rf := resolveForge("https://github.mycorp.com/me/repo", "", forges)
	assert.Equal(t, ForgeGitHub, rf.Type)
	assert.Equal(t, "GHE_TOKEN", rf.TokenEnv)
}

// TestResolveForge_UnknownLabelCannotBePersisted pins the guard that makes
// U093-F07 unreachable.
//
// The row claimed an unknown `forge:` label on a remote is silently ignored and
// falls through to host matching, binding the remote to a different endpoint
// and a different token env. resolveForge's label lookup does indeed fall
// through when the label is absent — but a remote cannot ACQUIRE an unknown
// label: SetForge validates against MergeForges(registry forges) and refuses,
// leaving the previous binding in place. Every label that can reach
// resolveForge is therefore a label resolveForge can find.
//
// This goes red the moment that validation is relaxed.
func TestResolveForge_UnknownLabelCannotBePersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.yaml")
	reg, err := NewRegistry(path)
	require.NoError(t, err)
	require.NoError(t, reg.Add("corp", "https://git.example.com/corp/repo"))
	require.NoError(t, reg.SetForge("corp", "git"))

	require.Error(t, reg.SetForge("corp", "no-such-forge"))

	rem, err := reg.Get("corp")
	require.NoError(t, err)
	assert.Equal(t, "git", rem.Forge, "a refused bind must not overwrite the previous label")

	// The surviving label resolves against the merged forge set — the lookup
	// arm the row said was skipped.
	forges := MergeForges(reg.Forges())
	require.Contains(t, forges, rem.Forge)
	assert.Equal(t, ForgeGitGeneric, resolveForge(rem.URL, rem.Forge, forges).Type)
}

// TestResolveForge_HostMatchIsDeterministic pins that host matching answers the
// same way every time. The candidate set is a map, and ranging a Go map yields
// a randomised order per iteration, so two forges sharing a base_url host used
// to bind the remote to a different endpoint and a DIFFERENT token env from one
// process to the next. Lowest label wins, and it wins every time.
func TestResolveForge_HostMatchIsDeterministic(t *testing.T) {
	forges := MergeForges(map[string]ForgeConfig{
		"zulu-ghe": {Type: "github", Body: map[string]any{
			"base_url":  "https://git.acme.example",
			"token_env": "ZULU_TOKEN",
		}},
		"alfa-ghe": {Type: "github", Body: map[string]any{
			"base_url":  "https://git.acme.example",
			"token_env": "ALFA_TOKEN",
		}},
		"mike-ghe": {Type: "github", Body: map[string]any{
			"base_url":  "https://git.acme.example",
			"token_env": "MIKE_TOKEN",
		}},
	})

	for i := 0; i < 200; i++ {
		rf := resolveForge("https://git.acme.example/me/repo", "", forges)
		require.Equal(t, "ALFA_TOKEN", rf.TokenEnv, "host match must not depend on map iteration order (attempt %d)", i)
	}
}

func TestResolvedForge_Token(t *testing.T) {
	t.Run("github reads token_env over ambient", func(t *testing.T) {
		t.Setenv("GHE_TOKEN", "ghe-secret")
		rf := ResolvedForge{Type: ForgeGitHub, TokenEnv: "GHE_TOKEN"}
		assert.Equal(t, "ghe-secret", rf.Token(AuthConfig{GitHub: "ambient"}))
	})

	t.Run("github falls back to ambient when token_env unset", func(t *testing.T) {
		os.Unsetenv("MISSING_TOKEN")
		rf := ResolvedForge{Type: ForgeGitHub, TokenEnv: "MISSING_TOKEN"}
		assert.Equal(t, "ambient", rf.Token(AuthConfig{GitHub: "ambient"}))
	})

	t.Run("generic git holds no token", func(t *testing.T) {
		rf := ResolvedForge{Type: ForgeGitGeneric, TokenEnv: "ANY"}
		assert.Empty(t, rf.Token(AuthConfig{GitHub: "ambient"}))
	})
}

func TestRegistry_ForgePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "remotes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"forges:\n"+
			"  work-ghe:\n"+
			"    type: github\n"+
			"    base_url: https://github.mycorp.com\n"+
			"    token_env: GHE_TOKEN\n"+
			"remotes:\n"+
			"  myprofiles:\n"+
			"    url: https://github.mycorp.com/me/profiles\n"+
			"    forge: work-ghe\n"), 0o644))

	reg, err := NewRegistry(path)
	require.NoError(t, err)

	rem, err := reg.Get("myprofiles")
	require.NoError(t, err)
	assert.Equal(t, "work-ghe", rem.Forge)

	// U094-F14 deleted the Registry.ResolveForge convenience wrapper (zero
	// production callers); this exercises the same resolution its body did,
	// against the registry state persisted above.
	rf := resolveForge(rem.URL, rem.Forge, reg.Forges())
	assert.Equal(t, ForgeGitHub, rf.Type)
	assert.Equal(t, "https://github.mycorp.com", rf.BaseURL)
	assert.Equal(t, "GHE_TOKEN", rf.TokenEnv)

	// A save must preserve both the forge label on the remote and the forges
	// block. Trigger a persist with a save-backed mutation (setting the default).
	require.NoError(t, reg.SetDefault("myprofiles"))

	reg2, err := NewRegistry(path)
	require.NoError(t, err)
	rem2, err := reg2.Get("myprofiles")
	require.NoError(t, err)
	assert.Equal(t, "work-ghe", rem2.Forge, "forge label survives save round-trip")
	assert.Equal(t, "myprofiles", reg2.GetDefault())

	forges := reg2.Forges()
	require.Contains(t, forges, "work-ghe")
	assert.Equal(t, "https://github.mycorp.com", forges["work-ghe"].BaseURL())
}

func TestRegistry_SetForge(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "remotes.yaml")

	reg, err := NewRegistry(path)
	require.NoError(t, err)
	require.NoError(t, reg.Add("corp", "https://git.example.com/corp/repo"))

	t.Run("binds a built-in forge and persists", func(t *testing.T) {
		require.NoError(t, reg.SetForge("corp", "git"))

		reg2, err := NewRegistry(path)
		require.NoError(t, err)
		rem, err := reg2.Get("corp")
		require.NoError(t, err)
		assert.Equal(t, "git", rem.Forge)
	})

	t.Run("rejects an unknown label", func(t *testing.T) {
		err := reg.SetForge("corp", "bogus")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown forge")
	})

	t.Run("empty label clears the binding", func(t *testing.T) {
		require.NoError(t, reg.SetForge("corp", ""))
		rem, err := reg.Get("corp")
		require.NoError(t, err)
		assert.Empty(t, rem.Forge)
	})
}

func TestRegistry_UnknownForgeType(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "remotes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"forges:\n"+
			"  bogus:\n"+
			"    type: subversion\n"), 0o644))

	_, err := NewRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type")
}

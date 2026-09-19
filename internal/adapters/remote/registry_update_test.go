package remote

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A remote is an address, and every field of it is editable in ONE call.
//
// Update exists as a single method rather than three setters because an edit
// that changes more than one field must not half-land: three saves can leave a
// remote renamed but still pointing at the old URL, with nothing to say which
// half applied. One lock, one save, one rollback.
func updateTestRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewRegistry(filepath.Join(t.TempDir(), "remotes.yaml"))
	require.NoError(t, err)
	require.NoError(t, reg.Add("corp", "https://git.example.com/corp/ctxloom"))
	return reg
}

func strptr(s string) *string { return &s }

func TestRegistryUpdate_ChangesURLAndPersists(t *testing.T) {
	reg := updateTestRegistry(t)

	got, err := reg.Update("corp", RemoteEdit{URL: strptr("https://git.example.com/corp/moved")})
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/corp/moved", got.URL)

	// Re-read from disk: an in-memory mutation that never saved would pass
	// every assertion above it.
	reloaded, err := NewRegistry(reg.configPath)
	require.NoError(t, err)
	rem, err := reloaded.Get("corp")
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/corp/moved", rem.URL)
}

func TestRegistryUpdate_RenamesAndCarriesTheDefaultPointer(t *testing.T) {
	reg := updateTestRegistry(t)
	require.NoError(t, reg.SetDefault("corp"))

	_, err := reg.Update("corp", RemoteEdit{Name: strptr("acme")})
	require.NoError(t, err)

	assert.False(t, reg.Has("corp"), "the old name is gone, not aliased")
	assert.True(t, reg.Has("acme"))
	// The default is stored BY NAME, so a rename that ignored it would leave
	// the pointer naming a remote that no longer exists.
	assert.Equal(t, "acme", reg.GetDefault(), "the default follows the rename")
}

// The other side of the pointer: a rename of some OTHER remote must not
// capture the default. Without this, a fix-up that unconditionally rewrote the
// pointer would pass the test above and be wrong.
func TestRegistryUpdate_RenameLeavesAnUnrelatedDefaultAlone(t *testing.T) {
	reg := updateTestRegistry(t)
	require.NoError(t, reg.Add("other", "https://git.example.com/other/ctxloom"))
	require.NoError(t, reg.SetDefault("other"))

	_, err := reg.Update("corp", RemoteEdit{Name: strptr("acme")})
	require.NoError(t, err)

	assert.Equal(t, "other", reg.GetDefault(), "an unrelated default is untouched")
}

func TestRegistryUpdate_RefusesACollidingName(t *testing.T) {
	reg := updateTestRegistry(t)
	require.NoError(t, reg.Add("taken", "https://git.example.com/taken/ctxloom"))

	_, err := reg.Update("corp", RemoteEdit{Name: strptr("taken")})
	require.Error(t, err)
	assert.True(t, reg.Has("corp"), "a refused rename leaves the original in place")
}

func TestRegistryUpdate_RefusesAURLAnotherRemoteAlreadyHas(t *testing.T) {
	reg := updateTestRegistry(t)
	require.NoError(t, reg.Add("other", "https://git.example.com/other/ctxloom"))

	_, err := reg.Update("corp", RemoteEdit{URL: strptr("https://git.example.com/other/ctxloom")})
	require.Error(t, err)

	rem, err := reg.Get("corp")
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/corp/ctxloom", rem.URL, "a refused edit changes nothing")
}

// The URL-collision check must exclude the remote being edited. Passing a
// remote its own current URL is ordinary — it happens on any edit that spells
// out every field, or that changes the forge and restates the address — and a
// check that only asked "does some remote have this URL" would refuse it,
// because one does: this one.
func TestRegistryUpdate_AcceptsARemotesOwnURL(t *testing.T) {
	reg := updateTestRegistry(t)

	got, err := reg.Update("corp", RemoteEdit{
		URL:   strptr("https://git.example.com/corp/ctxloom"),
		Forge: strptr("git"),
	})
	require.NoError(t, err, "a remote's own URL is not a collision with itself")
	assert.Equal(t, "git", got.Forge)
	assert.Equal(t, "https://git.example.com/corp/ctxloom", got.URL)
}

func TestRegistryUpdate_RefusesAnUnknownForge(t *testing.T) {
	reg := updateTestRegistry(t)

	_, err := reg.Update("corp", RemoteEdit{Forge: strptr("no-such-forge")})
	require.Error(t, err)
}

// An empty forge is a VALUE, not an omission: it means "resolve by URL host".
// A nil field is the omission. Conflating them makes the flag unable to
// express a reset back to host resolution.
func TestRegistryUpdate_EmptyForgeResetsToHostResolution(t *testing.T) {
	reg := updateTestRegistry(t)
	require.NoError(t, reg.SetForge("corp", "git"))

	got, err := reg.Update("corp", RemoteEdit{Forge: strptr("")})
	require.NoError(t, err)
	assert.Empty(t, got.Forge)

	// And a nil Forge leaves a set one alone.
	require.NoError(t, reg.SetForge("corp", "git"))
	got, err = reg.Update("corp", RemoteEdit{URL: strptr("https://git.example.com/corp/again")})
	require.NoError(t, err)
	assert.Equal(t, "git", got.Forge, "an omitted field is unchanged")
}

func TestRegistryUpdate_RefusesAnUnknownRemote(t *testing.T) {
	reg := updateTestRegistry(t)

	_, err := reg.Update("nope", RemoteEdit{URL: strptr("https://git.example.com/x/y")})
	require.Error(t, err)
}

// All three fields at once, which is the case the single-save design exists
// for: the result must be wholly applied, from disk.
func TestRegistryUpdate_AppliesEveryFieldAtOnce(t *testing.T) {
	reg := updateTestRegistry(t)
	require.NoError(t, reg.SetDefault("corp"))

	_, err := reg.Update("corp", RemoteEdit{
		Name:  strptr("acme"),
		URL:   strptr("https://github.com/acme/ctxloom"),
		Forge: strptr("github"),
	})
	require.NoError(t, err)

	reloaded, err := NewRegistry(reg.configPath)
	require.NoError(t, err)
	rem, err := reloaded.Get("acme")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/acme/ctxloom", rem.URL)
	assert.Equal(t, "github", rem.Forge)
	assert.Equal(t, "acme", reloaded.GetDefault())
	assert.False(t, reloaded.Has("corp"))
}

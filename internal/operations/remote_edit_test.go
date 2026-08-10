package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

func editRemoteRegistry(t *testing.T) *remote.Registry {
	t.Helper()
	reg, err := remote.NewRegistry(filepath.Join(t.TempDir(), "remotes.yaml"))
	require.NoError(t, err)
	require.NoError(t, reg.Add("corp", "https://git.example.com/corp/ctxloom"))
	return reg
}

func sp(s string) *string { return &s }

// Changed is the operation's whole reason for reporting rather than returning
// bare success: an edit that names a field it did not touch, or stays silent
// about one it did, is the shape this project keeps finding — exit 0 over an
// effect nobody can see.
func TestEditRemote_ReportsOnlyTheFieldsThatChanged(t *testing.T) {
	reg := editRemoteRegistry(t)

	res, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		URL:      sp("https://git.example.com/corp/moved"),
		Registry: reg,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"url"}, res.Changed)
	assert.Equal(t, "https://git.example.com/corp/ctxloom", res.Before.URL)
	assert.Equal(t, "https://git.example.com/corp/moved", res.After.URL)
	assert.False(t, res.DefaultPointerUpdated)
}

func TestEditRemote_NoOpEditChangesNothingAndSaysSo(t *testing.T) {
	reg := editRemoteRegistry(t)

	res, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		URL:      sp("https://git.example.com/corp/ctxloom"),
		Registry: reg,
	})
	require.NoError(t, err)
	assert.Empty(t, res.Changed, "restating a value is not a change")
}

func TestEditRemote_RenameReportsTheDefaultPointerMove(t *testing.T) {
	reg := editRemoteRegistry(t)
	require.NoError(t, reg.SetDefault("corp"))

	res, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		NewName:  sp("acme"),
		Registry: reg,
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"name"}, res.Changed)
	assert.Equal(t, "acme", res.Name)
	assert.True(t, res.DefaultPointerUpdated,
		"a silent default move is a change to which remote every bare command uses")
	assert.Equal(t, "acme", reg.GetDefault())
}

// Editing the default remote WITHOUT renaming it moves no pointer. The
// pointer is keyed by name, so only a rename can disturb it — reporting a move
// for a URL or forge change would tell a reader their default had shifted when
// it had not.
func TestEditRemote_EditingTheDefaultWithoutRenamingMovesNoPointer(t *testing.T) {
	reg := editRemoteRegistry(t)
	require.NoError(t, reg.SetDefault("corp"))

	res, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		URL:      sp("https://git.example.com/corp/moved"),
		Registry: reg,
	})
	require.NoError(t, err)

	assert.False(t, res.DefaultPointerUpdated, "no rename, no pointer move")
	assert.Equal(t, "corp", reg.GetDefault())
}

func TestEditRemote_RenameOfANonDefaultReportsNoPointerMove(t *testing.T) {
	reg := editRemoteRegistry(t)
	require.NoError(t, reg.Add("other", "https://git.example.com/other/ctxloom"))
	require.NoError(t, reg.SetDefault("other"))

	res, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		NewName:  sp("acme"),
		Registry: reg,
	})
	require.NoError(t, err)
	assert.False(t, res.DefaultPointerUpdated)
	assert.Equal(t, "other", reg.GetDefault())
}

func TestEditRemote_RequiresAName(t *testing.T) {
	_, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Registry: editRemoteRegistry(t),
		URL:      sp("https://git.example.com/x/y"),
	})
	require.Error(t, err)
}

// Nothing to do is a refusal, not a success. An edit with no fields set would
// otherwise report exit 0 over an untouched remote.
func TestEditRemote_RefusesAnEditThatAsksForNothing(t *testing.T) {
	_, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		Registry: editRemoteRegistry(t),
	})
	require.Error(t, err)
}

func TestEditRemote_PropagatesARefusal(t *testing.T) {
	reg := editRemoteRegistry(t)
	require.NoError(t, reg.Add("taken", "https://git.example.com/taken/ctxloom"))

	_, err := EditRemote(context.Background(), nil, EditRemoteRequest{
		Name:     "corp",
		NewName:  sp("taken"),
		Registry: reg,
	})
	require.Error(t, err)
	assert.True(t, reg.Has("corp"))
}

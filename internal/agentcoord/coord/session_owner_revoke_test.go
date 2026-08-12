package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RevokeSessionOwner had ZERO call sites anywhere in the repo
// (production or test), so depth-0 session-owner credentials — minted once
// per `ctxloom run`/`ctxloom acp` process by SessionOwnerEnv
// (internal/mcp/coord_host.go) — were never revoked. Since runsFold.apply
// re-applies every factSessionCred fact on replay/adoption, every owner
// token ever minted for a project stayed valid forever in that project's
// coordinator state, contradicting doc.go's own "revocation at run end
// severs the credential's streams and parked polls" claim (which held for
// run credentials, via factRunEnded, but never for this one).
//
// This pins the mechanism itself, previously completely unexercised: a
// revoked session-owner token must stop identifying, and a re-registration
// of the same harp (a fresh `ctxloom run` on the same project) must mint an
// INDEPENDENT credential — the fix is wired into internal/cli/run.go's
// coordinator teardown (defer sessionCoord.RevokeSessionOwner(ownerToken),
// ordered to fire before the coordinator Close so the journal is still
// open to accept the write).
func TestRevokeSessionOwner_TokenStopsIdentifying(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const harp = "owner-to-revoke"
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)

	id, ok := c.Identify(token)
	require.True(t, ok, "precondition: a freshly minted owner token must identify")
	assert.Equal(t, 0, id.Depth, "precondition: a session owner is depth 0")

	c.RevokeSessionOwner(token)

	_, ok = c.Identify(token)
	assert.False(t, ok, "a revoked session-owner token must stop identifying — this is the guarantee "+
		"doc.go claims for every credential, and RevokeSessionOwner existing with no caller made it false "+
		"for this one specifically")

	// A later process (a fresh `ctxloom run` on the same project/harp)
	// mints an independent credential — revoking the OLD one must not
	// disturb the NEW one.
	token2, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)
	_, ok = c.Identify(token2)
	assert.True(t, ok, "a fresh registration for the same harp after a prior revocation must still identify")
}

// TestRevokeSessionOwner_UnknownTokenIsANoOp: revoking a token this
// coordinator never minted (or already revoked) must not error or panic —
// the CLI teardown path calls this unconditionally on the way out.
func TestRevokeSessionOwner_UnknownTokenIsANoOp(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	assert.NotPanics(t, func() { c.RevokeSessionOwner("not-a-real-token") })
	assert.NotPanics(t, func() { c.RevokeSessionOwner("") })
}

// TestRevokeSessionOwner_RevocationSurvivesAdoption is the DURABILITY half, and
// the whole reason revocation is a journal fact rather than a map delete: the
// registry fold re-applies every factSessionCred on replay, so a credential a
// dead process revoked in memory would come back valid the moment a fresh
// coordinator adopted that project's state. The revoked-arm's job is to be
// replayed too, and it must delete only the hash it names.
func TestRevokeSessionOwner_RevocationSurvivesAdoption(t *testing.T) {
	stateDir := t.TempDir()
	projectDir := t.TempDir()

	first, err := New(Options{ProjectDir: projectDir, StateDir: stateDir, Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)

	revoked, err := first.RegisterSessionOwner("owner-gone")
	require.NoError(t, err)
	kept, err := first.RegisterSessionOwner("owner-still-here")
	require.NoError(t, err)
	first.RevokeSessionOwner(revoked)
	first.Close()

	// A fresh process adopting the same project's journals.
	second, err := New(Options{ProjectDir: projectDir, StateDir: stateDir, Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)
	t.Cleanup(second.Close)

	_, ok := second.Identify(revoked)
	assert.False(t, ok, "a revoked owner credential must stay revoked across adoption; replaying the mint without the revocation would resurrect it")

	id, ok := second.Identify(kept)
	assert.True(t, ok, "revoking one owner credential must not disturb another minted in the same process")
	assert.Equal(t, "owner-still-here", id.Harp)
	assert.Equal(t, 0, id.Depth)
}

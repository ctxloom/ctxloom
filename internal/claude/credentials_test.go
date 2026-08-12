package claude

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realCredentialJSON is the live .credentials.json shape, probe-verified
// 2026-08-12 against a subscription login: every token field nests under
// claudeAiOauth. accessToken/expiresAt/scopes/subscriptionType/rateLimitTier
// authenticate a run; refreshToken/refreshTokenExpiresAt are the single-use
// rotating half that must NEVER reach a disposable copy.
const realCredentialJSON = `{"claudeAiOauth":{` +
	`"accessToken":"acc-123",` +
	`"refreshToken":"ref-should-be-stripped",` +
	`"expiresAt":1760000000000,` +
	`"refreshTokenExpiresAt":1790000000000,` +
	`"scopes":["user:inference","user:profile"],` +
	`"subscriptionType":"max",` +
	`"rateLimitTier":"default"}}`

// oauthOf parses projected bytes and returns the claudeAiOauth object.
func oauthOf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(b, &cfg), "the projected credential must still be valid JSON")
	oauth, ok := cfg["claudeAiOauth"].(map[string]any)
	require.True(t, ok, "the projected credential must keep the claudeAiOauth object")
	return oauth
}

// TestProjectAmbientCredential_StripsRefreshKeepsAccess is the load-bearing
// assertion of the whole ruling: claude's ambient credential copy is
// ACCESS-TOKEN-ONLY. The refresh token and its expiry are gone; everything that
// authenticates a run stays.
//
// MUTATION TARGET (m1): remove refreshToken from strippedCredentialFields (or
// no-op stripCredentialFields) and this goes red — the copy would carry the
// single-use token a refresh could rotate the host's login with.
func TestProjectAmbientCredential_StripsRefreshKeepsAccess(t *testing.T) {
	out, err := NewCredentialProjector().ProjectAmbientCredential(CredentialsFileName, []byte(realCredentialJSON))
	require.NoError(t, err)

	oauth := oauthOf(t, out)
	assert.NotContains(t, oauth, "refreshToken", "the single-use refresh token must be stripped from the copy")
	assert.NotContains(t, oauth, "refreshTokenExpiresAt", "the refresh-token expiry rides out with the token it describes")

	// The access-token half is preserved verbatim, so the copy authenticates.
	assert.Equal(t, "acc-123", oauth["accessToken"])
	assert.EqualValues(t, 1760000000000, oauth["expiresAt"])
	assert.Equal(t, []any{"user:inference", "user:profile"}, oauth["scopes"])
	assert.Equal(t, "max", oauth["subscriptionType"])
	assert.Equal(t, "default", oauth["rateLimitTier"])

	// The projection is a STRICT SUBSET: no key the copy carries is absent from
	// the host, and the only host keys missing are the two stripped ones.
	var host map[string]any
	require.NoError(t, json.Unmarshal([]byte(realCredentialJSON), &host))
	hostOAuth := host["claudeAiOauth"].(map[string]any)
	for k := range oauth {
		assert.Contains(t, hostOAuth, k, "the copy invents no key the host credential lacks")
	}
}

// TestProjectAmbientCredential_TopLevelRefreshAlsoStripped guards the defensive
// flattened-schema pass: even if a future claude moved the fields to the top
// level, the strip still catches them.
func TestProjectAmbientCredential_TopLevelRefreshAlsoStripped(t *testing.T) {
	flat := `{"accessToken":"acc","refreshToken":"ref","refreshTokenExpiresAt":1}`
	out, err := NewCredentialProjector().ProjectAmbientCredential(CredentialsFileName, []byte(flat))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(out, &cfg))
	assert.NotContains(t, cfg, "refreshToken")
	assert.NotContains(t, cfg, "refreshTokenExpiresAt")
	assert.Equal(t, "acc", cfg["accessToken"])
}

// TestProjectAmbientCredential_UnparseableFailsLoud: a credential the projector
// cannot parse is one it cannot prove is refresh-token-free. Copying it would
// reopen the host-invalidation hole, so the projection fails rather than
// passing the bytes through.
func TestProjectAmbientCredential_UnparseableFailsLoud(t *testing.T) {
	_, err := NewCredentialProjector().ProjectAmbientCredential(CredentialsFileName, []byte("not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot parse")
}

// TestProjectAmbientCredential_NonCredentialFilePassesThrough: a projector keyed
// on the credential leaf leaves any other ambient file byte-for-byte, so adding
// a second claude ambient file later cannot be silently mangled by this strip.
func TestProjectAmbientCredential_NonCredentialFilePassesThrough(t *testing.T) {
	body := []byte(`{"refreshToken":"kept because this is not the credential file"}`)
	out, err := NewCredentialProjector().ProjectAmbientCredential(".claude.json", body)
	require.NoError(t, err)
	assert.Equal(t, body, out, "only the credential leaf is projected; other files pass through")
}

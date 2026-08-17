package claude

import (
	"encoding/json"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CredentialsFileName (declared in enginecli.go) is claude's OAuth credential
// file leaf — the destName the projector below keys on.

// oauthObjectKey is the single top-level object claude wraps its OAuth
// subscription credential in inside .credentials.json — probe-verified
// against a live subscription login: the file is
// `{"claudeAiOauth":{...}}` and every token field lives under it.
const oauthObjectKey = "claudeAiOauth"

// strippedCredentialFields are the fields elided from claude's ambient
// credential copy. They are the REFRESH half of the OAuth token, and nothing
// else: accessToken, expiresAt, scopes, subscriptionType and rateLimitTier all
// stay, so the copy authenticates for the life of its access token.
//
// WHY THE STRIP EXISTS (easiest-stomp, after a live
// experiment): claude's refresh token is SINGLE-USE and ROTATING — the moment
// any holder refreshes, the old refresh token is invalidated for EVERY other
// holder, including the human's own host login. A copied instance home that
// carried the refresh token and outlived its access token would refresh, and
// that refresh would silently log the human out of their real ~/.claude. So a
// disposable instance is handed an ACCESS-TOKEN-ONLY credential: it can
// authenticate, and when its access token expires it fails loud (re-launch to
// get a fresh one) rather than rotating — and destroying — the host's.
var strippedCredentialFields = []string{"refreshToken", "refreshTokenExpiresAt"}

// NewCredentialProjector constructs claude's ambient-credential projector — the
// engine-owned strip of the refresh token from a copy of ~/.claude/.credentials.json.
// It is claude's half of the engine write-config directive for the credential
// file, the sibling of NewInstanceConfigWriter's half for .claude.json:
// isolation.CopyAmbient decides that claude's credential crosses at all, this
// owns which bytes of it do.
func NewCredentialProjector() agent.CredentialProjector { return credentialProjector{} }

// credentialProjector implements agent.CredentialProjector for claude-code.
type credentialProjector struct{}

var _ agent.CredentialProjector = credentialProjector{}

// ProjectAmbientCredential strips the refresh-token fields from a copy of
// claude's .credentials.json. Any OTHER ambient file claude might grow later
// passes through untouched (there is none today; the guard keeps this correct
// if one is added). An unparseable credential is a LOUD FAILURE, never a
// passthrough: a file this projector cannot parse is one it cannot prove is
// refresh-token-free, and copying it would reopen exactly the host-invalidation
// hole the strip exists to close.
func (credentialProjector) ProjectAmbientCredential(destName string, hostBytes []byte) ([]byte, error) {
	if destName != CredentialsFileName {
		return hostBytes, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(hostBytes, &cfg); err != nil {
		return nil, fmt.Errorf(
			"claude credential projection: cannot parse %s to strip the refresh token; refusing to copy a credential that cannot be sanitized (a refresh in a copied home rotates and invalidates the host login): %w",
			destName, err)
	}
	// The live schema nests the token under claudeAiOauth; a defensive
	// top-level pass covers a future flattened schema without waiting for a
	// re-probe to catch it. Deleting an absent key is a no-op.
	stripCredentialFields(cfg)
	if oauth, ok := cfg[oauthObjectKey].(map[string]any); ok {
		stripCredentialFields(oauth)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("claude credential projection: re-encode %s: %w", destName, err)
	}
	return out, nil
}

// stripCredentialFields deletes each strippedCredentialFields key from obj.
func stripCredentialFields(obj map[string]any) {
	for _, k := range strippedCredentialFields {
		delete(obj, k)
	}
}

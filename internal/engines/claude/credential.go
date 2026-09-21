package claude

import (
	"encoding/json"
	"fmt"
)

// The credential's projection into a session home.
//
// claude stores its OAuth grant under the `claudeAiOauth` object of
// .credentials.json (on Linux/Windows the file; on macOS the same JSON as a
// Keychain item). Two of its fields are the REFRESH half — the single-use,
// rotating grant — and the seed withholds exactly those, as claude's own
// session-seeding path does (2.1.278). Every other field crosses: the
// access token, its expiry, the scopes, the subscription fields claude
// reads to pick its rate-limit tier.

// oauthKey is the object claude keeps its OAuth grant under.
const oauthKey = "claudeAiOauth"

// refreshFields are the fields of the OAuth object a seeded copy never
// carries. Named by claude's own schema; a copy holding them can refresh,
// and a refresh from a copy revokes the host's login.
var refreshFields = []string{"refreshToken", "refreshTokenExpiresAt"}

// projectCredential is claude's engine.SeedFile.Project: the host's
// credential bytes with the refresh half of the OAuth grant removed. A host
// file that is not a JSON object is refused rather than passed through — a
// pass-through would seed the instance with something the projection never
// inspected, which is how a refresh token crosses by accident.
func projectCredential(host []byte) ([]byte, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(host, &cfg); err != nil {
		return nil, fmt.Errorf("claude credential: not a JSON object: %w", err)
	}
	raw, ok := cfg[oauthKey]
	if !ok {
		// An API-key credential (no OAuth grant): nothing to withhold.
		return json.Marshal(cfg)
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(raw, &oauth); err != nil {
		return nil, fmt.Errorf("claude credential: %s is not a JSON object: %w", oauthKey, err)
	}
	for _, f := range refreshFields {
		delete(oauth, f)
	}
	projected, err := json.Marshal(oauth)
	if err != nil {
		return nil, fmt.Errorf("claude credential: encode %s: %w", oauthKey, err)
	}
	cfg[oauthKey] = projected
	return json.Marshal(cfg)
}

package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A forge is matched to a remote by host, and the host must be spelled the way
// repository IDENTITY spells it (refuri.CanonicalHost): otherwise a remote that
// keys as one repository binds to a different forge — a different endpoint and
// a different token env — depending only on how its host was written.
func TestResolveForge_HostCanonicalization(t *testing.T) {
	ghe := func(base string) map[string]ForgeConfig {
		return MergeForges(map[string]ForgeConfig{
			"work-ghe": {Type: "github", Body: map[string]any{"base_url": base, "token_env": "GHE_TOKEN"}},
		})
	}
	tests := []struct {
		name      string
		remote    string
		base      string
		wantType  ForgeType
		wantToken string
	}{
		{"U-label remote, A-label forge", "https://bücher.example/o/r", "https://xn--bcher-kva.example", ForgeGitHub, "GHE_TOKEN"},
		{"A-label remote, U-label forge", "https://xn--bcher-kva.example/o/r", "https://BÜCHER.example", ForgeGitHub, "GHE_TOKEN"},
		{"explicit default port, portless forge", "https://ghe.example:443/o/r", "https://ghe.example", ForgeGitHub, "GHE_TOKEN"},
		{"portless remote, forge with default port", "https://ghe.example/o/r", "https://ghe.example:443", ForgeGitHub, "GHE_TOKEN"},
		// A host identity refuses names no repository: it must bind to no
		// configured forge, even one configured with the identical spelling.
		{"refused host matches nothing", "https://bad_host.example/o/r", "https://bad_host.example", ForgeGitGeneric, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rf := resolveForge(tt.remote, "", ghe(tt.base))
			assert.Equal(t, tt.wantType, rf.Type)
			assert.Equal(t, tt.wantToken, rf.TokenEnv)
		})
	}
}

func TestDetectForge_HostCanonicalization(t *testing.T) {
	// Full-width letters map to "github.com" under the IDNA lookup profile —
	// the same repository identity github.com has — so the same adapter.
	forge, base, err := DetectForge("https://ｇｉｔｈｕｂ.com/o/r")
	require.NoError(t, err)
	assert.Equal(t, ForgeGitHub, forge)
	assert.Equal(t, "https://github.com", base)

	for _, raw := range []string{"https://bad_host.example/o/r", "git@bad_host.example:o/r.git", "bad_host.example/o/r"} {
		_, _, err := DetectForge(raw)
		assert.Error(t, err, raw)
	}
}

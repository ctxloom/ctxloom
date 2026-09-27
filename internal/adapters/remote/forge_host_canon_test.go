package remote

import (
	"bytes"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"

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
		{"ssh default port, portless https forge", "ssh://git@ghe.example:22/o/r", "https://ghe.example", ForgeGitHub, "GHE_TOKEN"},
		{"forge on 8443, remote on 8443", "https://ghe.example:8443/o/r", "https://ghe.example:8443", ForgeGitHub, "GHE_TOKEN"},
		// A different port is a different server (RFC 3986 §6.2.3): it must not
		// be handed this forge's endpoint or token env.
		{"remote on 8443, portless forge", "https://ghe.example:8443/o/r", "https://ghe.example", ForgeGitGeneric, ""},
		{"portless remote, forge on 8443", "https://ghe.example/o/r", "https://ghe.example:8443", ForgeGitGeneric, ""},
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

// A remote whose host names a configured forge but whose port does not is
// resolved as generic git — and says so, since the missing token env would
// otherwise surface only as an opaque auth failure much later.
func TestResolveForge_PortMismatchWarns(t *testing.T) {
	clidiag.ResetWarnOnce()
	var sink bytes.Buffer
	defer clidiag.SetSink(&sink)()
	forges := MergeForges(map[string]ForgeConfig{
		"work-ghe": {Type: "github", Body: map[string]any{"base_url": "https://ghe.example", "token_env": "GHE_TOKEN"}},
	})

	rf := resolveForge("https://ghe.example:8443/o/r", "", forges)
	assert.Equal(t, ForgeGitGeneric, rf.Type)
	assert.Contains(t, sink.String(), "work-ghe")
	assert.Contains(t, sink.String(), "8443")

	sink.Reset()
	resolveForge("https://ghe.example:443/o/r", "", forges)
	resolveForge("https://other.example:8443/o/r", "", forges)
	assert.Empty(t, sink.String(), "a match, or a host no forge names, is not a port mismatch")
}

// The credential consequence of port-aware matching, at the layer that spends
// it: a port other than the scheme's default is another server, and neither
// the ambient github.com token nor a forge's named token_env goes to it.
func TestRepoCache_cloneToken_PortIsPartOfTheServer(t *testing.T) {
	t.Setenv(DefaultGitHubTokenEnv, "pat-for-github-dot-com")
	t.Setenv("CORP_TOKEN", "corp-scoped-token")
	forges := MergeForges(map[string]ForgeConfig{"corp": {Type: string(ForgeGitHub), Body: map[string]any{
		"base_url": "https://github.corp.example", "token_env": "CORP_TOKEN",
	}}})
	cache := NewRepoCache("", AuthConfig{GitHub: "pat-for-github-dot-com"},
		WithForgeResolver(func(u string) ResolvedForge { return ResolveForgeForURLWith(u, "", forges) }))
	defer clidiag.SetSink(&bytes.Buffer{})()

	assert.Empty(t, cache.cloneToken("https://github.com:8443/o/r.git"), "github.com's token is for github.com:443")
	assert.Equal(t, "pat-for-github-dot-com", cache.cloneToken("https://github.com:443/o/r.git"))
	assert.Empty(t, cache.cloneToken("https://github.corp.example:8443/o/r.git"), "corp's token_env is for its own port")
	assert.Equal(t, "corp-scoped-token", cache.cloneToken("https://github.corp.example/o/r.git"))

	// With no forge resolver, isGitHubDotCom alone decides.
	bare := NewRepoCache("", AuthConfig{GitHub: "pat-for-github-dot-com"})
	assert.Empty(t, bare.cloneToken("https://github.com:8443/o/r.git"), "github.com's token is for github.com:443")
	assert.Equal(t, "pat-for-github-dot-com", bare.cloneToken("https://github.com/o/r.git"))
}

func TestDetectForge_PortIsPartOfTheServer(t *testing.T) {
	forge, _, err := DetectForge("https://github.com:8443/o/r")
	require.NoError(t, err)
	assert.Equal(t, ForgeGitGeneric, forge, "github.com on another port is not the GitHub API")

	forge, _, err = DetectForge("https://github.com:443/o/r")
	require.NoError(t, err)
	assert.Equal(t, ForgeGitHub, forge)
}

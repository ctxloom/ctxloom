package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
)

// ambiguousDiscoverer wires a Discoverer to an in-memory keyring holding two
// named identities, with nothing to narrow the choice — the shape that makes
// agentkey return *AmbiguousKeyError.
func ambiguousDiscoverer(t *testing.T) *agentkey.Discoverer {
	t.Helper()
	kr := agent.NewKeyring()
	for _, comment := range []string{"ben@abbitt.me", "release-bot@example.com"} {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv, Comment: comment}))
	}
	return &agentkey.Discoverer{
		GitConfig: func(context.Context, string, string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return kr, nil },
	}
}

// TestResolveReviewSigner_AmbiguousListingMatchesAgentKey is a PARITY pin
// across the two places the ambiguous-key candidate listing is authored:
// agentkey.AmbiguousKeyError.Error() and resolveReviewSigner's own rendering.
//
// It sits at the public seam ABOVE the duplication on purpose (template
// §11o/§4): it is unchanged by whatever canonical form the two collapse into,
// and it is red only if the two ever describe the same candidates differently.
//
// The divergence it caught: review dropped the agent COMMENT from
// every candidate line, printing only fingerprint and key type. The comment is
// the whole point of the listing — agentkey's own code says so, "it's what a
// human actually recognizes" — and it is what the user must then type to
// choose. Review showed a reviewer two indistinguishable ed25519 keys and
// asked them to pick.
func TestResolveReviewSigner_AmbiguousListingMatchesAgentKey(t *testing.T) {
	d := ambiguousDiscoverer(t)

	_, agentErr := d.Discover(context.Background(), "")
	var ambiguous *agentkey.AmbiguousKeyError
	require.ErrorAs(t, agentErr, &ambiguous, "fixture must actually be ambiguous")
	require.Len(t, ambiguous.Candidates, 2)

	var unsigned bool
	stderr := captureStderr(t, func() {
		var err error
		_, unsigned, err = resolveReviewSigner(context.Background(), d, "", false)
		require.NoError(t, err)
	})
	require.True(t, unsigned, "an ambiguous chain still degrades review to the unsigned path")

	canonical := agentErr.Error()

	for _, c := range ambiguous.Candidates {
		assert.Contains(t, stderr, c.Fingerprint, "review must name every candidate's fingerprint")
		assert.Contains(t, canonical, c.Fingerprint)

		require.NotEmpty(t, c.Comment, "fixture must give every identity a comment")
		assert.Contains(t, canonical, c.Comment,
			"agentkey's listing names the comment — that is the identifying part")
		assert.Contains(t, stderr, c.Comment,
			"review's listing must name it too: two ed25519 keys are indistinguishable without it")
	}

	// Whatever else each surface adds, the header must not drift apart —
	// it is the same event being described.
	header := "multiple keys in ssh-agent"
	assert.Contains(t, strings.ToLower(canonical), header)
	assert.Contains(t, strings.ToLower(stderr), header)
}

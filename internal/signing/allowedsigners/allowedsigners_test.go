package allowedsigners_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// TestExample_ParseAndAskIsThisKeyTrustedForThisNamespace is the front-door
// usage example: parse an allowed_signers file, then ask the one question
// this package exists to answer.
func TestExample_ParseAndAskIsThisKeyTrustedForThisNamespace(t *testing.T) {
	const file = `# example allowed_signers, per ssh-keygen(1)
releases@ctxloom.dev namespaces="publish.v1.ctxloom.dev" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGO+4UzAG5fNzbf+DqeceZ4ZtCIXMIStJzWMI6PG/CVJ ctxloom release key
`
	store, parseErrs, err := allowedsigners.Parse(strings.NewReader(file))
	require.NoError(t, err)
	require.Empty(t, parseErrs)

	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGO+4UzAG5fNzbf+DqeceZ4ZtCIXMIStJzWMI6PG/CVJ"))
	require.NoError(t, err)

	decision := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", time.Now())
	assert.True(t, decision.Trusted)
	assert.Equal(t, "releases@ctxloom.dev", decision.Principal)

	// The same key was never granted the approve namespace — this is the
	// role system doing its job.
	assert.False(t, store.TrustedForNamespace(key, "approve.v1.ctxloom.dev", time.Now()).Trusted)
}

// TestExample_UnionOfEmbeddedUserAndProjectStores demonstrates the
// three-location precedence model from the signature-envelope spec §7:
// embedded defaults, user store, and project store are all unioned — a
// key counts for whatever namespaces it lists in ANY of them.
func TestExample_UnionOfEmbeddedUserAndProjectStores(t *testing.T) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGO+4UzAG5fNzbf+DqeceZ4ZtCIXMIStJzWMI6PG/CVJ"))
	require.NoError(t, err)

	embedded := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"bundles@ctxloom.dev"},
		Namespaces: []string{"publish.v1.ctxloom.dev"},
		PublicKey:  key,
	})
	project := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"lead@team.example"},
		Namespaces: []string{"approve.v1.ctxloom.dev"},
		PublicKey:  key,
	})

	effective := allowedsigners.Union(embedded, project)
	assert.True(t, effective.TrustedForNamespace(key, "publish.v1.ctxloom.dev", time.Now()).Trusted)
	assert.True(t, effective.TrustedForNamespace(key, "approve.v1.ctxloom.dev", time.Now()).Trusted)
}

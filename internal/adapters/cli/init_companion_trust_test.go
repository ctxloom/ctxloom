package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSeedCompanionTrust_WritesTheGrantIntoAStoreThatCanRevokeIt: init
// authorizes the companions ctxloom ships by WRITING the grant, not by relying
// on one compiled into the binary.
//
// The placement is the subject. A companion grant in the embedded root would be
// unrevokable — that store is a union member nothing can remove — so a project
// could never withdraw permission for a binary to execute. Written to a store
// on disk, deleting the line withdraws it, and that is the property here.
//
// The path is taken from the call's own report rather than rebuilt by joining,
// so the test cannot pass by checking a file production never wrote to.
func TestSeedCompanionTrust_WritesTheGrantIntoAStoreThatCanRevokeIt(t *testing.T) {
	testsupport.ProjectDir(t)

	store := seedCompanionTrust(true)
	require.NotEmpty(t, store, "init must write the grant rather than depend on the embedded root")
	require.FileExists(t, store)

	body, err := os.ReadFile(store)
	require.NoError(t, err)
	assert.Contains(t, string(body), signing.NamespaceCompanion,
		"the written grant must carry the namespace companion admission actually consults")
	assert.NotContains(t, string(body), signing.NamespaceApprove,
		"init must never widen the release key to approve on the user's behalf")
	assert.NotContains(t, string(body), signing.NamespaceReject,
		"init must never widen the release key to reject on the user's behalf")

	// The grant must DECIDE the question, not merely appear in a file: a
	// well-formed line naming the wrong key or namespace satisfies a substring
	// check and still admits nothing.
	cfg, err := GetConfig()
	require.NoError(t, err)
	embedded := configload.EmbeddedSigners().Entries()
	require.NotEmpty(t, embedded, "the embedded root must ship a key for this to mean anything")
	assert.True(t,
		cfg.Trust().Root().TrustedForNamespace(embedded[0].PublicKey, signing.NamespaceCompanion, time.Now()).Trusted,
		"after seeding, the trust root must authorize ctxloom's release key to sign the companions it ships")
}

// TestSeedCompanionTrust_IsIdempotent: init deliberately runs on PRE-EXISTING
// projects and AddSigner APPENDS, so without the trust check ahead of it every
// re-init would leave another copy of the same entry behind.
//
// The second call must report that it wrote NOTHING — a stronger statement than
// "the file did not change", which a write of identical bytes would also
// satisfy.
func TestSeedCompanionTrust_IsIdempotent(t *testing.T) {
	testsupport.ProjectDir(t)

	store := seedCompanionTrust(true)
	require.NotEmpty(t, store)

	assert.Empty(t, seedCompanionTrust(true),
		"a re-init over an already-authorized project must write nothing at all")

	body, err := os.ReadFile(store)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(body), signing.NamespaceCompanion),
		"exactly one companion grant, however many times init runs")
}

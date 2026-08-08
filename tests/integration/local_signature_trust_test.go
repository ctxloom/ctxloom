//go:build integration

package integration

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Local content is trusted BY VIRTUE OF BEING LOCAL: a bundle the project
// authors in its own .ctxloom/content/ tree resolves and delivers whatever its
// detached publisher signature says — absent, valid, stale, or structurally
// garbage. A signature on local content is METADATA for the day it is promoted
// (bundle push / bundle move), never a gate on delivery.
//
// These tests exist because the opposite would be silent: a stale .sig that
// withheld local content would turn authored context into NO context with no
// diagnostic, which is this codebase's characteristic failure mode. Every
// assertion below is on the DELIVERED PAYLOAD (what the model actually
// received), never on an exit code or a success line — a withhold exits 0.
//
// They deliberately do NOT weaken remote trust: everything here is
// ctxloom:local content in the project's own tree. The remote counterpart —
// signed bytes fetched from another repo, where a trusted key over
// non-matching bytes IS a tamper alarm — is J001500's, and is untouched.

const localSigFragmentBody = "LOCAL-SIG-CHARACTERIZATION-PAYLOAD"

// localSigBundlePath is the authored (committed content) bundle the harness's
// writeFragment helper writes to.
const localSigBundlePath = ".ctxloom/content/bundles/local.yaml"

// deliverLocalFragment runs the fragment through a real assembly and returns
// what the language model was actually handed.
func deliverLocalFragment(t *testing.T, env *testenv.TestEnvironment, mockLM *testenv.MockLM, fragment string) string {
	t.Helper()
	_ = env.Run("run", "-f", fragment, "--print", "delivery probe")
	recorded, err := mockLM.GetRecordedInput()
	require.NoError(t, err, "mock LM recorded no input at all (command output: %s)", env.LastOutput())
	return recorded
}

// setupLocalSigEnv builds a project holding one local bundle with one fragment
// and returns the environment plus the mock LM that records what is delivered.
func setupLocalSigEnv(t *testing.T) (*testenv.TestEnvironment, *testenv.MockLM) {
	t.Helper()
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("OK"))
	writeFragment(t, env, "signed-local", []string{"local"}, localSigFragmentBody)
	return env, mockLM
}

// signLocalBundle signs the bundle file's exact current bytes and writes the
// detached sibling, exactly as `ctxloom bundle sign` does.
func signLocalBundle(t *testing.T, env *testenv.TestEnvironment, signer *testenv.TestSigner) {
	t.Helper()
	body, err := env.ReadFile(localSigBundlePath)
	require.NoError(t, err)
	sig, err := signing.Sign([]byte(body), signer.Signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, env.WriteFile(localSigBundlePath+".sig", string(sig)))
}

// editAfterSigning rewrites the bundle's bytes WITHOUT touching the sibling
// .sig, which is exactly what "edited and forgot to re-sign" looks like on
// disk: a signature over bytes that no longer exist.
func editAfterSigning(t *testing.T, env *testenv.TestEnvironment) {
	t.Helper()
	body, err := env.ReadFile(localSigBundlePath)
	require.NoError(t, err)
	require.NoError(t, env.WriteFile(localSigBundlePath, body+"\n# edited after signing, never re-signed\n"))
}

// TestLocalBundle_NoSignature_Delivers is the baseline: unsigned local content
// is delivered, with no trust warning. This is the state the ctxloom-project
// bundle was deliberately reduced to (commit 2775db45) and is the regression
// witness for the whole rule.
func TestLocalBundle_NoSignature_Delivers(t *testing.T) {
	env, mockLM := setupLocalSigEnv(t)
	require.False(t, env.FileExists(localSigBundlePath+".sig"), "precondition: no signature on disk")

	delivered := deliverLocalFragment(t, env, mockLM, "signed-local")

	assert.Contains(t, delivered, localSigFragmentBody, "unsigned local content must be delivered")
	assert.NotContains(t, env.LastOutput(), "withheld", "unsigned local content must not be reported as withheld")
}

// TestLocalBundle_ValidSignature_Delivers: a signature that still covers the
// bytes changes nothing about delivery, whether or not the signing key is
// trusted for publishing. A local bundle is not allowed BECAUSE it is signed;
// it is allowed because it is local.
func TestLocalBundle_ValidSignature_Delivers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
	}{
		{"untrusted key", false},
		{"trusted publisher key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, mockLM := setupLocalSigEnv(t)
			signer, err := testenv.GenerateTestSigner()
			require.NoError(t, err)
			if tc.trusted {
				require.NoError(t, env.TrustSigner(signer, "author@example.test", true))
			}
			signLocalBundle(t, env, signer)

			delivered := deliverLocalFragment(t, env, mockLM, "signed-local")

			assert.Contains(t, delivered, localSigFragmentBody, "validly signed local content must be delivered")
		})
	}
}

// TestLocalBundle_StaleSignature_Delivers is the case the rule exists for: the
// author edited the bundle and forgot to re-sign. The signature no longer
// covers the bytes. Locality already answered the trust question, so the
// content must still be delivered.
//
// The TRUSTED-key variant is the sharp one. For REMOTE content that same
// on-disk shape — a key you trust, over bytes it does not cover — is
// signing.ErrSignatureTampered and the bundle is withheld entirely (J001500). The
// distinction being asserted here is local-tree provenance vs fetched-content
// provenance: on content you authored yourself there is no adversary to
// detect, only a chore you skipped.
func TestLocalBundle_StaleSignature_Delivers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted bool
	}{
		{"untrusted key", false},
		{"trusted publisher key", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, mockLM := setupLocalSigEnv(t)
			signer, err := testenv.GenerateTestSigner()
			require.NoError(t, err)
			if tc.trusted {
				require.NoError(t, env.TrustSigner(signer, "author@example.test", true))
			}
			signLocalBundle(t, env, signer)
			editAfterSigning(t, env)

			// The signature really is stale — assert the precondition rather
			// than assume it, or this test could pass over a valid pair.
			body, err := env.ReadFile(localSigBundlePath)
			require.NoError(t, err)
			sig, err := env.ReadFile(localSigBundlePath + ".sig")
			require.NoError(t, err)
			require.Error(t, signing.CoversBytes([]byte(body), []byte(sig), signing.NamespacePublish),
				"precondition: the signature must NOT cover the edited bytes")

			delivered := deliverLocalFragment(t, env, mockLM, "signed-local")

			assert.Contains(t, delivered, localSigFragmentBody,
				"local content with a stale signature must still be delivered — locality is the trust decision")

			// Delivered, but not silently. The signature is now worthless to
			// the only thing that consumes it (promotion), and the author is
			// the only person who can fix that.
			assert.Contains(t, env.LastOutput(), "local.yaml.sig",
				"a stale signature must be reported, or the author learns of it at publish time")
			assert.Contains(t, env.LastOutput(), "still delivered",
				"the diagnostic must say the content was NOT withheld — that is the reader's first question")
			assert.Contains(t, env.LastOutput(), "ctxloom bundle sign local",
				"the diagnostic must name the command that fixes it")
		})
	}
}

// TestLocalBundle_CorruptSignature_Delivers: a .sig that is not even a
// well-formed signature blob is still just metadata on local content. It must
// not withhold, and — the failure mode that matters — it must not make the
// bundle fail to LOAD either, which would take every other item in it down too.
func TestLocalBundle_CorruptSignature_Delivers(t *testing.T) {
	env, mockLM := setupLocalSigEnv(t)
	require.NoError(t, env.WriteFile(localSigBundlePath+".sig", "not a signature at all\n"))

	delivered := deliverLocalFragment(t, env, mockLM, "signed-local")

	assert.Contains(t, delivered, localSigFragmentBody,
		"local content with a structurally invalid signature must still be delivered")
}

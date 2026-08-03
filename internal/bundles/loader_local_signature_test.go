package bundles

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// A bundle in the project's own content tree is trusted BY VIRTUE OF BEING
// LOCAL — the decision function allows it at its local step, above every
// signature step, and no filesystem load path verifies a publisher signature at
// all. So a stale or invalid sibling `.sig` cannot and must not withhold it.
//
// What it MUST do is say something. A signature that no longer covers its bytes
// is a promotion failure waiting to happen: `bundle push` and `bundle move --to`
// both refuse to publish that pair (they would hand every consumer a tamper
// alarm), so the author finds out at the moment they publish rather than at the
// moment they broke it. Saying it at load turns a deferred, confusing failure
// into an immediate, actionable one — and, unlike a withhold, costs the user
// none of their content.
//
// Every test below therefore asserts BOTH halves: the bundle is returned, AND
// the diagnostic is (or is not) emitted. Asserting only the warning would let a
// regression that withholds the content pass.

// signBytesFor signs data under the publish namespace with a throwaway key and
// returns the armored detached signature — real crypto, so "covers these bytes"
// is a fact rather than a fixture convention.
func signBytesFor(t *testing.T, data []byte) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	armored, err := signing.Sign(data, signer, signing.NamespacePublish)
	require.NoError(t, err)
	return armored
}

// localSigFixture writes a one-fragment bundle at /bundles/<name>.yaml on a
// fresh in-memory fs and returns the fs, the bundle path and its exact bytes.
//
// Each test passes a DISTINCT name on purpose: the stale-signature diagnostic
// deduplicates process-wide (see bundleWarner), so two tests sharing a path
// would have the second one silently observe the first one's suppression.
func localSigFixture(t *testing.T, name string) (afero.Fs, string, []byte) {
	t.Helper()
	mem := afero.NewMemMapFs()
	require.NoError(t, mem.MkdirAll("/bundles", 0o755))
	path := filepath.Join("/bundles", name+".yaml")
	body := []byte("version: \"1.0\"\nfragments:\n  keeper:\n    content: KEEPER-PAYLOAD\n")
	require.NoError(t, afero.WriteFile(mem, path, body, 0o644))
	return mem, path, body
}

// TestLoader_LoadFile_StaleLocalSignature_WarnsAndDelivers is the case the rule
// exists for: the author edited the bundle and forgot to re-sign. The content
// still loads; the user is told.
func TestLoader_LoadFile_StaleLocalSignature_WarnsAndDelivers(t *testing.T) {
	mem, path, body := localSigFixture(t, "stale-tools")
	// Signed over the ORIGINAL bytes, then the bytes change underneath it —
	// exactly what a hand edit after `ctxloom bundle sign` leaves on disk.
	require.NoError(t, afero.WriteFile(mem, path+SigSuffix, signBytesFor(t, body), 0o644))
	edited := append(append([]byte{}, body...), []byte("# edited, never re-signed\n")...)
	require.NoError(t, afero.WriteFile(mem, path, edited, 0o644))

	var warnings bytes.Buffer
	loader := NewLoader([]string{"/bundles"}, false, WithFS(mem), WithWarnWriter(&warnings))

	b, err := loader.LoadFile(path)

	require.NoError(t, err, "a stale signature must never stop local content loading")
	require.NotNil(t, b)
	assert.Equal(t, "KEEPER-PAYLOAD", b.Fragments["keeper"].Content,
		"the content must be delivered — locality already answered the trust question")
	assert.Contains(t, warnings.String(), "ctxloom: warning:")
	assert.Contains(t, warnings.String(), "stale-tools.yaml.sig")
	assert.Contains(t, warnings.String(), "ctxloom bundle sign stale-tools",
		"the warning must name the command that fixes it")
}

// The diagnostic must not fire on a healthy pair. A warning that cries wolf on
// every correctly signed local bundle would be trained away within a day, and
// then the stale case would be silent in practice even though it is emitted.
func TestLoader_LoadFile_ValidLocalSignature_Silent(t *testing.T) {
	mem, path, body := localSigFixture(t, "valid-tools")
	require.NoError(t, afero.WriteFile(mem, path+SigSuffix, signBytesFor(t, body), 0o644))

	var warnings bytes.Buffer
	loader := NewLoader([]string{"/bundles"}, false, WithFS(mem), WithWarnWriter(&warnings))

	b, err := loader.LoadFile(path)

	require.NoError(t, err)
	assert.Equal(t, "KEEPER-PAYLOAD", b.Fragments["keeper"].Content)
	assert.Empty(t, warnings.String(), "a signature that still covers the bytes is not a problem")
}

// No signature at all is the overwhelmingly common case — and the state the
// project's own ctxloom-project bundle was deliberately reduced to. It must be
// completely silent.
func TestLoader_LoadFile_UnsignedLocalBundle_Silent(t *testing.T) {
	mem, path, _ := localSigFixture(t, "plain-tools")

	var warnings bytes.Buffer
	loader := NewLoader([]string{"/bundles"}, false, WithFS(mem), WithWarnWriter(&warnings))

	b, err := loader.LoadFile(path)

	require.NoError(t, err)
	assert.Equal(t, "KEEPER-PAYLOAD", b.Fragments["keeper"].Content)
	assert.Empty(t, warnings.String())
}

// A `.sig` holding bytes that are not a signature at all is "invalid" rather
// than "stale", and lands in the same place: warn, deliver. The load-bearing
// half is that a garbage sidecar cannot take the bundle — and therefore every
// other item in it — down.
func TestLoader_LoadFile_CorruptLocalSignature_WarnsAndDelivers(t *testing.T) {
	mem, path, _ := localSigFixture(t, "corrupt-tools")
	require.NoError(t, afero.WriteFile(mem, path+SigSuffix, []byte("not a signature\n"), 0o644))

	var warnings bytes.Buffer
	loader := NewLoader([]string{"/bundles"}, false, WithFS(mem), WithWarnWriter(&warnings))

	b, err := loader.LoadFile(path)

	require.NoError(t, err)
	assert.Equal(t, "KEEPER-PAYLOAD", b.Fragments["keeper"].Content)
	assert.Contains(t, warnings.String(), "corrupt-tools.yaml.sig")
}

// A `.sig` that exists but cannot be READ is a third state: we cannot establish
// whether it covers the bytes. For LOCAL content that is still not a withhold —
// there is nothing to gate — but silence would be wrong, because the file is
// exactly as unpublishable as a stale one.
func TestLoader_LoadFile_UnreadableLocalSignature_WarnsAndDelivers(t *testing.T) {
	mem, path, _ := localSigFixture(t, "opaque-tools")
	sigPath := path + SigSuffix
	require.NoError(t, afero.WriteFile(mem, sigPath, []byte("armored-signature-bytes"), 0o644))

	var warnings bytes.Buffer
	loader := NewLoader([]string{"/bundles"}, false,
		WithFS(&openFailFs{Fs: mem, failPath: filepath.ToSlash(sigPath)}),
		WithWarnWriter(&warnings))

	b, err := loader.LoadFile(path)

	require.NoError(t, err, "an unreadable sidecar must not stop local content loading")
	assert.Equal(t, "KEEPER-PAYLOAD", b.Fragments["keeper"].Content)
	assert.Contains(t, warnings.String(), "opaque-tools.yaml.sig")
}

// Said ONCE. Context is assembled more than once per process (sync's
// regeneration and the launch-time assembly, each through an independent
// Loader), so a per-loader guard would still repeat the same line to the same
// user in one run — which is how a diagnostic becomes noise.
func TestLoader_LoadFile_StaleLocalSignature_WarnsOncePerBundle(t *testing.T) {
	mem, path, body := localSigFixture(t, "repeat-tools")
	require.NoError(t, afero.WriteFile(mem, path+SigSuffix, signBytesFor(t, body), 0o644))
	edited := append(append([]byte{}, body...), []byte("# edited\n")...)
	require.NoError(t, afero.WriteFile(mem, path, edited, 0o644))

	var first, second bytes.Buffer
	_, err := NewLoader([]string{"/bundles"}, false, WithFS(mem), WithWarnWriter(&first)).LoadFile(path)
	require.NoError(t, err)
	_, err = NewLoader([]string{"/bundles"}, false, WithFS(mem), WithWarnWriter(&second)).LoadFile(path)
	require.NoError(t, err)

	assert.Contains(t, first.String(), "repeat-tools.yaml.sig")
	assert.Empty(t, second.String(), "the same stale pair must not be reported twice in one process")
}

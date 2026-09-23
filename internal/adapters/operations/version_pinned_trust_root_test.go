package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A version-pinned ("@<commit>") remote read verifies the historical tree
// against the generation's trust root — the signer files on disk — through the
// production wiring (configload + BundleVersionResolver). A resolver handed a
// root that trusts nothing refuses the signed tree, so this fails if the
// pinned path ever verifies against anything but the generation's real root.
func TestVersionPinnedRemoteRead_VerifiesAgainstTheGenerationsOnDiskSigners(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, rev1, _, pub := remoteTreeContentRepo(t)
	appDir := filepath.Join(t.TempDir(), "consumer", ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	testsupport.WriteFileString(t, afero.NewOsFs(), paths.AllowedSignersPath(appDir),
		"publisher@example.com namespaces=\""+signing.NamespacePublish+"\" "+string(ssh.MarshalAuthorizedKey(pub)), 0o644)

	cfg, err := configload.Load(configload.WithAppDir(appDir), configload.WithVersionResolver(BundleVersionResolver))
	require.NoError(t, err)

	ref := "file://" + filepath.ToSlash(repoDir) + "@bundles/go-tools#fragments/fmt"
	items, err := cfg.BundleLoader().ReadFragmentAtVersion(ref, rev1)
	require.NoError(t, err, "a tree signed by a publisher the on-disk allowed_signers trusts must resolve at a pinned commit")
	require.Len(t, items, 1)
	assert.Equal(t, "T1-BODY", string(items[0].Resolve(false).Body))
}

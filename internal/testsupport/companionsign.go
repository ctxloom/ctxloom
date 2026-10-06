package testsupport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
)

// SignLoadoutForTesting signs a loadout document with a freshly minted key
// and trusts that key, for the PUBLISH namespace only, in allowedSigners —
// for a test whose companion content must verify as published by principal. It returns the detached
// signature the envelope carries.
func SignLoadoutForTesting(t testing.TB, doc []byte, principal, allowedSigners string) []byte {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a loadout signing key: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("wrap the loadout signing key: %v", err)
	}
	sig, err := signing.Sign(doc, signer, signing.NamespacePublish)
	if err != nil {
		t.Fatalf("sign the loadout: %v", err)
	}

	line := fmt.Sprintf("%s namespaces=%q %s %s\n",
		principal, signing.NamespacePublish,
		signer.PublicKey().Type(),
		base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()))
	if err := os.MkdirAll(filepath.Dir(allowedSigners), 0o755); err != nil {
		t.Fatalf("create the trust root dir: %v", err)
	}
	prev, err := os.ReadFile(allowedSigners) //nolint:gosec // a path this test chose
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", allowedSigners, err)
	}
	if err := safefs.WriteFile(afero.NewOsFs(), allowedSigners, append(prev, line...), 0o600); err != nil {
		t.Fatalf("trust the loadout key: %v", err)
	}
	return sig
}

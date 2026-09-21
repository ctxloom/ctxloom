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
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// SignCompanionForTesting makes the binary at path executable by ctxloom: it
// signs the bytes with a freshly minted key and trusts that key, for the
// companion namespace only, in allowedSigners.
//
// It exists because companion admission is answered by a SIGNATURE, so a test
// that wants its fake companion to run has to do what a publisher does. There
// is no approve-a-hash path to reach for any more — that was trust-on-first-use
// and it is gone.
//
// The key is minted PER CALL and trusted only in the file named, so no test
// depends on the developer's own keys and no test's grant leaks into another.
//
// Both halves are required and that is the point: signing without trusting
// produces `untrusted-signer`, and trusting without signing produces
// `unsigned`. A helper that did one silently would make a test pass for the
// wrong reason.
func SignCompanionForTesting(t testing.TB, path, allowedSigners string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a companion signing key: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("wrap the companion signing key: %v", err)
	}

	payload, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatalf("read %s to sign it: %v", path, err)
	}
	sig, err := signing.Sign(payload, signer, signing.NamespaceCompanion)
	if err != nil {
		t.Fatalf("sign %s: %v", path, err)
	}
	if err := iox.WriteFileAtomic(path+".sig", sig, 0o600); err != nil {
		t.Fatalf("write the signature for %s: %v", path, err)
	}

	line := fmt.Sprintf("%s namespaces=%q %s %s\n",
		"fixture-companion@testenv.invalid", signing.NamespaceCompanion,
		signer.PublicKey().Type(),
		base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()))
	if err := os.MkdirAll(filepath.Dir(allowedSigners), 0o755); err != nil {
		t.Fatalf("create the trust root dir: %v", err)
	}
	// Read-modify-write rather than O_APPEND: the write goes through iox so a
	// fixture and production agree on what "write a file" means, and iox
	// replaces rather than appends.
	prev, err := os.ReadFile(allowedSigners) //nolint:gosec // a path this test chose
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", allowedSigners, err)
	}
	if err := iox.WriteFileAtomic(allowedSigners, append(prev, line...), 0o600); err != nil {
		t.Fatalf("trust the companion key: %v", err)
	}
}

// SignLoadoutForTesting signs a loadout document with a freshly minted key
// and trusts that key, for the PUBLISH namespace only, in allowedSigners —
// the loadout half of SignCompanionForTesting, for a test whose companion
// content must verify as published by principal. It returns the detached
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
	if err := iox.WriteFileAtomic(allowedSigners, append(prev, line...), 0o600); err != nil {
		t.Fatalf("trust the loadout key: %v", err)
	}
	return sig
}

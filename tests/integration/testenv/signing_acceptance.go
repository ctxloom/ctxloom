//go:build integration || acceptance

package testenv

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// TestSigner is a generated ed25519 identity for the J000200 trust scenarios: a
// signer that can sign bundle bytes (SeedSignedRemote) and be trusted
// (TrustSigner), without any real ssh-agent or on-disk private key — the same
// approach internal/operations/sign_test.go's testSigner uses at the unit
// level, lifted here for the acceptance harness.
type TestSigner struct {
	Signer ssh.Signer
	Public ssh.PublicKey

	// Private is the raw ed25519 private key behind Signer. Kept so
	// StartSSHAgent can hand it to agent.NewKeyring: the ssh-agent keyring
	// holds PRIVATE keys and signs with them, and ssh.Signer exposes no way
	// back to the material it wraps. Nothing outside this package's own
	// in-process agent should read it — J001600 drives `ctxloom bundle sign`,
	// which never sees a private key, only the agent socket.
	Private crypto.PrivateKey
}

// GenerateTestSigner creates a fresh ed25519 identity.
func GenerateTestSigner() (*TestSigner, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		return nil, fmt.Errorf("wrap signer: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("wrap public key: %w", err)
	}
	return &TestSigner{Signer: signer, Public: sshPub, Private: priv}, nil
}

// AuthorizedKey renders this identity as a single authorized_keys/`.pub` line
// (optionally carrying comment) — the shape `git config user.signingkey` and
// `ctxloom trust signer add --key <path>` both accept.
func (s *TestSigner) AuthorizedKey(comment string) string {
	line := string(ssh.MarshalAuthorizedKey(s.Public)) // already newline-terminated
	if comment == "" {
		return line
	}
	return line[:len(line)-1] + " " + comment + "\n"
}

// Fingerprint is the SHA256 fingerprint ctxloom prints and stores for this
// identity.
func (s *TestSigner) Fingerprint() string { return ssh.FingerprintSHA256(s.Public) }

// SeedSignedRemote is SeedRemote plus a detached publisher signature: every
// path in signPaths (bundle YAML files already present in files, e.g.
// ".ctxloom/content/bundles/onboarding.yaml") gets a "<path>.sig" sibling
// carrying an armored PROTOCOL.sshsig blob over its EXACT bytes, produced by
// signer under the publish namespace — the same detached-sibling contract
// verifyBundlePublisher reads (internal/remote.SignatureSuffix). A caller that
// wants unsigned content simply uses SeedRemote directly; this helper exists
// for the signed/trusted J000200 scenarios.
func (e *TestEnvironment) SeedSignedRemote(files map[string]string, signPaths []string, signer *TestSigner) (string, error) {
	seeded := make(map[string]string, len(files)+len(signPaths))
	for k, v := range files {
		seeded[k] = v
	}
	for _, path := range signPaths {
		content, ok := files[path]
		if !ok {
			return "", fmt.Errorf("SeedSignedRemote: %q is not among the seeded files", path)
		}
		sig, err := signing.Sign([]byte(content), signer.Signer, signing.NamespacePublish)
		if err != nil {
			return "", fmt.Errorf("sign %q: %w", path, err)
		}
		seeded[path+".sig"] = string(sig)
	}
	return e.SeedRemote(seeded)
}

// AdvanceSignedRemote is AdvanceRemote plus a REFRESHED detached publisher
// signature: every path in signPaths gets its "<path>.sig" sibling
// regenerated over the new bytes in files, signed by signer under the publish
// namespace. Use this for a legitimate new signed version (e.g. a publisher
// adding content to an already-signed bundle). For an illegitimate change —
// content mutated WITHOUT a matching re-sign, the J001500 tamper scenario — call
// AdvanceRemote directly instead and leave the old ".sig" sibling in place, so
// it no longer verifies over the new bytes (signing.ErrSignatureTampered).
func (e *TestEnvironment) AdvanceSignedRemote(bareDir string, files map[string]string, signPaths []string, signer *TestSigner) error {
	seeded := make(map[string]string, len(files)+len(signPaths))
	for k, v := range files {
		seeded[k] = v
	}
	for _, path := range signPaths {
		content, ok := files[path]
		if !ok {
			return fmt.Errorf("AdvanceSignedRemote: %q is not among the advanced files", path)
		}
		sig, err := signing.Sign([]byte(content), signer.Signer, signing.NamespacePublish)
		if err != nil {
			return fmt.Errorf("sign %q: %w", path, err)
		}
		seeded[path+".sig"] = string(sig)
	}
	return e.AdvanceRemote(bareDir, seeded)
}

// TrustSigner writes an allowed_signers entry trusting signer's public key for
// principal, in the publish namespace. project=true writes the committable
// project store (.ctxloom/allowed_signers); project=false writes the personal
// user store (~/.ctxloom/allowed_signers) — this process's HOME is already the
// isolated e.HomeDir (see TestEnvironment.Setup), so the user-store write lands
// in this environment's fake home, not the real developer's.
func (e *TestEnvironment) TrustSigner(signer *TestSigner, principal string, project bool) error {
	var cfg *config.Config
	if project {
		cfg = config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join(e.ProjectDir, ".ctxloom")}})
	}
	_, err := operations.AddSigner(cfg, operations.AddSignerRequest{
		Principal:  principal,
		Key:        operations.SignerKeyInfo{PublicKey: signer.Public, Fingerprint: ssh.FingerprintSHA256(signer.Public)},
		Namespaces: []string{signing.NamespacePublish},
		Project:    project,
	})
	if err != nil {
		return fmt.Errorf("trust signer %q: %w", principal, err)
	}
	return nil
}

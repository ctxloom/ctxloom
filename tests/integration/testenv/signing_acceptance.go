//go:build integration || acceptance

package testenv

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
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
// `ctxloom signer add --key <path>` both accept.
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
// ".ctxloom/content/bundles/v1/onboarding.yaml") gets a "<path>.sig" sibling
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

// SeedSignedTreeRemote signs a directory-form bundle as the given signer would
// and publishes the resulting TREE — envelope, item files, and the
// SHA256SUMS/.sigs attestation attest.SignBundle produces — into a seeded git
// remote. root is the remote-relative directory the tree lands in (e.g.
// remoteSingleFilePublishPath(name) in the acceptance package); envelope is
// the bundle.yaml body (no inline item keys — see
// internal/bundles/tree_read.go's readEnvelope); items maps each item's path
// relative to the tree root (e.g. "fragments/marker.md") to its content.
//
// It goes through the PRODUCT's own signing path — content.NewTreeStore plus
// attest.SignBundle, on a real temp directory — rather than hand-building a
// manifest and a .sigs/ entry: steps_j001400_bundle_distribution.go's
// j001400SignTree took the same approach for the identical reason (a
// hand-rolled manifest is a second implementation of the signed-tree format,
// and the first thing it stops catching is the format drifting out from
// under it).
func (e *TestEnvironment) SeedSignedTreeRemote(root, bundleID, envelope string, items map[string]string, signer *TestSigner) (string, error) {
	files, err := signTreeFiles(e.Root, root, bundleID, envelope, items, signer)
	if err != nil {
		return "", err
	}
	return e.SeedRemote(files)
}

// AdvanceSignedTreeRemote is SeedSignedTreeRemote's AdvanceRemote counterpart:
// a fresh signature over a REVISED tree, pushed as a second commit.
//
// Unlike AdvanceRemote (which only ever overlays the files it is given, so a
// path omitted from one round simply survives untouched from a previous one),
// this REPLACES the bundle's entire directory — the same "destination
// REPLACED, not merged" contract remote.Puller.installTree gives a real
// pulled tree. It has to: a caller renaming an item (e.g. GAP A's
// fragment-rename fixture) hands items a NEW path and expects the OLD one
// gone, and the signed manifest attest.SignBundle just produced only ever
// covers what is IN items — leaving the old file behind would publish it
// unsigned and UNCLAIMED, which attest.VerifyBundle reports as tampering on
// the very next pull.
func (e *TestEnvironment) AdvanceSignedTreeRemote(bareDir, root, bundleID, envelope string, items map[string]string, signer *TestSigner) error {
	files, err := signTreeFiles(e.Root, root, bundleID, envelope, items, signer)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp(e.Root, "advance-tree-*")
	if err != nil {
		return err
	}
	if err := runGitE("", "clone", bareDir, work); err != nil {
		return err
	}
	for _, s := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
		{"config", "commit.gpgsign", "false"},
	} {
		if err := runGitE(work, s...); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(work, filepath.FromSlash(root))); err != nil {
		return fmt.Errorf("clear the previous %s tree: %w", root, err)
	}
	for rel, content := range files {
		full := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for _, s := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "advance"},
		{"push", "origin", "main"},
	} {
		if err := runGitE(work, s...); err != nil {
			return err
		}
	}
	return nil
}

// signTreeFiles is SeedSignedTreeRemote/AdvanceSignedTreeRemote's shared
// build step: write the tree to a real temp directory, sign it with the
// product's own attest.SignBundle, and read every resulting file back into a
// remote-relative files map.
func signTreeFiles(workRoot, root, bundleID, envelope string, items map[string]string, signer *TestSigner) (map[string]string, error) {
	work, err := os.MkdirTemp(workRoot, "sign-tree-*")
	if err != nil {
		return nil, err
	}
	bundleDir := filepath.Join(work, bundleID)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "bundle.yaml"), []byte(envelope), 0o644); err != nil {
		return nil, fmt.Errorf("write bundle.yaml: %w", err)
	}
	for rel, body := range items {
		full := filepath.Join(bundleDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
	}

	ctx := context.Background()
	store, err := content.NewTreeStore(afero.NewOsFs(), work, content.Provenance{IsLocal: true})
	if err != nil {
		return nil, fmt.Errorf("open the tree at %s: %w", work, err)
	}
	bundle, err := store.Open(ctx, content.BundleID(bundleID))
	if err != nil {
		return nil, fmt.Errorf("open bundle %q for signing: %w", bundleID, err)
	}
	if err := attest.SignBundle(ctx, store, bundle, signer.Signer); err != nil {
		return nil, fmt.Errorf("sign bundle %q: %w", bundleID, err)
	}

	files := map[string]string{}
	walkErr := filepath.WalkDir(bundleDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(bundleDir, p)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files[root+"/"+filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("collect signed tree %s: %w", bundleDir, walkErr)
	}
	return files, nil
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

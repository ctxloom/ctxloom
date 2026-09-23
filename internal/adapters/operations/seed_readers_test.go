package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// seedReaders presents authored bundle VALUES as the pinned content a reader
// reads: one reader per ref, over the bundle's own YAML bytes.
//
// It exists because no exported constructor lets a caller mint a provenance or
// a signer — deliberately, since one that did would be a trust bypass wearing a
// struct literal. So a test that wants content in a loader supplies BYTES and
// lets the reader establish the facts, exactly as a session does.
//
// A seed whose bundle carries a Signer() gets a real one: a throwaway key signs
// those exact bytes and the reader is given a trust root that authorizes that
// key for the wanted principal. The stamp therefore survives the round trip the
// only way it can — by being verified — which is the point of the field being
// unexported and yaml:"-".
//
// THE SEED KEY DECIDES WHICH READER. A canonical ref is pinned REMOTE content
// and gets the repofs reader; a bare bundle name is this project's own content
// and gets the project reader. Posture comes from the reader that produced the
// read, never re-derived from the ref string: a fixture whose reader disagrees
// with its identity is a fixture that cannot exercise the rows that key on the
// difference.
func seedReaders(t *testing.T, seed map[string]*bundles.Bundle) []bundles.Reader {
	t.Helper()
	refs := make([]string, 0, len(seed))
	for ref := range seed {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	projectFS := afero.NewMemMapFs()
	local := false
	var out []bundles.Reader
	for _, ref := range refs {
		b := seed[ref]
		if b == nil {
			continue
		}
		if !remote.IsSelfContainedRef(ref) && !strings.Contains(ref, "@") {
			// A bare name is a bundle in this project's own tree. The reader
			// is handed the bundles ROOT below and expands the format roots
			// itself — the bare root is searched by nobody.
			bundletree.WriteBundle(t, projectFS, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), ref, b, seedSkillOptions(b)...)
			local = true
			continue
		}

		var signer ssh.Signer
		opts := []bundles.ReaderOption{bundles.WithRepoURL(seedRepoURL(t, ref))}
		if principal := b.Signer(); principal != "" {
			s, root := seedSignerAs(t, principal)
			signer, opts = s, append(opts, bundles.WithTrustRoot(root))
		}
		fsys, root, id := stageSeedTree(t, ref, b, signer)
		if len(b.Skills) > 0 {
			// A skill is read from its package directory, so a seed carrying
			// one is read as an INSTALLED tree: bundle.Path, and FSDir, name
			// the directory on disk.
			opts = append(opts, bundles.WithInstalledDir(filepath.Join(root, string(id))))
		}
		tfs, err := content.NewAferoTreeFS(fsys, root)
		require.NoError(t, err)
		out = append(out, bundles.NewRepoFSReader(tfs, ref, opts...))
	}
	if local {
		out = append(out, bundles.NewProjectReader(projectFS, []string{"/bundles"}))
	}
	return out
}

// seedItemRef is the CANONICAL item reference addressing an item in the bundle
// seeded under key. A seed key is the lockfile/pipeline spelling a reader is
// handed ("<url>@bundles/<path>", or a bare project bundle name); a trust
// mutation is typed by a human and takes the canonical URI. Minting through
// trust.Ref.AsBundleRef is the same bridge the reader itself stamps its source
// ref with, so a fixture can never address an identity the reader did not
// produce.
func seedItemRef(t *testing.T, key, selector string) string {
	t.Helper()
	kind, name, err := trust.ParseSelector(selector)
	require.NoError(t, err, "selector %q", selector)
	src := trust.Ref{Bundle: key, IsLocal: true}
	if parsed, perr := remote.ParseReference(key); perr == nil {
		src = trust.Ref{RepoURL: parsed.URL, Bundle: parsed.Path, IsLocal: parsed.IsLocal, IsCompanion: parsed.IsCompanion}
	}
	base, err := src.AsBundleRef()
	require.NoError(t, err, "seed key %q", key)
	full, err := base.WithItem(kind, name)
	require.NoError(t, err, "seed key %q selector %q", key, selector)
	return full.String()
}

// seedLoader is seedReaders wired into a loader, for the many tests whose only
// interest is "a loader that can see this content".
func seedLoader(t *testing.T, seed map[string]*bundles.Bundle) *bundles.Loader {
	t.Helper()
	return bundles.NewLoader(seedReaders(t, seed)...)
}

// seedUntrustedSigned presents b as pinned content signed by a key NOTHING on
// this machine trusts, and returns the loader plus the fingerprint of the key
// that made the signature — the display-only value a reviewer compares against
// what the publisher told them out of band.
//
// It signs for real rather than stamping a string, because a fingerprint that
// did not come from a key is not the fact the review surface claims to be
// showing.
func seedUntrustedSigned(t *testing.T, ref string, b *bundles.Bundle) (*bundles.Loader, string) {
	t.Helper()
	signer, _, pub := seedSigner(t, "nobody@example.test")
	// No trust root: nothing here trusts the key, which is the state under test.
	l := bundles.NewLoader(bundles.NewRepoFSReader(seedTree(t, ref, b, signer), ref,
		bundles.WithRepoURL(seedRepoURL(t, ref))))
	return l, ssh.FingerprintSHA256(pub)
}

// seedSigner mints a throwaway key and the trust root that authorizes it to
// publish as principal, returning the SIGNER — a tree is signed over its own
// manifest, by attest.SignBundle, once the tree exists, so there is no payload
// to sign ahead of time.
func seedSigner(t *testing.T, principal string) (ssh.Signer, trust.TrustRoot, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return sshSigner, allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	}), sshPub
}

// seedSignerAs is seedSigner for the callers that do not need the public key.
func seedSignerAs(t *testing.T, principal string) (ssh.Signer, trust.TrustRoot) {
	t.Helper()
	signer, root, _ := seedSigner(t, principal)
	return signer, root
}

// seedRepoURL is the publisher repository a seeded ref claims to have come
// from: the canonical ref's own prefix, so a fixture claims the origin it names
// rather than a constant that could disagree with the ref trust keys on.
//
// A tree read opens a content store, and content.Provenance REFUSES to default
// a remote origin, so this is required rather than decorative.
func seedRepoURL(_ *testing.T, ref string) string {
	// A ref with no "@" is not canonical, and at least one fixture seeds one
	// DELIBERATELY — the unmintable-source characterization tests hand the gate
	// a ref nothing can address, and the reader must still be constructible for
	// them to observe what it does with it. The whole ref is the honest origin
	// to claim there: it is all the fixture said.
	if url, _, found := strings.Cut(ref, "@"); found {
		return url
	}
	return ref
}

// seedTree stages b as the TREE a repofs reader now requires, and serves it
// through the same TreeFS seam a pinned remote does.
//
// It writes through the production store save and content writer
// (bundletree), so a seeded fixture is the shape a publisher would publish.
//
// signer, when non-nil, signs the finished tree's manifest.
func seedTree(t *testing.T, ref string, b *bundles.Bundle, signer ssh.Signer) bundles.TreeFS {
	t.Helper()
	fsys, root, _ := stageSeedTree(t, ref, b, signer)
	tfs, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	return tfs
}

// stageSeedTree writes the tree and hands back the filesystem it lives on, so a
// caller that must disturb the bytes AFTER signing can reach them. It is the
// OS filesystem: a seeded skill is read from its package directory through the
// loader's filesystem, which is the OS one for pinned content.
func stageSeedTree(t *testing.T, ref string, b *bundles.Bundle, signer ssh.Signer) (afero.Fs, string, content.BundleID) {
	t.Helper()
	root := t.TempDir()
	id := content.BundleID(path.Base(strings.TrimSuffix(ref, "/")))
	fsys := afero.NewOsFs()
	st, err := content.NewTreeStore(fsys, root, content.Provenance{IsLocal: true})
	require.NoError(t, err)

	// A tree's envelope declares NO items — they are files beside it — so
	// `version:` is the whole of what it carries, and ParseBundle refuses one
	// that declares neither. Document seeds never needed a version because
	// their items were in the same file. Defaulting it here, on a copy, keeps
	// that a property of the staging rather than something every seed fixture
	// has to remember; a seed that sets its own version keeps it.
	staged := *b
	if staged.Version == "" {
		staged.Version = "1.0.0"
	}
	bundletree.WriteBundle(t, fsys, root, string(id), &staged, seedSkillOptions(b)...)
	if signer != nil {
		tree, err := st.Open(context.Background(), id)
		require.NoError(t, err)
		require.NoError(t, attest.SignBundle(context.Background(), st, tree, treeRelease(t, tree), signer))
	}
	return fsys, root, id
}

// signedTreeFiles stages b as a signed tree and returns it in the shape a
// remote.TreeFetchFunc hands back: bundle-root-relative paths to file bytes.
//
// It is the bridge between the two seams. Staging goes through the production
// converter and signer, so what the walk verifies is a real signed tree; the
// map it returns is what the fetch seam would have produced, so a test can
// exercise the walk without standing up a forge double that can serve a
// directory listing.
func signedTreeFiles(t *testing.T, id string, b *bundles.Bundle, signer ssh.Signer) map[string]remote.TreeFile {
	t.Helper()
	fsys, root, bid := stageSeedTree(t, id, b, signer)
	dir := path.Join(root, string(bid))
	out := map[string]remote.TreeFile{}
	require.NoError(t, afero.Walk(fsys, dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		require.NoError(t, rerr)
		data, rerr := afero.ReadFile(fsys, p)
		require.NoError(t, rerr)
		out[filepath.ToSlash(rel)] = remote.TreeFile{Data: data}
		return nil
	}))
	require.NotEmpty(t, out, "the staged tree must have produced files")
	return out
}

// seedHostileTree stages b as a tree and then writes extra files into it
// DIRECTLY, bypassing the writer.
//
// That bypass is the point, not a shortcut. content.Writer refuses to write a
// malformed package — a path with a newline in it, a skill
// with no SKILL.md — which is exactly right for a publisher using ctxloom, and
// exactly wrong for a fixture about a publisher who does NOT. A hostile tree is
// bytes in a repository, not the output of our own writer, so it has to be
// written the way an attacker would: straight onto the filesystem.
func seedHostileTree(t *testing.T, ref string, b *bundles.Bundle, extra map[string][]byte) bundles.TreeFS {
	t.Helper()
	fsys, root, id := stageSeedTree(t, ref, b, nil)
	for rel, data := range extra {
		full := filepath.Join(root, string(id), filepath.FromSlash(rel))
		require.NoError(t, fsys.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, afero.WriteFile(fsys, full, data, 0o644))
	}
	tfs, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	return tfs
}

// seedSkillPackages holds the package a test states for a seeded bundle's
// skill, keyed by the bundle value. A skill is a directory of files, and a
// bundle value no longer carries them, so a test that cares what the package
// holds states it here (withSeedSkillPackage); every other seeded skill gets
// defaultSeedSkillPackage.
var seedSkillPackages = map[*bundles.Bundle]map[string]map[string]bundletree.File{}

// withSeedSkillPackage states the package b's skill carries when b is seeded.
func withSeedSkillPackage(t *testing.T, b *bundles.Bundle, skill string, files map[string]bundletree.File) {
	t.Helper()
	if seedSkillPackages[b] == nil {
		seedSkillPackages[b] = map[string]map[string]bundletree.File{}
		t.Cleanup(func() { delete(seedSkillPackages, b) })
	}
	seedSkillPackages[b][skill] = files
}

// defaultSeedSkillPackage is a minimal readable package: SKILL.md and one
// executable script.
func defaultSeedSkillPackage(name string) map[string]bundletree.File {
	return map[string]bundletree.File{
		"SKILL.md":       {Body: "---\nname: " + name + "\ndescription: seeded " + name + "\n---\n\nseeded skill\n"},
		"scripts/run.sh": {Body: "#!/bin/sh\necho " + name + "\n", Executable: true},
	}
}

// seedSkillOptions writes every skill b declares with its stated or default
// package.
func seedSkillOptions(b *bundles.Bundle) []bundletree.Option {
	var out []bundletree.Option
	for name := range b.Skills {
		files, ok := seedSkillPackages[b][name]
		if !ok {
			files = defaultSeedSkillPackage(name)
		}
		out = append(out, bundletree.WithSkill(name, files))
	}
	return out
}

// seedTampered presents b as pinned content that was SIGNED AND THEN ALTERED —
// the spec §10.2 downgrade attempt, in the only form a tree can express it.
//
// The document form expressed this as a signature made over different bytes.
// A tree cannot: its signature covers a MANIFEST, and the manifest covers the
// item files, so "signed then altered" is a tree whose files no longer match
// SHA256SUMS. That is a genuinely different mechanism and it is caught in a
// genuinely different place — at the READ, by attest.VerifyBundle, which is why
// the loader below yields no read at all rather than a read carrying
// SignatureInvalid.
//
// It REQUIRES that it found a file to alter. A tamper helper that tampered with
// nothing would make every assertion resting on it vacuous.
func seedTampered(t *testing.T, ref, principal string, b *bundles.Bundle) *bundles.Loader {
	t.Helper()
	signer, root := seedSignerAs(t, principal)
	fsys, treeRoot, id := stageSeedTree(t, ref, b, signer)

	altered := false
	require.NoError(t, afero.Walk(fsys, path.Join(treeRoot, string(id)), func(p string, info os.FileInfo, err error) error {
		if err != nil || altered || info.IsDir() {
			return err
		}
		// Anything but the manifest and its signature: the publisher's key must
		// still verify over SHA256SUMS, so that what fails is the tree/manifest
		// agreement rather than the signature itself.
		if strings.Contains(p, content.ManifestPath) || strings.Contains(p, content.SigDirName) {
			return nil
		}
		before, rerr := afero.ReadFile(fsys, p)
		require.NoError(t, rerr)
		require.NoError(t, afero.WriteFile(fsys, p, append(before, []byte("\nnot these bytes\n")...), 0o644))
		altered = true
		return nil
	}))
	require.True(t, altered, "the fixture must have altered an item file, or it is testing nothing")

	tfs, err := content.NewAferoTreeFS(fsys, treeRoot)
	require.NoError(t, err)
	return bundles.LoaderOf(bundles.Resolve(context.Background(), strictness.Sink("ctxloom"), bundles.NewRepoFSReader(tfs, ref,
		bundles.WithRepoURL(seedRepoURL(t, ref)), bundles.WithTrustRoot(root),
		bundles.WithReaderReporter(strictness.Sink("ctxloom")))))
}

// seedTrustedSigned is seedUntrustedSigned's counterpart: b as pinned content
// signed by a key this machine DOES trust to publish as principal.
func seedTrustedSigned(t *testing.T, ref, principal string, b *bundles.Bundle) *bundles.Loader {
	t.Helper()
	signer, root := seedSignerAs(t, principal)
	return bundles.NewLoader(bundles.NewRepoFSReader(seedTree(t, ref, b, signer), ref,
		bundles.WithRepoURL(seedRepoURL(t, ref)), bundles.WithTrustRoot(root)))
}

// readOf resolves ref through loader to the READ its reader produced — the
// value every trust decision now keys on.
func readOf(t *testing.T, loader *bundles.Loader, ref string) bundles.BundleRead {
	t.Helper()
	read, err := loader.Read(ref)
	require.NoError(t, err, "the fixture bundle %q must resolve", ref)
	return read
}

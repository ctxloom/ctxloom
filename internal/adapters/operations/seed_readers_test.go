package operations

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
			s, root, _ := bundletree.PublisherKey(t, principal)
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

// seedLoader is seedReaders wired into a loader, for the many tests whose only
// interest is "a loader that can see this content".
func seedLoader(t *testing.T, seed map[string]*bundles.Bundle) *bundles.Loader {
	t.Helper()
	return bundles.NewLoader(seedReaders(t, seed)...)
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

	bundletree.WriteBundle(t, fsys, root, string(id), b, seedSkillOptions(b)...)
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

// seedSkillPackages holds the package a test states for a seeded bundle's
// skill, keyed by the bundle value. A skill is a directory of files, and a
// bundle value no longer carries them, so a test that cares what the package
// holds states it here (withSeedSkillPackage); every other seeded skill gets
// defaultSeedSkillPackage.
var seedSkillPackages = map[*bundles.Bundle]map[string]map[string]bundletree.File{}

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

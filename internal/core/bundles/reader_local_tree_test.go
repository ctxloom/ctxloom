package bundles

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Local tree-form authoring read.
//
// WHY THESE ASSERT BODIES AND NOT COUNTS: the defect being pinned is this
// project's characteristic silent no-op — a tree-form bundle that loaded as
// zero items while reporting success. A test that asserted "the bundle read
// without error" passed throughout the entire life of that bug, and so would a
// test asserting only that a name appears. Every assertion below reaches the
// item's BYTES.

// stageLocalTree writes a bundle as a TREE through the production content
// writer — the same Put/PutRootFile convert.Apply uses — rather than
// hand-rolling item YAML. A hand-rolled fixture would pin this test to a layout
// the writer could drift away from, and the drift would show up as this test
// still passing against a tree nothing else produces.
func stageLocalTree(t *testing.T, envelope string, put func(w content.Writer)) afero.Fs {
	t.Helper()
	fsys := afero.NewMemMapFs()
	// A staged tree carries its items as files, so it is format v2 and belongs
	// in the v2 root the reader actually searches.
	v2 := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2, 0o755))
	st, err := content.NewTreeStore(fsys, v2, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	put(st)
	require.NoError(t, st.PutRootFile(context.Background(), "vault", DirectoryFormManifest, []byte(envelope)))
	return fsys
}

// localV2 is where a fixture must write for readOneLocal to find it: the
// FORMAT ROOT beneath the /bundles root these tests hand the reader. The bare
// root is only its parent and is searched by nobody.
func localV2(rel string) string {
	return filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), rel)
}

// readOneLocal reads the single bundle a project reader finds under /bundles.
func readOneLocal(t *testing.T, fsys afero.Fs) *Bundle {
	t.Helper()
	reads, err := NewProjectReader(fsys, []string{"/bundles"}, WithReaderReporter(ledger())).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1, "expected exactly one bundle under /bundles")
	return reads[0].Bundle
}

func putFragment(w content.Writer, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: "vault", Kind: trust.KindFragment, Name: name},
		signing.FormRaw,
		content.Fragment{Name: name, ItemMeta: content.ItemMeta{Body: body, Tags: []string{"style"}}})
}

func putCommand(w content.Writer, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: "vault", Kind: trust.KindPrompt, Name: name},
		signing.FormRaw,
		content.Command{Name: name, ItemMeta: content.ItemMeta{Body: body, Description: "ship it"}})
}

func putSkill(w content.Writer, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: "vault", Kind: trust.KindSkill, Name: name},
		signing.FormRaw,
		content.Skill{Name: name, Files: []content.SkillFile{
			{Path: "SKILL.md", Bytes: []byte(body), Mode: content.ModeRegular},
		}})
}

const treeEnvelope = "name: vault\nversion: 1.2.3\ndescription: the vault bundle\n"

// TestLocalTreeForm_FragmentResolvesWithItsBody is the regression this work
// exists for: before it, this bundle read as zero fragments and said nothing.
func TestLocalTreeForm_FragmentResolvesWithItsBody(t *testing.T) {
	fsys := stageLocalTree(t, treeEnvelope, func(w content.Writer) {
		putFragment(w, "house-style", "FRAG-BODY-MARKER")
	})
	b := readOneLocal(t, fsys)

	require.Len(t, b.Fragments, 1, "the tree's fragment file must be enumerated")
	frag, ok := b.Fragments["house-style"]
	require.True(t, ok, "fragment must resolve under its file-derived name")
	// The BODY, not the name: a reader that enumerated the file but decoded
	// nothing would satisfy every assertion above this one.
	require.Equal(t, "FRAG-BODY-MARKER", frag.Content)
	// Envelope metadata survives the swap to the tree-read bundle.
	require.Equal(t, "1.2.3", b.Version)
	require.Equal(t, "the vault bundle", b.Description)
}

// TestLocalTreeForm_CommandsAndSkillsResolve covers the two kinds the old
// reader could not serve at all: it required the retired inline shape for
// skills, so a tree-form skill was unreachable rather than merely empty.
func TestLocalTreeForm_CommandsAndSkillsResolve(t *testing.T) {
	fsys := stageLocalTree(t, treeEnvelope, func(w content.Writer) {
		putFragment(w, "house-style", "FRAG-BODY-MARKER")
		putCommand(w, "ship-it", "CMD-BODY-MARKER")
		putSkill(w, "reviewer", "SKILL-BODY-MARKER")
	})
	b := readOneLocal(t, fsys)

	require.Len(t, b.Commands, 1)
	require.Equal(t, "CMD-BODY-MARKER", b.Commands["ship-it"].Content)

	require.Len(t, b.Skills, 1)
	_, ok := b.Skills["reviewer"]
	require.True(t, ok, "a tree-form skill must resolve without the retired inline shape")
}

// TestLocalTreeForm_AStrayDocumentIsNotABundle: a bundle is a tree. A .yaml
// file beside the trees is just a file — it names no bundle, and nothing
// reports it as a bundle that failed to read.
func TestLocalTreeForm_AStrayDocumentIsNotABundle(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, localV2("vault.yaml"), []byte(
		"name: vault\nversion: 1.2.3\nfragments:\n  solo-frag:\n    content: SOLO-BODY-MARKER\n"), 0o644))

	r := NewProjectReader(fsys, []string{"/bundles"}, WithReaderReporter(ledger()))
	reads, err := r.Read(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reads)
	failures, ok := r.(interface{ ReadFailures() map[string]error })
	require.True(t, ok)
	assert.Empty(t, failures.ReadFailures(), "a file that is not a bundle is not a bundle that failed")
}

// TestLocalTreeForm_AnEnvelopeDeclaringItemsIsRefused: a tree's items are its
// files, so an envelope that declares items inline has two answers for them
// (or, with no files beside it, claims items the tree does not hold). Either
// way the bundle is refused, and the reader says why.
func TestLocalTreeForm_AnEnvelopeDeclaringItemsIsRefused(t *testing.T) {
	inline := "name: vault\nversion: 1.2.3\nfragments:\n  inline-frag:\n    content: INLINE-BODY-MARKER\n"
	alone := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(alone, localV2("vault/bundle.yaml"), []byte(inline), 0o644))
	for name, fsys := range map[string]afero.Fs{
		"beside item files": stageLocalTree(t, inline, func(w content.Writer) { putFragment(w, "house-style", "FRAG-BODY-MARKER") }),
		"alone":             alone,
	} {
		t.Run(name, func(t *testing.T) {
			r := NewProjectReader(fsys, []string{"/bundles"}, WithReaderReporter(ledger()))
			reads, err := r.Read(context.Background())
			require.NoError(t, err)
			assert.Empty(t, reads)
			failures, ok := r.(interface{ ReadFailures() map[string]error })
			require.True(t, ok)
			require.Contains(t, failures.ReadFailures(), "vault")
			assert.Contains(t, failures.ReadFailures()["vault"].Error(), "inline")
		})
	}
}

// TestLocalTreeForm_UnreadableTreeIsReportedNotSilentlyEmptied pins the
// no-swallow rule, which is the whole point of the change.
//
// A tree holding a file no surface type claims (guard.yml, the mis-extensioned
// hook content.Refs refuses to enumerate past) must not degrade into a bundle
// that loads with zero items. Falling back to the document read on error would
// do exactly that: the envelope of a tree-form bundle deliberately carries no
// items, so the fallback's "success" is an empty bundle and a zero exit — the
// shape this work exists to delete.
//
// The assertion is on the EFFECT in both directions: the bundle is absent from
// the read AND the reader can say why, which is what keeps "will not load" from
// reaching the user as "does not exist".
func TestLocalTreeForm_UnreadableTreeIsReportedNotSilentlyEmptied(t *testing.T) {
	fsys := stageLocalTree(t, treeEnvelope, func(w content.Writer) {
		putFragment(w, "house-style", "FRAG-BODY-MARKER")
	})
	require.NoError(t, afero.WriteFile(fsys, localV2("vault/hooks/guard.yml"), []byte("bad"), 0o644))

	r := NewProjectReader(fsys, []string{"/bundles"})
	reads, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Empty(t, reads, "an unreadable tree must not read as a bundle at all")

	failures, ok := r.(interface{ ReadFailures() map[string]error })
	require.True(t, ok)
	require.Contains(t, failures.ReadFailures(), "vault",
		"the reader must record WHY the bundle is missing, not drop it silently")
}

// TestLocalTreeForm_MetadataOnlyDirectoryBundleStillLoads pins the empty-tree
// fall-through. ReadTree refuses an item-less bundle outright, so routing this
// shape there would turn a bundle that loads today into a hard failure.
func TestLocalTreeForm_MetadataOnlyDirectoryBundleStillLoads(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, localV2("vault/bundle.yaml"),
		[]byte(treeEnvelope), 0o644))
	b := readOneLocal(t, fsys)

	require.Empty(t, b.Fragments)
	require.Equal(t, "1.2.3", b.Version)
}

// TestLocalTreeForm_ItemFilesInsideATreeAreNotThemselvesBundles pins the walk
// boundary. A tree bundle owns everything beneath it: those files are its ITEMS.
//
// The regression this exists for is not hypothetical — it appeared the moment
// real bundles were converted to tree form. A converted bundle carries its
// profiles as profiles/<name>.yaml, the walk descended into the tree, and every
// one of those item files was read back as a malformed BUNDLE: fifteen spurious
// "skipping bundle" failures on every single command. Nothing was lost, which is
// exactly what made it corrosive — a warning channel that cries on every
// invocation is one nobody reads by the time it matters.
//
// readOneLocal's require.Len(reads, 1) IS the assertion: a walk that descends
// finds two, so this cannot pass vacuously.
func TestLocalTreeForm_ItemFilesInsideATreeAreNotThemselvesBundles(t *testing.T) {
	fsys := stageLocalTree(t, treeEnvelope, func(w content.Writer) {
		putFragment(w, "house-style", "FRAG-BODY-MARKER")
	})

	// A .yaml item file inside the tree, the shape a converted bundle's
	// profiles/ directory has. Deliberately NOT parseable as a bundle.
	require.NoError(t, fsys.MkdirAll(localV2("vault/profiles"), 0o755))
	testsupport.WriteFileString(t, fsys,
		filepath.Join(localV2("vault/profiles"), "coordinator.yaml"),
		"bundles:\n  - something\n", 0o644)

	// ASSERT ON THE FINDING, NOT THE COUNT. A spurious item-file read FAILS to
	// parse, so it raises a finding and is never appended to the results --
	// which means require.Len(reads, 1) passes whether the walk descends or
	// not. That version of this test was written first and a mutation removing
	// the skip survived it.
	mark := strictness.Checkpoint()
	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)
	require.Empty(t, strictness.Since(mark),
		"a tree's own item files must not be read back as malformed bundles")

	require.Len(t, reads, 1, "exactly one bundle under /bundles")
	b := reads[0].Bundle
	require.Equal(t, "vault", b.Name)
	frag, ok := b.Fragments["house-style"]
	require.True(t, ok, "skipping the subtree must not skip the bundle's own items")
	require.Equal(t, "FRAG-BODY-MARKER", frag.Content)
}

// A tree-form fragment's PREMISE must survive the read. It is authored as the
// item's `description` front-matter key (ItemMeta.Description) and lands on
// BundleFragment.Premise, the field conditional delivery is driven from.
//
// Losing it does not fail loudly, which is why this needs a test: an empty
// premise means ALWAYS LOADED, so a dropped premise silently converts a
// conditional fragment into an unconditional one. The author asks for "load
// this only when the agent is doing X" and gets "load this always" — with no
// error, no warning, and a corpus that quietly grows.
//
// The command path already carried Description; only the fragment path did
// not, so a test asserting merely that the fragment resolves passes either way.
func TestLocalTreeForm_FragmentPremiseSurvivesTheRead(t *testing.T) {
	const premise = "PREMISE-MARKER: you are about to hand-roll a wire format"
	fsys := stageLocalTree(t, treeEnvelope, func(w content.Writer) {
		_ = w.Put(context.Background(),
			trust.Ref{Bundle: "vault", Kind: trust.KindFragment, Name: "conditional"},
			signing.FormRaw,
			content.Fragment{Name: "conditional", ItemMeta: content.ItemMeta{
				Body:        "BODY",
				Description: premise,
			}})
	})
	b := readOneLocal(t, fsys)

	frag, ok := b.Fragments["conditional"]
	require.True(t, ok, "fragment must resolve")
	require.Equal(t, premise, frag.Premise,
		"the authored premise was dropped on the way in; the fragment would load unconditionally")
}

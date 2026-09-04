package bundles

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
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
	require.NoError(t, fsys.MkdirAll("/bundles", 0o755))
	st, err := content.NewTreeStore(fsys, "/bundles", content.Provenance{IsLocal: true})
	require.NoError(t, err)
	put(st)
	require.NoError(t, st.PutRootFile(context.Background(), "vault", DirectoryFormManifest, []byte(envelope)))
	return fsys
}

// readOneLocal reads the single bundle a project reader finds under /bundles.
func readOneLocal(t *testing.T, fsys afero.Fs) *Bundle {
	t.Helper()
	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1, "expected exactly one bundle under /bundles")
	return reads[0].Bundle
}

func putFragment(w content.Writer, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: "vault", Kind: trust.KindFragment, Name: name},
		signing.FormRaw,
		content.Fragment{Name: name, Body: body, Tags: []string{"style"}})
}

func putCommand(w content.Writer, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: "vault", Kind: trust.KindPrompt, Name: name},
		signing.FormRaw,
		content.Command{Name: name, Body: body, Description: "ship it"})
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

// TestLocalTreeForm_InlineDirectoryFormStillReadsAsADocument pins the half of
// the contract that is about NOT changing: the retired inline shape is still
// live in this repo's authored bundles and must keep loading exactly as it did.
// It is also the guard against "fix tree form by making everything tree form".
func TestLocalTreeForm_InlineDirectoryFormStillReadsAsADocument(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/vault/bundle.yaml", []byte(
		"name: vault\nversion: 1.2.3\nfragments:\n  inline-frag:\n    content: INLINE-BODY-MARKER\n"), 0o644))
	b := readOneLocal(t, fsys)

	require.Len(t, b.Fragments, 1)
	require.Equal(t, "INLINE-BODY-MARKER", b.Fragments["inline-frag"].Content)
}

// TestLocalTreeForm_SingleFileDocumentStillReads pins the other unchanged form.
func TestLocalTreeForm_SingleFileDocumentStillReads(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/vault.yaml", []byte(
		"name: vault\nversion: 1.2.3\nfragments:\n  solo-frag:\n    content: SOLO-BODY-MARKER\n"), 0o644))
	b := readOneLocal(t, fsys)

	require.Len(t, b.Fragments, 1)
	require.Equal(t, "SOLO-BODY-MARKER", b.Fragments["solo-frag"].Content)
}

// TestLocalTreeForm_SingleFileDocumentBesideItemDirsStillReads pins the
// manifest-name guard, which the plain single-file case does NOT reach.
//
// Found by mutation, and the fixture is this specific for a reason — two
// EARLIER mutation attempts survived, and each one narrowed it:
//
//   - A document at <dir>/vault.yaml roots a tree at <dir>'s PARENT with id
//     "<dir>". A search directory does not normally hold fragments/ of its own,
//     so the enumeration is empty and the empty-tree fall-through rescues the
//     read by accident. Hence the stray item directory below.
//   - A document that declares items inline returns at the inlineKeys guard
//     before the manifest name is ever consulted. Hence a document that
//     declares NOTHING — the only shape for which this guard is the sole
//     defence.
//
// Without the guard this bundle is read as a tree whose envelope
// (<dir>/bundle.yaml) does not exist, the read fails, and a valid single-file
// bundle disappears from the listing entirely.
func TestLocalTreeForm_EmptySingleFileDocumentBesideItemDirsStillReads(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fsys, "/bundles/vault.yaml", []byte(treeEnvelope), 0o644))
	// A stray item-kind directory in the SEARCH dir, not in any bundle: enough
	// to make that directory enumerate as a tree if the guard stops holding.
	require.NoError(t, afero.WriteFile(fsys, "/bundles/fragments/stray.md", []byte("STRAY"), 0o644))

	// The bundle must still be THERE, and it must be the document's own bytes:
	// the version is carried by no other file in this fixture.
	b := readOneLocal(t, fsys)
	require.Equal(t, "1.2.3", b.Version)
	require.Equal(t, "the vault bundle", b.Description)
}

// TestLocalTreeForm_HalfMigratedBundleReadsItsInlineItems pins the boundary
// against readEnvelope's refusal, which this change must not have relaxed.
//
// A bundle.yaml carrying inline items BESIDE item files is not routed to
// ReadTree at all — it is the old document form and reads as one. The refusal
// still governs every tree that DOES reach ReadTree; what this asserts is that
// the local reader never hands it a half-migrated envelope to refuse, so
// migrating one bundle cannot break its neighbour.
func TestLocalTreeForm_HalfMigratedBundleReadsItsInlineItems(t *testing.T) {
	fsys := stageLocalTree(t,
		"name: vault\nversion: 1.2.3\nfragments:\n  inline-frag:\n    content: INLINE-BODY-MARKER\n",
		func(w content.Writer) { putFragment(w, "house-style", "FRAG-BODY-MARKER") })
	b := readOneLocal(t, fsys)

	require.Len(t, b.Fragments, 1, "the inline item is the only answer; the file is not merged in")
	require.Equal(t, "INLINE-BODY-MARKER", b.Fragments["inline-frag"].Content)
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
	require.NoError(t, afero.WriteFile(fsys, "/bundles/vault/hooks/guard.yml", []byte("bad"), 0o644))

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
	require.NoError(t, afero.WriteFile(fsys, "/bundles/vault/bundle.yaml",
		[]byte(treeEnvelope), 0o644))
	b := readOneLocal(t, fsys)

	require.Empty(t, b.Fragments)
	require.Equal(t, "1.2.3", b.Version)
}

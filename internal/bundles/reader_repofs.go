package bundles

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TreeFS is the pinned-tree seam a repofs reader reads through: LIST a
// directory, READ a file, and nothing else. It is content.TreeFS itself rather
// than a parallel declaration, so a remote tree at a pinned SHA
// (content.NewMapTreeFS over fetched bytes) and an installed tree on disk
// (content.NewAferoTreeFS) both satisfy it without an adapter.
type TreeFS = content.TreeFS

// ErrTreeBundleWithheld reports a directory-form bundle that was read and must
// NOT be used: its signed manifest and its files disagree, or a trusted key's
// signature does not cover the manifest at all.
//
// It is separate from a read failure because the two call for opposite
// responses. A read failure is "this did not arrive"; this is "this arrived and
// is not what the publisher signed", which is a security event and must never
// degrade to the unsigned/review path — degrading it would let an attacker
// downgrade a signed tree by editing one file in the cache.
var ErrTreeBundleWithheld = errors.New("directory-form bundle withheld: its content does not match what was signed")

// repoFSReader reads ONE bundle out of a tree pinned at a revision:
// ProvenanceRemote, TrustCtxRemote.
//
// It is the only implementation whose signature facts are GATE INPUTS rather
// than diagnostics, because it is the only one whose bytes crossed an
// intermediary. It is also the only one that must be told the trust root twice
// over: without one, no key is trusted and everything it reads is untrusted —
// the fail-toward-less-exposure direction.
type repoFSReader struct {
	tree TreeFS
	ref  string
	cfg  readerConfig
}

// NewRepoFSReader reads the bundle identified by ref out of a pinned tree, and
// does the signature checking.
//
// ref is the bundle's canonical identity (the lockfile key), not a path: one
// lockfile entry is one bundle, and the tree handed in holds that bundle's
// bytes at the revision the lockfile pinned. Provenance and trust context are
// hard-coded remote and are not parameters — a repofs reader that could be
// asked for local-context content would be a trust bypass with a struct literal
// for a weapon.
//
// It reads BOTH bundle forms, dispatching on what the tree IS rather than
// guessing: a tree whose root holds bundle.yaml alongside item directories is
// directory-form and is verified through its signed manifest (attest.
// VerifyBundle, which checks the tree against the manifest in both directions);
// anything else is a single YAML document verified against its detached `.sig`.
// This is not a dual-format fallback — nothing is retried as the other form —
// it is one entry read as the thing it is.
func NewRepoFSReader(tree TreeFS, ref string, opts ...ReaderOption) Reader {
	return &repoFSReader{tree: tree, ref: ref, cfg: newReaderConfig(opts)}
}

// Read reports the bundle this reader was pointed at, with its signature facts
// established. It reports a TAMPERED tree as an ERROR rather than as content:
// a tree whose files no longer match its signed manifest has not been read at
// all — there is no honest set of bytes to report — which is a different
// statement from withholding content that was read.
func (r *repoFSReader) Read(ctx context.Context) ([]BundleRead, error) {
	if r.tree == nil {
		return nil, fmt.Errorf("bundles: repofs reader for %q has no tree", r.ref)
	}
	treeForm, err := r.isTreeForm()
	if err != nil {
		return nil, err
	}
	var read BundleRead
	if treeForm {
		read, err = r.readTreeForm(ctx)
	} else {
		read, err = r.readDocument()
	}
	if err != nil {
		return nil, err
	}
	return []BundleRead{read}, nil
}

// sourceRefTyped mints this reader's structured source ref from r.ref, its
// already-canonical lockfile identity ("<url>@bundles/<path>" or a bare local
// name), through canonicalBundleRefTyped.
func (r *repoFSReader) sourceRefTyped() trust.BundleRef {
	br, err := canonicalBundleRefTyped(r.ref)
	if err != nil {
		warnUnmintableSource(r.ref, err)
		return trust.BundleRef{}
	}
	return br
}

// canonicalBundleRefTyped mints the structured trust.BundleRef for a canonical
// resolution ref of the "<url>@bundles/<path>" / bare-local-name shape —
// the ONE grammar shared by a pinned tree's own ref (repoFSReader.
// sourceRefTyped, both single-file and tree form) and a version-pinned read's
// version-less canonical ref (loader_version.go's bundleAtVersion, whose
// commit-addressed reads carry the SAME identity as their unpinned twin). It
// is the SAME Ref -> BundleRef bridge (trust.Ref.AsBundleRef) that
// trust.ParseItemRef's own base-ref parse feeds for the identical string
// shape — reusing that conversion rather than re-deriving host/path from the
// ref a second, competing way. remote.ParseReference here is not a second
// parser: it is the one parser this ref's grammar has, the same one
// loader_version.go's versionRead already calls on a canonical ref of this
// exact shape.
//
// It returns the ERROR rather than the zero BundleRef alone, and that return is
// load-bearing. A caller that cannot mint here degrades to an unaddressable ref,
// and an unaddressable ref is WITHHELD from delivery — so a swallowed failure
// here is not a missing field, it is content silently vanishing. That is
// exactly how a universal ".git" refusal in the grammar withheld 402 items
// while every package test stayed green: six sites discarded this error, so the
// only surviving evidence was a %#v of a zero struct that named nothing.
// Callers must report what could not be minted; warnUnmintableSource is the
// shared way to do it.
func canonicalBundleRefTyped(canonical string) (trust.BundleRef, error) {
	parsed, err := remote.ParseReference(canonical)
	if err != nil {
		return trust.BundleRef{}, fmt.Errorf("parse %q: %w", canonical, err)
	}
	br, err := trust.Ref{
		RepoURL:     parsed.URL,
		Bundle:      parsed.Path,
		IsLocal:     parsed.IsLocal,
		IsCompanion: parsed.IsCompanion,
	}.AsBundleRef()
	if err != nil {
		return trust.BundleRef{}, fmt.Errorf("convert %q: %w", canonical, err)
	}
	return br, nil
}

// leaf is the last segment of the ref — the name the bundle answers to inside
// the tree it was handed, whether that is a document ("<leaf>.yaml") or a
// directory-form bundle's own directory ("<leaf>/").
func (r *repoFSReader) leaf() string { return path.Base(strings.TrimSuffix(r.ref, "/")) }

// isTreeForm reports whether the tree holds this ref as a DIRECTORY, which is
// what makes a directory-form bundle a directory-form bundle. The tree is
// rooted at the parent in both cases — a bundle id must be a single path
// segment, so a nested ref path is absorbed by the root — which is why the
// question is about an entry in the root rather than about the root itself.
func (r *repoFSReader) isTreeForm() (bool, error) {
	entries, err := r.tree.ReadDir(".")
	if err != nil {
		return false, fmt.Errorf("bundles: listing the pinned tree for %q: %w", r.ref, err)
	}
	for _, e := range entries {
		if e.IsDir && e.Name == r.leaf() {
			return true, nil
		}
	}
	return false, nil
}

// readDocument reads a single-file bundle document out of the tree and
// verifies its detached signature over the exact file bytes, before any parse.
func (r *repoFSReader) readDocument() (BundleRead, error) {
	docPath, err := r.documentPath()
	if err != nil {
		return BundleRead{}, err
	}
	data, err := r.tree.ReadFile(docPath)
	if err != nil {
		return BundleRead{}, fmt.Errorf("bundles: reading %s for %q: %w", docPath, r.ref, err)
	}
	// A missing sibling `.sig` is UNSIGNED, the ordinary case — not an error.
	sig, sigErr := r.tree.ReadFile(docPath + SigSuffix)
	if sigErr != nil {
		sig = nil
	}
	facts := readSignatureFacts(data, sig, r.cfg.root)

	b, err := ParseBundle(data)
	if err != nil {
		return BundleRead{}, fmt.Errorf("bundles: parsing %s for %q: %w", docPath, r.ref, err)
	}
	// Canonical is the sole RESOLUTION identity for pinned content: profiles
	// author canonical refs and resolve straight to this bundle, so sourceRef
	// and the read's ref are the canonical ref unconditionally. Name is the
	// bundle's DECLARED identity and the ref is only its fallback, so a
	// document that named itself keeps that name.
	if b.Name == "" {
		b.Name = r.ref
	}
	b.sourceRef = r.sourceRefTyped()
	b.sourceRefSet = true
	// A document in a pinned tree has no directory of its own, so Path is the
	// synthetic sentinel FSDir refuses rather than a filesystem path it would
	// resolve against the process working directory.
	b.Path = r.syntheticPath()
	facts.stamp(b)
	return newRead(r.ref, b, ProvenanceRemote, TrustCtxRemote, facts), nil
}

// syntheticPath names the bundle's origin in the one field callers look at for
// it, WITHOUT handing them a path they could walk: the ref, and the revision
// its bytes were pinned at when the caller supplied one. FSDir refuses it (see
// nonFilesystemPathPrefixes), which is the point — a remote document has no
// directory, and guessing one resolves against the process working directory.
func (r *repoFSReader) syntheticPath() string {
	if r.cfg.revision == "" {
		return remotePathSentinel + r.ref
	}
	return remotePathSentinel + r.ref + "@" + r.cfg.revision
}

// documentPath finds the single bundle document at the tree root. More than
// one is refused rather than guessed at: this reader was pointed at ONE ref,
// and picking a file to answer to that name is exactly the kind of quiet
// decision a read must not make.
func (r *repoFSReader) documentPath() (string, error) {
	entries, err := r.tree.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("bundles: listing the pinned tree for %q: %w", r.ref, err)
	}
	var docs []string
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".yaml") {
			continue
		}
		// The ref's own leaf answers to the ref, always. Anything else is only
		// a candidate when the tree holds exactly one document.
		if e.Name == r.leaf()+".yaml" {
			return e.Name, nil
		}
		docs = append(docs, e.Name)
	}
	sort.Strings(docs)
	switch len(docs) {
	case 0:
		return "", fmt.Errorf("bundles: the pinned tree for %q holds no bundle document", r.ref)
	case 1:
		return docs[0], nil
	default:
		return "", fmt.Errorf("bundles: the pinned tree for %q holds %d bundle documents (%s); one ref names one bundle",
			r.ref, len(docs), strings.Join(docs, ", "))
	}
}

// readTreeForm reads a directory-form bundle and resolves its publisher
// attestation.
//
// Verification runs over the tree AS INSTALLED, which is strictly stronger than
// trusting the pin: attest.VerifyBundle checks the publisher's signature over
// the manifest AND the tree against that manifest in both directions, so an
// edit to the installed cache is caught. The pin still decides WHICH bytes were
// installed; what changed is that integrity is checked where the bytes are
// actually read from.
func (r *repoFSReader) readTreeForm(ctx context.Context) (BundleRead, error) {
	tree, err := r.openTreeBundle()
	if err != nil {
		return BundleRead{}, err
	}
	facts, err := r.verifyTree(ctx, tree)
	if err != nil {
		return BundleRead{}, err
	}
	b, err := ReadTree(ctx, tree)
	if err != nil {
		return BundleRead{}, fmt.Errorf("bundles: reading the pinned tree for %q: %w", r.ref, err)
	}
	// A declared name wins; the canonical ref is only the fallback identity for
	// a tree that named nothing (see readDocument).
	if b.Name == "" {
		b.Name = r.ref
	}
	b.sourceRef = r.sourceRefTyped()
	b.sourceRefSet = true
	// Path points at the tree's own envelope so FSDir resolves to the installed
	// directory — which is what makes a tree bundle's SKILL packages loadable.
	// Without an installed directory there is nothing on a filesystem to point
	// at, and the sentinel is the honest answer.
	if r.cfg.installDir != "" {
		b.Path = filepath.Join(r.cfg.installDir, DirectoryFormManifest)
	} else {
		b.Path = r.syntheticPath()
	}
	facts.stamp(b)
	return newRead(r.ref, b, ProvenanceRemote, TrustCtxRemote, facts), nil
}

// openTreeBundle opens the tree as a content.Bundle. The store is rooted at the
// tree itself, so the bundle id is the ref's last segment — a bundle id must be
// a single path segment, and a nested ref path is absorbed by the root rather
// than smuggled into the id.
func (r *repoFSReader) openTreeBundle() (content.Bundle, error) {
	store, err := content.NewFSStore(r.tree, content.Provenance{RepoURL: r.cfg.repoURL})
	if err != nil {
		return nil, fmt.Errorf("bundles: opening the pinned tree for %q: %w", r.ref, err)
	}
	id := content.BundleID(path.Base(strings.TrimSuffix(r.ref, "/")))
	tree, err := store.Open(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("bundles: the pinned tree for %q is not readable as bundle %q: %w", r.ref, id, err)
	}
	return tree, nil
}

// verifyTree resolves a directory-form bundle's attestation into the two
// signature axes. A tree that is INTERNALLY inconsistent — signed manifest
// present, files no longer matching it — is a state a single document cannot
// reach, and it is an error rather than an invalid-signature fact: nothing here
// was read as the publisher signed it.
func (r *repoFSReader) verifyTree(ctx context.Context, tree content.Bundle) (signatureFacts, error) {
	verdict, err := attest.VerifyBundle(ctx, tree, r.cfg.root, time.Now())
	if err != nil {
		return signatureFacts{}, fmt.Errorf("bundles: verifying the pinned tree for %q: %w", r.ref, err)
	}
	if verdict.Contents != nil {
		return signatureFacts{}, fmt.Errorf("%w: %q — %v", ErrTreeBundleWithheld, r.ref, verdict.Contents)
	}
	if verdict.Status == attest.StatusTampered {
		return signatureFacts{}, fmt.Errorf("%w: %q — %s", ErrTreeBundleWithheld, r.ref, verdict.Detail)
	}
	if verdict.OK() {
		return signatureFacts{signature: SignatureValid, signer: SignerTrusted, principal: verdict.Principal}, nil
	}
	// Unattested, or attested by a key this trust root does not know: unsigned
	// to us, the ordinary case and the review path — not an error. Carry the
	// display-only fingerprint when the verdict has one, so a human can compare
	// it against what the publisher told them out of band.
	if fp := verdict.UntrustedSignerFingerprint; fp != "" {
		return signatureFacts{signature: SignatureValid, signer: SignerUntrusted, fingerprint: fp}, nil
	}
	return signatureFacts{signature: SignatureNone, signer: SignerNone}, nil
}

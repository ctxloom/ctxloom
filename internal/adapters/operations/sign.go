// Publisher-side signing (signature-envelope spec §7A): resolving a ref to
// the local bundle FILE it names, and signing that file's exact on-disk
// bytes. This file is deliberately separate from trust.go (which owns the
// verification-side ReviewRecords/EffectiveTrust machinery) — it reuses the
// canonical reference grammar (trust.ParseBundleRef, trust.ParseSelector) and
// the shared retired-spelling recognizers rather than duplicating the grammar
// (ADR 0032: one ref grammar).
package operations

import (
	"context"
	"errors"
	"fmt"
	fs2 "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/release"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// SignTarget is a ref resolved down to the local bundle it names. Spec
// §7A.1: a publisher signature covers the whole bundle FILE, so an item ref
// (<bundle>#fragments/x) resolves to its CONTAINING bundle and signs that —
// ItemNote carries what the ref actually named, for the "resolves to
// containing bundle" message the CLI prints.
type SignTarget struct {
	// BundleName is the local bundle name (as bundles.Store.Load expects:
	// no extension, no directory).
	BundleName string
	// ItemNote is "<kind>/<name>" when ref named an item within the bundle
	// ("" when ref was already a bare bundle ref).
	ItemNote string
}

// ResolveSignTarget resolves ref to the local bundle `ctxloom sign` should
// write a `.sig` sibling for, using the canonical bundle-reference grammar
// (trust.ParseBundleRef) plus the item selector every reader shares
// (trust.ParseSelector) — never a second grammar (ADR 0032).
//
// It takes NO CATALOG and resolves nothing, deliberately. `ctxloom sign` runs
// in a publishing repo, where the authored bundle being signed is the repo's
// own content and need not be a member of any resolved set; making signing
// membership-dependent would break it exactly where signing happens. Its
// arms are therefore all local rules:
//
//  1. a canonical URI, judged by its CLASS;
//  2. a retired spelling, refused with the hint that names the current
//     grammar — never resolved, because a hint is not a shim;
//  3. a bare token, which is a local bundle name, optionally carrying an item
//     selector.
//
// Only LOCAL bundles resolve successfully: ctxloom has no write access to a
// remote's git tree (only that remote's own publisher can sign it), and a
// companion loadout is signed where it is built (`just sign-loadouts`), not
// here. Both are reported as clear, actionable errors rather than silently
// skipped; a retired spelling (trust.IsRetiredAskSpelling) is refused by name
// so a stale instruction fails loud instead of resolving to a local bundle.
func ResolveSignTarget(ref string) (SignTarget, error) {
	if ref == "" {
		return SignTarget{}, fmt.Errorf("ref is required")
	}

	if br, err := trust.ParseBundleRef(ref); err == nil {
		switch br.Class {
		case trust.ClassLocal:
			return SignTarget{BundleName: br.Bundle, ItemNote: itemNote(br.Kind, br.Item)}, nil
		default:
			return SignTarget{}, errSignRemote(ref)
		}
	}

	if trust.IsRetiredAskSpelling(ref) {
		return SignTarget{}, fmt.Errorf("ctxloom bundle sign: %w: %q — see `ctxloom bundle sign --help`; "+
			"re-run `ctxloom init` to migrate a project", errs.ErrRetiredRefSpelling, ref)
	}

	base, sel, scoped := strings.Cut(ref, "#")
	if !scoped {
		return SignTarget{BundleName: ref}, nil
	}
	if base == "" {
		return SignTarget{}, fmt.Errorf("ctxloom bundle sign: %q names no bundle before its #<kind>/<name> selector", ref)
	}
	kind, name, err := trust.ParseSelector(sel)
	if err != nil {
		return SignTarget{}, fmt.Errorf("ctxloom bundle sign: invalid ref %q: %w", ref, err)
	}
	return SignTarget{BundleName: base, ItemNote: itemNote(kind, name)}, nil
}

// itemNote renders the "<kind>/<name>" note a SignTarget carries when the ref
// named an item within the bundle, and "" when it named the bundle itself.
func itemNote(kind trust.ItemKind, name string) string {
	if kind == "" && name == "" {
		return ""
	}
	return kind.Dir() + "/" + name
}

// errSignRemote refuses a bundle this project does not author.
func errSignRemote(ref string) error {
	return fmt.Errorf("ctxloom bundle sign: %q does not resolve to a bundle you author locally — "+
		"ctxloom cannot sign a remote bundle's tree from here; only that remote's own publisher can", ref)
}

// errStaleSkillManifests refuses to sign bundleName because one or more of
// its skills' recorded manifests (bundle.yaml's skills.<name>.files) no
// longer match their on-disk source trees. Unconditional: there is no
// --degraded arm here, because signing IS the harm — proceeding would
// produce a signature that validly attests to a manifest already known to be
// false, and every consumer verifying it would trust that false attestation.
// Names every stale skill, not just the first, so one refusal is enough to
// fix all of them.
func errStaleSkillManifests(bundleName string, stale []string) error {
	return fmt.Errorf("ctxloom bundle sign: %s: skill manifest does not match the source tree for %s — "+
		"run `ctxloom skill sync %s` to refresh it before signing "+
		"(signing now would attest to a sha256 the tree does not have, and the bundle would be withheld at materialize)",
		bundleName, strings.Join(stale, ", "), bundleName)
}

// SignBundleRequest is the input to SignBundleFile.
type SignBundleRequest struct {
	Target SignTarget
	// Signer signs the payload; SignBundleFile never resolves one itself
	// (key discovery is internal/adapters/signing/agentkey's job, invoked by the
	// caller) so this function stays pure and trivially testable with a
	// fake signer.
	Signer ssh.Signer
	// SignerSource describes where Signer came from ("git config
	// user.signingkey", "--key", ...), for the refusal AuthorizePublisher
	// raises when the repository does not authorise that key. Optional: the
	// fingerprint is always named, this says which link of the discovery chain
	// produced it — the difference between "wrong key" and "your git config is
	// pointing at your personal key".
	SignerSource string
	// Store overrides the default filesystem bundle store (ADR 0026); nil
	// uses bundles.NewFSStore(fs, cfg.GetBundleDirs()).
	Store bundles.Store
	// FS overrides the default OS filesystem (afero); nil uses
	// afero.NewOsFs().
	FS afero.Fs
}

// SignBundleResult reports what was signed and where the signature landed.
type SignBundleResult struct {
	BundleName string `json:"bundle_name"`
	BundlePath string `json:"bundle_path"`
	// SigPath is where the signature landed: the bundle's .sigs/ STORE
	// DIRECTORY, which holds one entry per (signing key, namespace) — a
	// re-sign by the same key replaces its entry there.
	SigPath string `json:"sig_path"`
	// Tree reports that a DIRECTORY-form bundle was signed as a tree — manifest
	// plus a signature filed against it — rather than as one file's bytes. The
	// two attest different things, and a caller that printed "signed" without
	// saying which would leave an author unable to tell whether their content was
	// covered at all.
	Tree bool `json:"tree"`
	// ManifestPath is the SHA256SUMS the signature covers. Empty unless Tree.
	ManifestPath string `json:"manifest_path"`
	// ItemNote carries SignTarget.ItemNote through, for CLI display.
	ItemNote string `json:"item_note"`
}

// SignBundleFile signs the EXACT bytes of a local bundle file as they exist
// on disk right now (spec §3.1: publisher payload = the bundle file bytes,
// verbatim — nothing prepended, appended, or re-serialized) and writes a
// detached sibling `<path>.sig` (spec §4.2, local filesystem bundles).
//
// Signing failure is always returned as an error — there is no code path
// here that degrades to "wrote the bundle without a .sig"; the whole
// operation either produces a verifiable signature or nothing changes on
// disk.
func SignBundleFile(cfg *config.Config, req SignBundleRequest) (*SignBundleResult, error) {
	if req.Signer == nil {
		return nil, fmt.Errorf("sign %s: no signer supplied", req.Target.BundleName)
	}
	// Checked HERE rather than in the CLI so no signing caller can bypass it —
	// `bundle push --sign` resolves its own key through the same discovery chain
	// and would otherwise inherit the exact defect this refuses. It runs before
	// the bundle is even loaded: the whole point is that nothing is written.
	if err := AuthorizePublisher(cfg, req.FS, req.Signer, req.SignerSource); err != nil {
		return nil, err
	}

	fs := getFS(req.FS)
	// A bundle still carrying the retired sibling signature is REFUSED by
	// every reader (bundles.ErrSiblingSignatureRetired), and re-signing is
	// the remedy the refusal names — so the sibling is cleared BEFORE the
	// bundle is loaded, by path, or the remedy could never run.
	if err := clearRetiredSibling(fs, cfg, req.Target.BundleName); err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}

	store := bundleStore(cfg, req.Store)
	bundle, err := loadBundleForUpdate(store, cfg, req.Target.BundleName)
	if err != nil {
		return nil, err
	}

	if filepath.Base(bundle.Path) != bundles.DirectoryFormManifest {
		return nil, fmt.Errorf("%w: %s is a single-file bundle (%s)", ErrSingleFileBundleUnsignable, req.Target.BundleName, filepath.Base(bundle.Path))
	}
	return signBundleTree(req, bundle, fs)
}

// ErrSingleFileBundleUnsignable: a bundle's ONE signature is the SHA256SUMS
// manifest and its .sigs/ entry, which only a directory-form bundle can
// carry. A single-file bundle that is to be signed takes the tree form first;
// there is no second signature shape for it.
var ErrSingleFileBundleUnsignable = errors.New("sign: only a directory-form bundle (bundle.yaml with its items as files) carries a signature — move the bundle to that form and sign again")

// signBundleTree signs a DIRECTORY-form bundle through its ONE signature: it
// builds the SHA256SUMS manifest over every covered file and files a
// publisher signature against it in the bundle's own .sigs/ store. A retired
// sibling bundle.yaml.sig beside the manifest is REMOVED first: every reader
// refuses a bundle still carrying one (bundles.ErrSiblingSignatureRetired),
// and re-signing is the upgrade path that clears it.
//
// It goes through attest.SignBundle — the same object every reader verifies
// with (attest.VerifyBundle) — rather than assembling a manifest here, so the
// two halves cannot drift into disagreeing about what a signature covers.
//
// attest.SignBundle REFUSES a tree holding files no surface type recognises, and
// that refusal is wanted here: publishing is the last moment a mis-extensioned
// hook is cheap to fix, and the manifest would otherwise happily cover
// `guard.yml` by path and produce a perfectly signed bundle in which the
// guardrail does not exist.
func signBundleTree(req SignBundleRequest, bundle *bundles.Bundle, fs afero.Fs) (*SignBundleResult, error) {
	// Checked BEFORE anything is written: a stale skill
	// manifest means bundle.yaml's skills.<name>.files no longer matches the
	// source tree, so any signature produced from here on attests to a false
	// content hash. `ctxloom skill sync` is the verb that recomputes it — see
	// StaleSkillManifests.
	stale, err := StaleSkillManifests(fs, bundle)
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}
	if len(stale) > 0 {
		return nil, errStaleSkillManifests(req.Target.BundleName, stale)
	}

	manifestPath := bundle.Path
	dir := filepath.Dir(manifestPath)
	store, err := content.NewTreeStore(fs, filepath.Dir(dir), content.Provenance{IsLocal: true})
	if err != nil {
		return nil, fmt.Errorf("sign %s: open the bundle tree at %s: %w", req.Target.BundleName, dir, err)
	}
	ctx := context.Background()
	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	if err != nil {
		return nil, fmt.Errorf("sign %s: open the bundle tree at %s: %w", req.Target.BundleName, dir, err)
	}
	rel, err := bundleRelease(string(tree.ID()), bundle)
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}
	if err := attest.SignBundle(ctx, store, tree, rel, req.Signer); err != nil {
		return nil, fmt.Errorf("sign %s: %w", req.Target.BundleName, err)
	}
	return &SignBundleResult{
		BundleName:   req.Target.BundleName,
		BundlePath:   manifestPath,
		SigPath:      filepath.Join(dir, content.SigDirName),
		Tree:         true,
		ManifestPath: filepath.Join(dir, content.ManifestPath),
		ItemNote:     req.Target.ItemNote,
	}, nil
}

// ErrUnsignableVersion refuses to sign a bundle whose bundle.yaml version is
// not strict semver. The signed version is what every consumer's rollback
// floor is measured in, so a version two consumers could read differently —
// "v1.2", "1.2" — is not one a signature may carry.
var ErrUnsignableVersion = errors.New("sign: bundle.yaml version must be strict semver (MAJOR.MINOR.PATCH)")

// bundleRelease is the release a signature over the bundle named name asserts,
// read from its authored bundle.yaml: the version, and the retractions and
// withdrawal the author wrote there.
func bundleRelease(name string, b *bundles.Bundle) (release.Release, error) {
	v, err := semver.StrictNewVersion(b.Version)
	if err != nil {
		return release.Release{}, fmt.Errorf("%w: %s has version %q", ErrUnsignableVersion, name, b.Version)
	}
	rel := release.Release{Name: name, Version: v, Withdrawn: b.Withdrawn}
	for _, r := range b.Retracts {
		rv, err := semver.StrictNewVersion(r.Version)
		if err != nil {
			return release.Release{}, fmt.Errorf("%w: %s retracts version %q", ErrUnsignableVersion, name, r.Version)
		}
		rel.Retracts = append(rel.Retracts, release.Retraction{Version: rv, Reason: r.Reason})
	}
	return rel, nil
}

// ListLocalBundleNames returns every bundle name found in the project's
// authored bundle directories (cfg.GetBundleDirs() — the committed
// .ctxloom/content/bundles tree) — the set `ctxloom bundle sign --all` signs. In a
// publishing repo that set IS the repo's shipped content, which is the whole
// point: the thing you publish must be the thing you can sign. Remote (seeded)
// and companion bundles are never included, nor is anything in the gitignored
// cache: this project only has write access to its own authored bundle files.
// Sorted for deterministic --all output.
//
// The enumeration MIRRORS bundles.Loader.List: both bundle shapes, walked
// recursively, named by slash-joined path relative to the search dir.
// Listing only top-level `*.yaml` files skipped every DIRECTORY-form bundle —
// which is exactly the shape that can ship skills (skills.go requires
// directory form) — so a bundle with skills was unsignable via --all while
// the command reported success having signed a subset. It is mirrored rather
// than delegated because the loader's walk deliberately swallows read errors
// (a corrupt bundle must not blank a listing), whereas this set decides what
// gets SIGNED: an unreadable authored dir has to stay loud here.
func ListLocalBundleNames(cfg *config.Config, fs afero.Fs) ([]string, error) {
	fs = getFS(fs)
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	// Every FORMAT root of every authored dir, because the bundles directory
	// itself holds no bundles — it is the parent the format roots are siblings
	// under. Walking it directly instead would name each bundle by a path
	// carrying its format segment ("v1/atelier"), and a format-qualified name
	// resolves to nothing: `sign --all` would sign zero bytes and report
	// success. Deriving the roots from paths.BundleLayouts is what keeps this
	// enumeration equal to the loader's rather than merely similar to it.
	var roots []string
	for _, dir := range cfg.GetBundleDirs() {
		for _, l := range paths.BundleLayouts() {
			roots = append(roots, paths.BundlesLayoutRoot(dir, l))
		}
	}
	for _, dir := range roots {
		// An ABSENT dir is legitimately nothing to list; an unreadable one
		// (wrong permissions, a file where a directory should be, an I/O
		// error) is a failure to find out, and swallowing it made a
		// misconfigured GetBundleDirs indistinguishable from an empty
		// project — `sign --all` then reported "no local bundles to sign"
		// and exited 0.
		if _, err := afero.ReadDir(fs, dir); err != nil {
			if errors.Is(err, fs2.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("list local bundles: read %s: %w", dir, err)
		}
		walkErr := afero.Walk(fs, dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			rel, relErr := filepath.Rel(dir, p)
			if relErr != nil {
				return fmt.Errorf("resolve %s under %s: %w", p, dir, relErr)
			}
			hasManifest := false
			if info.IsDir() {
				// An unreadable candidate is a failure to FIND OUT, and this
				// set decides what gets signed: swallowing it would sign a
				// subset and report success, the failure the surrounding
				// function's doc describes.
				ok, existsErr := afero.Exists(fs, paths.BundleManifestPath(p))
				if existsErr != nil {
					return fmt.Errorf("check %s: %w", paths.BundleManifestPath(p), existsErr)
				}
				hasManifest = ok
			}
			// paths.ClassifyBundleWalkEntry owns both halves: which entries are
			// bundles, and — via WalkSkip — that a tree's item files are its
			// CONTENTS and not further bundles. Enumerating them here named
			// "<bundle>/profiles/<x>" as a bundle, and `sign --all` then died
			// resolving a name that cannot exist.
			step := paths.ClassifyBundleWalkEntry(rel, info.IsDir(), hasManifest)
			if step.IsBundle {
				add(step.Name)
			}
			return step.WalkSkip()
		})
		if walkErr != nil {
			return nil, fmt.Errorf("list local bundles: %w", walkErr)
		}
	}
	sort.Strings(names)
	return names, nil
}

// retiredSiblingSuffix is the suffix of the RETIRED detached sibling
// signature (`<file>.yaml.sig`): no reader parses one
// (bundles.ErrSiblingSignatureRetired refuses a bundle carrying it), and
// re-signing removes it (clearRetiredSibling). It is the only spelling left.
const retiredSiblingSuffix = ".sig"

// clearRetiredSibling removes a retired sibling signature (bundle.yaml.sig)
// beside the named authored tree, probing the project's bundle roots by path
// because the reader refuses to read past one. It says what it removed.
func clearRetiredSibling(fs afero.Fs, cfg *config.Config, name string) error {
	if cfg == nil {
		return nil
	}
	for _, dir := range cfg.BundleReaderDirs() {
		sibling := filepath.Join(paths.BundlesLayoutRoot(dir, paths.LayoutV2), name, bundles.DirectoryFormManifest+retiredSiblingSuffix)
		if _, err := fs.Stat(sibling); err != nil {
			continue
		}
		if err := fs.Remove(sibling); err != nil {
			return fmt.Errorf("remove the retired sibling signature %s: %w", sibling, err)
		}
		clidiag.Warn("ctxloom", "removed the retired sibling signature %s — the bundle's signature is its %s entry", sibling, content.SigDirName)
	}
	return nil
}

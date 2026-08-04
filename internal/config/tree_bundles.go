package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/content/attest"
	"github.com/ctxloom/ctxloom/internal/content/convert"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// ErrTreeBundleWithheld reports a directory-form bundle that was read but must
// NOT be used: its signed manifest and its files disagree, or a trusted key's
// signature does not cover the manifest at all.
//
// It is separate from a read failure because the two call for opposite
// responses. A read failure is "this did not arrive"; this is "this arrived and
// is not what the publisher signed", which is a security event and must never
// degrade to the unsigned/review path — degrading it would let an attacker
// downgrade a signed tree by editing one file in the cache.
var ErrTreeBundleWithheld = errors.New("directory-form bundle withheld: its content does not match what was signed")

// seedTreeBundles resolves the entries the byte reader legitimately refused.
//
// remote.BundleReader serves SINGLE-FILE bundles and says so
// (ErrTreeBundleUnreadable) rather than hunting a "<name>.yaml" no publisher
// wrote. Those refusals are not failures — they are the tree-form entries, and
// this is where they get read, through completely different machinery. Any
// OTHER failure is left in the map untouched for reportBundleLoadFailures.
//
// A tree that cannot be read, or that is read and withheld, REPLACES the
// sentinel with its own error, so the user is told what actually went wrong
// instead of "the tree read path does not exist".
func (c *Config) seedTreeBundles(ctx context.Context, lock *remote.Lockfile, root signing.TrustRoot, loaded map[string]*bundles.Bundle, failures map[string]error) {
	for canonical, err := range failures {
		if !errors.Is(err, remote.ErrTreeBundleUnreadable) {
			continue
		}
		entry, ok := lock.Bundles[canonical]
		if !ok {
			continue
		}
		b, signer, terr := c.loadTreeBundle(ctx, canonical, entry, root)
		if terr != nil {
			failures[canonical] = terr
			continue
		}
		// Lockfile keys are canonical refs — the sole seed/resolution identity,
		// exactly as on the single-file path. loadTreeBundle owns Path (it points
		// into the installed tree so skills resolve); identity and the verified
		// signer are stamped here so both paths do it in one place.
		b.Name = canonical
		b.StampSigner(signer) // "" for unsigned; the verified principal otherwise
		loaded[canonical] = b
		delete(failures, canonical)
	}
}

// loadTreeBundle reads one lockfile TREE entry from the tree `remote pull`
// installed, verifies it, and returns the bundle document plus the verified
// publisher principal ("" when the tree carries no trusted attestation).
//
// WHY THE INSTALLED TREE AND NOT THE CLONE AT THE PINNED SHA — the single-file
// path reads its bytes back out of the git clone at entry.SHA, and the obvious
// symmetry would be to walk the tree there too. Two things rule it out:
//
//   - a bundle's SKILLS are files on disk. bundles.Bundle.FSDir has to return a
//     real directory or a skill package is unloadable (it refuses the synthetic
//     "<remote>:…" path outright), and the installed tree is the only real
//     directory a tree bundle has.
//   - verifying the installed tree is STRICTLY STRONGER than trusting the pin.
//     attest.VerifyBundle checks the publisher's signature over the manifest AND
//     the tree against that manifest in both directions, so an edit to the
//     installed cache is caught. The pin cannot see the cache at all.
//
// The pin is not thereby abandoned: it is what `remote pull` fetched at, and it
// still decides WHICH bytes were installed. What changed is that integrity is
// now checked where the bytes are actually read from.
func (c *Config) loadTreeBundle(ctx context.Context, canonical string, entry remote.LockEntry, root signing.TrustRoot) (*bundles.Bundle, string, error) {
	if len(c.appPaths) == 0 {
		return nil, "", fmt.Errorf("no .ctxloom directory configured")
	}
	dir, err := treeBundleDir(c.appPaths[0], canonical)
	if err != nil {
		return nil, "", err
	}

	// Root the store at the tree's PARENT and open it by its own last segment:
	// a bundle id must be a single path segment (content.validateBundleID), and
	// a nested ref path ("lang/go/testing") is absorbed by the root rather than
	// smuggled into the id.
	store, err := content.NewTreeStore(c.getFS(), filepath.Dir(dir), content.Provenance{RepoURL: entry.URL})
	if err != nil {
		return nil, "", fmt.Errorf("open the installed tree for %q at %s: %w", canonical, dir, err)
	}
	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	if err != nil {
		return nil, "", fmt.Errorf("the lockfile records %q as a directory-form bundle but its tree is not installed at %s "+
			"(run `ctxloom remote pull`): %w", canonical, dir, err)
	}

	signer, err := verifyTreeBundle(ctx, tree, canonical, root)
	if err != nil {
		return nil, "", err
	}
	b, err := convert.Read(ctx, tree)
	if err != nil {
		return nil, "", fmt.Errorf("read the installed tree for %q at %s: %w", canonical, dir, err)
	}
	// Path points at the tree's own envelope so FSDir resolves to the installed
	// directory — which is what makes a tree bundle's SKILL packages loadable.
	// A single-file remote bundle gets the synthetic "<remote>:…" sentinel here
	// precisely because it has no directory; a tree does.
	b.Path = filepath.Join(dir, convert.EnvelopePath)
	return b, signer, nil
}

// treeBundleDir resolves the directory `remote pull` installed a tree bundle
// into, from its canonical lockfile key. It goes through the same
// Reference.LocalTreePath the installer used rather than re-assembling the path,
// so a layout change cannot make the writer and the reader disagree.
func treeBundleDir(baseDir, canonical string) (string, error) {
	ref, err := remote.ParseReference(canonical)
	if err != nil {
		return "", fmt.Errorf("invalid lockfile bundle key %q: %w", canonical, err)
	}
	if !ref.IsCanonical() {
		return "", fmt.Errorf("invalid lockfile bundle key %q: not a canonical ref", canonical)
	}
	return ref.LocalTreePath(baseDir), nil
}

// verifyTreeBundle resolves a tree's publisher attestation and decides whether
// its content may be used at all.
//
// The three outcomes mirror the single-file path's (see verifyBundlePublisher),
// with one addition a tree makes possible: a tree can be INTERNALLY
// inconsistent — signed manifest present, files no longer matching it — which a
// single file cannot be. That is withheld, not degraded, for the same reason a
// bad signature is: an attacker who can edit the cache must not be able to turn
// signed content into unsigned-and-therefore-merely-reviewable content.
func verifyTreeBundle(ctx context.Context, tree content.Bundle, canonical string, root signing.TrustRoot) (string, error) {
	verdict, err := attest.VerifyBundle(ctx, tree, root, time.Now())
	if err != nil {
		return "", fmt.Errorf("verify the installed tree for %q: %w", canonical, err)
	}
	if verdict.Contents != nil {
		return "", fmt.Errorf("%w: %q — %v", ErrTreeBundleWithheld, canonical, verdict.Contents)
	}
	if verdict.Status == attest.StatusTampered {
		return "", fmt.Errorf("%w: %q — %s", ErrTreeBundleWithheld, canonical, verdict.Detail)
	}
	if verdict.OK() {
		return verdict.Principal, nil
	}
	// Unattested, or attested by a key this trust root does not know: unsigned to
	// us, which is the ordinary case and the review path — not an error.
	return "", nil
}

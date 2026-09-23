package remote

import (
	"context"
	"fmt"
	"io"

	"github.com/Masterminds/semver/v3"

	"github.com/ctxloom/ctxloom/internal/core/release"
)

// Verified is what verifying a fetched tree established: the release its
// publisher signed, and who that publisher is. Publisher "" means the tree is
// unattested — no key this machine trusts signed it — and Release is then the
// zero value, because an unsigned header is only the repository's say-so.
type Verified struct {
	Release   release.Release
	Publisher string
}

// TreeVerifyFunc verifies a fetched directory-form bundle before a Puller pins
// it. It is wired in from above for the same reason TreeFetchFunc is: the
// verifier (the signed manifest, the trust root) lives in layers remote cannot
// import.
//
// It returns an error for a tree that must not be pinned — tampered, or whose
// files no longer match what its publisher signed — and Verified{} with a nil
// error for a tree nobody this machine trusts signed. Unattested content is
// pinnable (it takes the review path at exposure); tampered content is not.
type TreeVerifyFunc func(ctx context.Context, tree map[string]TreeFile, treeRoot, sha, repoURL string) (Verified, error)

// LockFields renders v as the lockfile's SignedVersion and Publisher: both
// empty for unattested content.
func (v Verified) LockFields() (signedVersion, publisher string) {
	if v.Publisher == "" || v.Release.Version == nil {
		return "", ""
	}
	return v.Release.Version.String(), v.Publisher
}

// AdmitSignedVersion holds next to prior's version floor. It is the ONE place
// every writer of LockEntry.SHA applies release.CheckAdvance, so the floor
// cannot mean one thing to pull and another to upgrade.
//
// allowDowngrade is the operator having named ref; the downgrade is announced
// on w, and the caller records next as the new floor.
func AdmitSignedVersion(w io.Writer, ref string, prior LockEntry, next Verified, allowDowngrade bool) error {
	if prior.SignedVersion == "" {
		return nil
	}
	floor, err := semver.StrictNewVersion(prior.SignedVersion)
	if err != nil {
		return fmt.Errorf("the lockfile records signed_version %q for %s, which is not strict semver, so its floor cannot be honoured: %w", prior.SignedVersion, ref, err)
	}
	nextVersion, _ := next.LockFields()
	var nv *semver.Version
	if nextVersion != "" {
		nv = next.Release.Version
	}
	if err := release.CheckAdvance(floor, nv, allowDowngrade); err != nil {
		return err
	}
	if allowDowngrade && (nv == nil || nv.LessThan(floor)) {
		to := nextVersion
		if to == "" {
			to = "unattested content"
		}
		if w != nil {
			_, _ = fmt.Fprintf(w, "downgrading %s from %s to %s at your request\n", ref, floor, to)
		}
	}
	return nil
}

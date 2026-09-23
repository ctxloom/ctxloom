package operations

import (
	"context"
	"errors"
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/remotetree"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// RefusedAdvance is one pin `deps upgrade` DECLINED to move, because the
// content at the commit it would have advanced to carries a publisher
// signature that does not verify over those bytes.
//
// It is a REPORT, not a failure: the lockfile keeps the entry it already had,
// so the consumer goes on being served the last content that verified. The
// caller is obliged to say so — see the note on UpgradeResult.Refused.
type RefusedAdvance struct {
	// Identity is the canonical ref of the item whose pin was not moved.
	Identity string `json:"identity"`
	// KeptSHA is the commit the pin stays at — the last one that verified.
	KeptSHA string `json:"kept_sha"`
	// ProposedSHA is the commit the constraint resolved to and that was
	// refused.
	ProposedSHA string `json:"proposed_sha"`
	// Detail is the verification failure, in the words of the verifier, so a
	// human is told WHY rather than just that something was refused.
	Detail string `json:"detail"`
	// BelowFloor reports a refusal by the version floor — the proposed content
	// is signed at a lower version than the pin recorded, or is no longer
	// signed — rather than by a signature that fails. The remedies differ: this
	// one the operator can override by naming the ref.
	BelowFloor bool `json:"below_floor,omitempty"`
}

// verifyAdvance decides whether a lock writer may move a pin onto p, and
// reports what verification established so the writer can record it.
//
// THE RULE, decided by the human (taskloom unearned-cornea), is narrower than
// "verify everything": the proposed content is refused when it carries a
// publisher signature that FAILS to verify — the tree read's withheld outcome
// (bundles.ErrTreeBundleWithheld), the one outcome the verifier never calls
// benign. Unsigned content, or content signed by a key this machine does not
// trust, reads as unsigned-to-you and takes the review path. A signature that
// does not cover its bytes is deliberately not reviewable
// (bundles.Reason.NeedsReview), so advancing onto it strands the user with
// nothing — the new copy withheld as tampered and the old copy unreachable
// past the moved pin.
//
// ON TOP of that, a pin that recorded a signed version (prior.SignedVersion) is
// held to it: a lower signed version, or content no longer signed at all, is
// refused unless allowDowngrade (the operator named this ref). That is the
// floor a repository's controller cannot move by re-serving an older tree the
// publisher really did sign. See remote.AdmitSignedVersion.
//
// The check is the ONE verifier every reader uses (attest.VerifyBundle, via
// bundles.ReadRemoteRef): the pull walk and this pre-advance read refuse the
// same things. It is fail-closed on a tamper: a tree that cannot be read at the
// proposed commit refuses the advance. Content that is not there at all
// carries nothing to verify and establishes no release — which
// the floor, when there is one, then refuses as unsigned.
//
// A non-bundle (a remote parent profile) is never refused here: publisher
// signatures cover bundle trees, and a profile has no tree to verify.
//
// A non-nil error is the refusal, in the verifier's own words.
func verifyAdvance(ctx context.Context, cfg *config.Config, factory remote.FetcherFactory, auth remote.AuthConfig, p PinnedRef, prior remote.LockEntry, allowDowngrade bool) (remote.Verified, error) {
	if p.Type != remote.ItemTypeBundle {
		return remote.Verified{}, nil
	}
	ref, err := remote.ParseReference(p.Identity)
	if err != nil || !ref.IsCanonical() {
		return remote.Verified{}, nil
	}
	var v remote.Verified
	_, v, err = bundles.ReadRemoteRef(ctx, factory, auth, ref, p.Hash, remotetree.PullTreeFetcher, cfg.Trust().Root())
	switch {
	case err == nil:
	case errors.Is(err, errs.ErrRemoteContentNotFound) || errors.Is(err, content.ErrNotFound),
		errors.Is(err, bundles.ErrTreeUnattested):
		// None of these is a tamper, and none is more readable at the kept pin
		// than at the proposed one: they establish no release, and the floor
		// below decides.
		v = remote.Verified{}
	default:
		return remote.Verified{}, err
	}
	if err := remote.AdmitSignedVersion(os.Stderr, p.Identity, prior, v, allowDowngrade); err != nil {
		return remote.Verified{}, err
	}
	return v, nil
}

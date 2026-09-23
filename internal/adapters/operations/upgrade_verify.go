package operations

import (
	"context"
	"errors"

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
}

// verifyAdvance decides whether upgrade may move a pin onto ref at proposed.
//
// THE RULE, decided by the human (taskloom unearned-cornea) and narrower than
// "verify everything": an advance is refused when the proposed content
// carries a publisher signature that FAILS to verify — the tree read's
// withheld outcome (bundles.ErrTreeBundleWithheld), the one outcome the
// verifier never calls benign. It is NOT refused for unsigned content or for
// content signed by a key this machine does not trust to publish: those read
// as unsigned-to-you, take the review path, and `ctxloom review` really does
// list them. A signature that does not cover its bytes is deliberately not
// reviewable (bundles.Reason.NeedsReview), so advancing onto it strands the
// user with nothing — the new copy withheld as tampered and the old copy
// unreachable past the moved pin.
//
// The check is the ONE verifier every reader uses (attest.VerifyBundle, via
// bundles.ReadRemoteRef): the pull walk and this pre-advance read refuse the
// same things. It is fail-closed: a tree that cannot be read at the proposed
// commit refuses the advance, because "I could not check" is not "it checks
// out". The exceptions are content that is not there at all, and a
// document-form ref, which carries nothing to verify — neither is this
// check's to decide.
//
// A non-bundle (a remote parent profile) is never refused here: publisher
// signatures cover bundle trees, and a profile has no tree to verify.
func verifyAdvance(ctx context.Context, cfg *config.Config, factory remote.FetcherFactory, auth remote.AuthConfig, p PinnedRef) (detail string, refuse bool) {
	if p.Type != remote.ItemTypeBundle {
		return "", false
	}
	ref, err := remote.ParseReference(p.Identity)
	if err != nil || !ref.IsCanonical() {
		return "", false
	}
	_, err = bundles.ReadRemoteRef(ctx, factory, auth, ref, p.Hash, remotetree.PullTreeFetcher, cfg.Trust().Root())
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, errs.ErrRemoteContentNotFound) || errors.Is(err, content.ErrNotFound):
		return "", false
	case errors.Is(err, bundles.ErrTreeUnattested), errors.Is(err, bundles.ErrDocumentFormUnreadable):
		// Unsigned or signed by a key this machine does not trust, or a
		// document-form ref with nothing to verify: none of these is a
		// tamper, and none is more readable at the kept pin than at the
		// proposed one, so there is nothing this check could strand the user
		// without. They take the review path.
		return "", false
	default:
		return err.Error(), true
	}
}

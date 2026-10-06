package remote

import (
	"context"

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
// pinnable; tampered content is not.
type TreeVerifyFunc func(ctx context.Context, tree map[string]TreeFile, treeRoot, sha, repoURL string) (Verified, error)

package remote

import "github.com/ctxloom/ctxloom/internal/core/release"

// Verified is what verifying a fetched tree established: the release its
// publisher signed, and who that publisher is. Publisher "" means the tree is
// unattested — no key this machine trusts signed it — and Release is then the
// zero value, because an unsigned header is only the repository's say-so.
type Verified struct {
	Release   release.Release
	Publisher string
}

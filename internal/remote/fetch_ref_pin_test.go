package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchRefBytes_AsksTheFetcherForTheSHAItWasGiven pins the security
// property this primitive's own doc claims: "a hash-pinned ref is fully
// self-describing, so reading it needs nothing but the clone at that sha".
//
// The existing coverage guards only the EMPTY sha — that a blank pin is refused
// rather than resolved as latest. Nothing guarded the far likelier defect: a
// sha that is present, correct, and simply not passed on. Measured 2026-08-08 by
// replacing the sha argument at the FetchFile call with "": the full unit suite
// passed and all 386 acceptance scenarios passed, so every hash-pinned read in
// the product could silently degrade to a latest read with nothing to notice.
//
// The acceptance suite cannot cover this and is not the right place to try.
// MockFetcher keys its Files map on PATH ALONE and ignores the ref, so a double
// hands back the same bytes whichever commit is requested — which is a
// reasonable fixture for content tests and structurally blind to this bug. The
// call record is the only place the requested sha survives, so that is what
// this asserts.
func TestFetchRefBytes_AsksTheFetcherForTheSHAItWasGiven(t *testing.T) {
	const pinned = "9f1c2d3e4a5b60718293a4b5c6d7e8f901234567"

	mock := &MockFetcher{Files: map[string][]byte{}}
	factory := func(string, AuthConfig) (Fetcher, error) { return mock, nil }
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}

	// Seed the exact path this ref resolves to, so the fetch succeeds and the
	// assertion below is about WHICH commit was asked for rather than about an
	// error path.
	mock.Files[ref.BuildFilePath(ref.ItemType)] = []byte("bundle: security\n")

	_, err := FetchRefBytes(context.Background(), factory, AuthConfig{}, ref, pinned)
	require.NoError(t, err)

	require.Len(t, mock.FetchFileCalls, 1, "exactly one fetch should have been issued")
	assert.Equal(t, pinned, mock.FetchFileCalls[0].Ref,
		"the pinned sha must reach the fetcher unchanged — an empty or substituted ref resolves to the default branch tip, which is a latest read wearing a pinned read's name")
}

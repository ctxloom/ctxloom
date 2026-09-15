package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchRef_AsksTheFetcherForTheSHAItWasGiven pins the security
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
//
// It moved here from FetchRefBytes when that wrapper was deleted. The assertion
// is unchanged: this file fetch is still the FIRST thing every read does,
// whichever form answers it.
func TestFetchRef_AsksTheFetcherForTheSHAItWasGiven(t *testing.T) {
	const pinned = "9f1c2d3e4a5b60718293a4b5c6d7e8f901234567"

	mock := &MockFetcher{Files: map[string][]byte{}}
	factory := func(string, AuthConfig) (Fetcher, error) { return mock, nil }
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}

	// Seed the exact path this ref resolves to, so the fetch succeeds and the
	// assertion below is about WHICH commit was asked for rather than about an
	// error path.
	mock.Files[ref.BuildFilePath(ref.ItemType)] = []byte("bundle: security\n")

	_, err := FetchRef(context.Background(), factory, AuthConfig{}, ref, pinned, nil)
	require.NoError(t, err)

	require.Len(t, mock.FetchFileCalls, 1, "exactly one fetch should have been issued")
	assert.Equal(t, pinned, mock.FetchFileCalls[0].Ref,
		"the pinned sha must reach the fetcher unchanged — an empty or substituted ref resolves to the default branch tip, which is a latest read wearing a pinned read's name")
}

// TestFetchRef_AsksTheTREEFetcherForTheSHAItWasGiven extends the same property
// to the path that now carries every real read.
//
// The sibling above guards the FILE fetch, which was the whole of the story
// when a bundle could be a document. A bundle is now always a tree, so the
// bytes a session actually receives come through treeFetch — and its sha
// argument was unguarded. That is the same blind spot the sibling exists for,
// one layer along: MockFetcher ignores the ref, so a tree walker handed "" in
// place of the pin would serve identical bytes and no test would notice.
func TestFetchRef_AsksTheTREEFetcherForTheSHAItWasGiven(t *testing.T) {
	const pinned = "9f1c2d3e4a5b60718293a4b5c6d7e8f901234567"

	// Nothing seeded: the file form is absent, so the read falls through to the
	// directory form, which is what every published bundle is.
	mock := &MockFetcher{Files: map[string][]byte{}}
	factory := func(string, AuthConfig) (Fetcher, error) { return mock, nil }
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}

	var gotSHA []string
	treeFetch := func(_ context.Context, _ Fetcher, _, _, root, sha, _ string) (map[string]TreeFile, error) {
		gotSHA = append(gotSHA, sha)
		return map[string]TreeFile{BundleManifestName: {Data: []byte("version: 1.0.0\n")}}, nil
	}

	got, err := FetchRef(context.Background(), factory, AuthConfig{}, ref, pinned, treeFetch)
	require.NoError(t, err)
	require.True(t, got.IsTree(), "an absent file form must resolve as the directory form")

	require.NotEmpty(t, gotSHA, "the tree walker must have been asked for something")
	for _, sha := range gotSHA {
		assert.Equal(t, pinned, sha,
			"the pinned sha must reach the TREE walker unchanged — this is the fetch that delivers a bundle's item files, and an empty ref here is a latest read wearing a pinned read's name")
	}
}

package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchRef_AsksTheTREEFetcherForTheSHAItWasGiven: the bytes a session
// receives come through treeFetch, so its sha argument is the pin. MockFetcher
// ignores the ref, so a tree walker handed "" in place of the pin would serve
// identical bytes and no content test would notice; the call record is the
// only place the requested sha survives.
func TestFetchRef_AsksTheTREEFetcherForTheSHAItWasGiven(t *testing.T) {
	const pinned = "9f1c2d3e4a5b60718293a4b5c6d7e8f901234567"

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
	require.NotNil(t, got.Tree)

	require.NotEmpty(t, gotSHA, "the tree walker must have been asked for something")
	for _, sha := range gotSHA {
		assert.Equal(t, pinned, sha,
			"the pinned sha must reach the TREE walker unchanged — this is the fetch that delivers a bundle's item files, and an empty ref here is a latest read wearing a pinned read's name")
	}
}

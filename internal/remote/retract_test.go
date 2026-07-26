package remote

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckRetracted(t *testing.T) {
	ctx := context.Background()

	t.Run("returns false when no manifest exists", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		// No manifest file set

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("returns false when manifest has no retracted entries", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte("version: 1\n")

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("returns true when item is retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: security
    version: v1.0.0
    reason: "Security vulnerability found"
`)

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.True(t, retracted)
		assert.Equal(t, "Security vulnerability found", reason)
	})

	t.Run("returns true when item retracted without version", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: deprecated-bundle
    reason: "Deprecated, use new-bundle instead"
`)

		ref := &Reference{Path: "deprecated-bundle", ContentVersion: ""}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.True(t, retracted)
		assert.Contains(t, reason, "Deprecated")
	})

	t.Run("returns false when different item is retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: other-bundle
    version: v1.0.0
    reason: "Retracted"
`)

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
	})

	t.Run("returns false when different type is retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: profile
    name: security
    version: v1.0.0
    reason: "Retracted"
`)

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
	})

	t.Run("unversioned request not flagged by a versioned retraction", func(t *testing.T) {
		// A specific version is retracted but the caller installs "latest"
		// (ContentVersion empty). The retracted version is not necessarily the
		// tip, so this must NOT be reported as retracted.
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: security
    version: v1.0.0
    reason: "Broken in v1.0.0"
`)

		ref := &Reference{Path: "security", ContentVersion: ""}
		retracted, reason, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("versioned request not flagged by a different retracted version", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte(`
version: 1
retracted:
  - type: bundle
    name: security
    version: v1.0.0
    reason: "Broken in v1.0.0"
`)

		ref := &Reference{Path: "security", ContentVersion: "v2.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err)
		assert.False(t, retracted)
	})

	// U150-F04: an unparseable manifest used to resolve to "not retracted",
	// silently. That is not a graceful degrade — it is the publisher's
	// withdrawal of content THEY SIGNED losing to their own signature, because
	// the only thing that could have said "withdrawn" was the file we just
	// failed to read. "Could not determine" and "determined: not retracted"
	// are different answers and must not share a return value.
	//
	// This case is unambiguous in a way a FETCH failure is not: the manifest
	// was retrieved, it simply does not parse. There is no reading of that
	// under which the publisher has not retracted anything.
	t.Run("an unparseable manifest is an error, not a clean bill of health", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"
		mf.files[".ctxloom/content/manifest.yaml"] = []byte("invalid: yaml: [[")

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.Error(t, err,
			"a manifest that will not parse was reported as 'not retracted': a publisher's withdrawal "+
				"of content they signed silently loses to their own signature")
		assert.Contains(t, err.Error(), "manifest", "the error must name what could not be read")
		assert.False(t, retracted, "an undetermined verdict must not be reported as a positive retraction either")
	})

	// A repo with no manifest at all is the ordinary case — most remotes
	// publish none — and is legitimately "not retracted", not an error. This
	// pins that the fix above does not swallow it.
	t.Run("a repo with no manifest is legitimately not retracted", func(t *testing.T) {
		mf := newMockFetcher()
		mf.defaultBranch = "main"

		ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
		retracted, _, err := CheckRetracted(ctx, mf, "owner", "repo", ref, ItemTypeBundle)

		require.NoError(t, err, "no manifest is 'nothing to do, legitimately' — not a failure to determine")
		assert.False(t, retracted)
	})
}

// TestConfirmRetraction_PropagatesDeterminationFailure is U150-F04's
// end-to-end half: CheckRetracted's error slot is useless if the caller
// discards it. confirmRetraction used `retracted, reason, _ =`, so even once
// the check could say "I could not determine this", the pull carried on as
// though the answer were "clean".
func TestConfirmRetraction_PropagatesDeterminationFailure(t *testing.T) {
	ctx := context.Background()
	mf := newMockFetcher()
	mf.defaultBranch = "main"
	mf.files[".ctxloom/content/manifest.yaml"] = []byte("invalid: yaml: [[")

	p := &Puller{}
	ref := &Reference{Path: "security", ContentVersion: "v1.0.0"}
	var out bytes.Buffer
	_, _, err := p.confirmRetraction(ctx, mf, "owner", "repo", ref,
		PullOptions{ItemType: ItemTypeBundle, Force: true, Stdout: &out})

	require.Error(t, err,
		"confirmRetraction discarded the determination failure and let the pull proceed as if the content were clean")
}

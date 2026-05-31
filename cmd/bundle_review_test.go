package cmd

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

func TestBundleReviewState(t *testing.T) {
	cs := &operations.BundleChangeSet{
		Added: []operations.BundleChange{
			{Name: "remote-a/foo", Remote: "remote-a", NewSHA: "111"},
		},
		Modified: []operations.BundleChange{
			{Name: "remote-a/bar", Remote: "remote-a", OldSHA: "abc", NewSHA: "def"},
			{Name: "remote-b/baz", Remote: "remote-b", OldSHA: "111", NewSHA: "222"},
		},
	}

	t.Run("set+hasPending+snapshot", func(t *testing.T) {
		s := &bundleReviewState{}
		assert.False(t, s.hasPending())
		s.set(cs)
		assert.True(t, s.hasPending())

		snap := s.snapshot()
		require.NotNil(t, snap)
		assert.Len(t, snap.All(), 3)

		// snapshot must be a copy: mutating it doesn't leak back into state
		snap.Added = nil
		assert.True(t, s.hasPending(), "snapshot must not alias internal slice")
	})

	t.Run("removeBundle drops by name, clears when empty", func(t *testing.T) {
		s := &bundleReviewState{}
		s.set(&operations.BundleChangeSet{
			Added: []operations.BundleChange{{Name: "x/y", Remote: "x"}},
		})
		assert.True(t, s.removeBundle("x/y"))
		assert.False(t, s.hasPending(), "state should be cleared after last entry leaves")
		assert.False(t, s.removeBundle("x/y"), "second remove returns false")
	})

	t.Run("removeRemote drops every entry from that remote", func(t *testing.T) {
		s := &bundleReviewState{}
		s.set(cs)
		removed := s.removeRemote("remote-a")
		assert.Len(t, removed, 2, "remote-a had one Added and one Modified")
		snap := s.snapshot()
		require.NotNil(t, snap)
		assert.Len(t, snap.All(), 1)
		assert.Equal(t, "remote-b/baz", snap.All()[0].Name)
	})

	t.Run("clear is unconditional", func(t *testing.T) {
		s := &bundleReviewState{}
		s.set(cs)
		s.clear()
		assert.False(t, s.hasPending())
	})

	t.Run("set with empty change set clears pending", func(t *testing.T) {
		s := &bundleReviewState{}
		s.set(cs)
		s.set(&operations.BundleChangeSet{})
		assert.False(t, s.hasPending())
	})
}

func TestRenderReviewTemplate(t *testing.T) {
	t.Run("empty returns empty string", func(t *testing.T) {
		assert.Empty(t, renderReviewTemplate(nil))
		assert.Empty(t, renderReviewTemplate(&operations.BundleChangeSet{}))
	})

	t.Run("renders NEW and MODIFIED with reply menu", func(t *testing.T) {
		cs := &operations.BundleChangeSet{
			Added: []operations.BundleChange{
				{Name: "r/new", Remote: "r", NewSHA: "0123456789"},
			},
			Modified: []operations.BundleChange{
				{Name: "r/mod", Remote: "r", OldSHA: "aaaa1111", NewSHA: "bbbb2222"},
			},
		}
		out := renderReviewTemplate(cs)
		assert.Contains(t, out, "⚠️ ctxloom bundle review required")
		assert.Contains(t, out, "NEW:")
		assert.Contains(t, out, "MODIFIED:")
		// shortSHA truncates to 7 chars (existing helper in cmd/remote_update.go).
		assert.Contains(t, out, "r/new @ 0123456 (from r)")
		assert.Contains(t, out, "r/mod aaaa111 → bbbb222 (from r)")
		assert.Contains(t, out, "Reply:")
		assert.Contains(t, out, "A               Approve all")
		// Pin shortcut must be visible alongside the rest of the reply
		// menu so users can freeze a noisy bundle without leaving the
		// review flow.
		assert.Contains(t, out, "P <name>")
		assert.Contains(t, out, "Pin one at its current active SHA")
		// Per-remote batch approval — narrower than T (trust) since it
		// doesn't persist any future-trust setting.
		assert.Contains(t, out, "A <remote>")
		assert.Contains(t, out, "Approve all pending from one remote")
	})
}

func TestAllowedDuringReview(t *testing.T) {
	// Smoke test: the gate's safety hinges on this list being minimal.
	// Tools known to return bundle bytes or execute bundle code must NOT
	// appear in the allowlist.
	blockedTools := []string{
		"assemble_context",
		"get_fragment",
		"get_prompt",
		"search_content",
		"apply_hooks",
		"pull_remote",
		"sync_dependencies",
		"load_session",
		"recover_session",
		"update_remote",
	}
	for _, name := range blockedTools {
		if _, ok := allowedDuringReview[name]; ok {
			t.Errorf("tool %q is in allowedDuringReview but should be blocked", name)
		}
	}

	// Sanity: the review-flow tools must be allowed. pin_bundle and
	// unpin_bundle don't fetch bundle bytes or execute bundle code, so
	// they're safe to permit while pending — and the user needs them
	// to act on the new 'P <name>' template option.
	for _, name := range []string{
		"acknowledge_bundle_review",
		"decline_bundle",
		"show_bundle_verbatim",
		"trust_remote",
		"pin_bundle",
		"unpin_bundle",
		"approve_remote_pending",
	} {
		if _, ok := allowedDuringReview[name]; !ok {
			t.Errorf("review tool %q must be on allowlist", name)
		}
	}
}

func TestBlockedToolResultRendersTemplateAndName(t *testing.T) {
	cs := &operations.BundleChangeSet{
		Added: []operations.BundleChange{{Name: "r/x", Remote: "r", NewSHA: "abc"}},
	}
	result := blockedToolResult("assemble_context", cs)
	require.True(t, result.IsError, "blocked result must signal error")
	require.Len(t, result.Content, 1)

	body := contentText(t, result.Content[0])
	assert.Contains(t, body, "ctxloom bundle review required")
	assert.Contains(t, body, "r/x @ abc")
	assert.Contains(t, body, `tool "assemble_context" is blocked`)
}

// contentText pulls the text field out of an mcp.TextContent without
// importing the SDK's content types into the test surface. Uses the
// wire-format JSON the content marshals to and decodes the "text" key.
func contentText(t *testing.T, c any) string {
	t.Helper()
	marshaller, ok := c.(interface{ MarshalJSON() ([]byte, error) })
	require.True(t, ok, "content does not implement MarshalJSON")
	data, err := marshaller.MarshalJSON()
	require.NoError(t, err)
	var wire struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(data, &wire))
	return wire.Text
}

func TestAllowedDuringReviewSorted_ForGrep(t *testing.T) {
	// Cosmetic: not testing logic, but if the list ever grows past a
	// page, a sorted printout helps reviewers eyeball the surface. Skip
	// failure — this is informational.
	names := make([]string, 0, len(allowedDuringReview))
	for k := range allowedDuringReview {
		names = append(names, k)
	}
	sort.Strings(names)
	t.Logf("allowedDuringReview = %s", strings.Join(names, ", "))
}

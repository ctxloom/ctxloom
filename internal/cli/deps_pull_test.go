package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestRenderPullSummary_SkippedDoesNotClaimCurrency closes a gap: `remote
// pull` reported "Skipped (already installed)" for items whose UPSTREAM
// CONTENT HAD CHANGED. Pull installs exactly the pinned set — moving a pin is
// `deps upgrade`'s job — so the skip is correct, but the wording claimed
// currency the tool had not established, and cost an hour's misdiagnosis.
func TestRenderPullSummary_SkippedDoesNotClaimCurrency(t *testing.T) {
	var out bytes.Buffer
	renderPullSummary(&out, &operations.SyncDependenciesResult{
		Total:   2,
		Skipped: []operations.SyncItem{{Reference: "a"}, {Reference: "b"}},
	})
	text := out.String()

	assert.NotContains(t, text, "already installed",
		`"already installed" reads as "you are current" when upstream may have moved`)
	assert.Contains(t, text, "locked commit", "say what is actually true: the pin was honored")
	assert.Contains(t, text, "upstream changes", "name the thing the user actually cares about")
	assert.Contains(t, text, "ctxloom deps upgrade", "name the command that moves the pin")
}

// TestRenderPullSummary_NothingToPull keeps the empty case intact.
func TestRenderPullSummary_NothingToPull(t *testing.T) {
	var out bytes.Buffer
	renderPullSummary(&out, &operations.SyncDependenciesResult{})
	assert.Contains(t, out.String(), "No remote dependencies to pull.")
}

// A pull that could not fetch or apply a dependency EXITS NON-ZERO. The
// failures are printed to stdout by renderPullSummary, so without this a caller
// scripting on the exit code (rather than scraping stdout) could not tell a
// broken sync from a clean one. pullResultErr is the extracted decision
// runDepsPull defers to. Retracted is deliberately
// NOT a failure here (see the "retracted item does not fail the pull"
// subtest below): it is the retraction mechanism working as designed, and
// the acceptance journeys for it (j001500/j001700/trust_surface) require `deps pull`
// to keep exiting 0 when a dependency is withheld this way — an earlier
// version of this fix treated Retracted as a failure too and broke exactly
// those three scenarios.
func TestPullResultErr(t *testing.T) {
	t.Run("clean pull is nil", func(t *testing.T) {
		assert.NoError(t, pullResultErr(&operations.SyncDependenciesResult{
			Total: 2, Installed: 2,
		}))
	})

	t.Run("skipped-at-locked-commit is not itself a failure", func(t *testing.T) {
		assert.NoError(t, pullResultErr(&operations.SyncDependenciesResult{
			Total: 1, Skipped: []operations.SyncItem{{Reference: "a"}},
		}))
	})

	t.Run("any Errors makes the pull fail", func(t *testing.T) {
		err := pullResultErr(&operations.SyncDependenciesResult{
			Total: 2, Installed: 1, Errors: 1,
			Failed: []operations.SyncItem{{Reference: "b", Error: "boom"}},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "b")
	})

	t.Run("a retracted item does not fail the pull", func(t *testing.T) {
		err := pullResultErr(&operations.SyncDependenciesResult{
			Total: 2, Installed: 1,
			Retracted: []operations.SyncItem{{Reference: "c", Error: "retracted upstream"}},
		})
		assert.NoError(t, err, "retraction is the protective mechanism working, not a pull failure")
	})
}

// --- Phase 2: direct tests for the extracted `remote` RunE bodies -----------

// renderRemoteList's empty arm is the one that used to be a func literal
// nested two levels deep inside a composite literal — unreachable from a test
// without a real config and a real remote store. The guidance it prints is the
// whole output in that case, so it has to name commands that exist.
func TestRenderRemoteList_EmptyStoreGuidanceNamesRealCommands(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderRemoteList(&buf, &operations.ListRemotesResult{Count: 0}))
	got := buf.String()

	assert.Contains(t, got, "No remotes configured.")
	assert.Contains(t, got, "ctxloom remote create")
	assert.Contains(t, got, "ctxloom remote discover")
	assert.NotContains(t, got, "ctxloom remote add",
		"`remote add` was renamed to `remote create`; guidance naming a deleted spelling is worse than none")
}

// The default marker is the only thing distinguishing two otherwise identical
// rows, and `remote default` has no read command of its own — this listing IS
// the read surface for it.
func TestRenderRemoteList_MarksTheDefault(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderRemoteList(&buf, &operations.ListRemotesResult{
		Count:   2,
		Default: "origin",
		Remotes: []operations.RemoteEntry{
			{Name: "origin", URL: "https://example.test/a"},
			{Name: "other", URL: "https://example.test/b"},
		},
	}))
	got := buf.String()

	assert.Contains(t, got, "Configured remotes:")
	assert.Regexp(t, `origin\s+https://example\.test/a \(default\)`, got)
	assert.Regexp(t, `other\s+https://example\.test/b\n`, got)
	assert.Equal(t, 1, strings.Count(got, "(default)"), "exactly one remote is the default")
}

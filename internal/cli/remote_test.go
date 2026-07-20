package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestRenderPullSummary_SkippedDoesNotClaimCurrency closes rare-vixen: `remote
// pull` reported "Skipped (already installed)" for items whose UPSTREAM
// CONTENT HAD CHANGED. Pull installs exactly the pinned set — moving a pin is
// `remote upgrade`'s job — so the skip is correct, but the wording claimed
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
	assert.Contains(t, text, "ctxloom remote upgrade", "name the command that moves the pin")
}

// TestRenderPullSummary_NothingToPull keeps the empty case intact.
func TestRenderPullSummary_NothingToPull(t *testing.T) {
	var out bytes.Buffer
	renderPullSummary(&out, &operations.SyncDependenciesResult{})
	assert.Contains(t, out.String(), "No remote dependencies to pull.")
}

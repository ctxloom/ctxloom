package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

const (
	aliceDemo    = "https://github.com/alice/ctxloom@bundles/demo"
	corpSecurity = "https://github.com/corp/ctxloom@bundles/security"
)

// renderReconcile must NAME what it removed. A pull that silently prunes is
// indistinguishable from a pull that found nothing to do, and the user only
// learns which by missing the content later.
func TestRenderReconcile_NamesEveryRemoval(t *testing.T) {
	var b testWriter

	renderReconcile(&b, operations.ReconcilePlan{Gone: []trust.BundleKey{aliceDemo, corpSecurity}})

	assert.Contains(t, b.String(), "no longer published")
	assert.Contains(t, b.String(), aliceDemo, "a removal nobody can name is a removal nobody can undo")
	assert.Contains(t, b.String(), corpSecurity)
}

// The unchecked half has to be as loud as the removed half. "I could not reach
// this remote" is the sentence that stops a user concluding their installation
// was verified against upstream when it was not.
func TestRenderReconcile_SaysWhatItCouldNotCheck(t *testing.T) {
	var b testWriter

	renderReconcile(&b, operations.ReconcilePlan{Unreachable: []operations.UncheckedRemote{{
		URL:    "https://github.com/alice/ctxloom",
		Refs:   []trust.BundleKey{aliceDemo},
		Reason: "authentication failed",
	}}})
	out := b.String()

	assert.Contains(t, out, "could not be reached")
	assert.Contains(t, out, "https://github.com/alice/ctxloom")
	assert.Contains(t, out, "authentication failed")
	assert.NotContains(t, out, "no longer published",
		"nothing was found gone, so nothing may be described as gone")
}

// Silence when there is nothing to say: a pull against a healthy closure must
// not print a reconciliation section at all.
func TestRenderReconcile_AnEmptyPlanIsSilent(t *testing.T) {
	var b testWriter

	renderReconcile(&b, operations.ReconcilePlan{})

	assert.Empty(t, b.String())
}

// testWriter is a minimal io.Writer with a String(), so these tests do not
// depend on any fixture.
type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *testWriter) String() string              { return string(w.b) }

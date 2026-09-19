package bundles

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestCanonicalBundleRefTyped_ReportsWhatItCouldNotMint is the regression guard
// for the failure that caused the U3b-2 revert.
//
// A source ref the grammar cannot convert degrades to the zero BundleRef, which
// degrades to an unaddressable item ref, which the trust gate WITHHOLDS. That
// chain is fail-CLOSED and stays, but it used to be fail-SILENT: every mint site
// wrote `if typed, err := mint(...); err == nil` and dropped the error, so 402
// items vanished from delivery and the only evidence was a %#v of an all-empty
// struct naming neither the bundle nor the offending string.
//
// The assertions are on the STRING and the REASON, not on the fact that
// something was logged. "A warning was emitted" is satisfied by any warning,
// including one about an unrelated bundle; what makes the diagnosis one grep
// instead of a bisect is that the message names the input that failed.
func TestCanonicalBundleRefTyped_ReportsWhatItCouldNotMint(t *testing.T) {
	t.Run("the error names the ref and the failure stage", func(t *testing.T) {
		_, err := canonicalBundleRefTyped("::not a reference::")
		require.Error(t, err, "an unconvertible ref must not be reported as a successful mint")

		// HasPrefix on THIS function's own wrapping, not Contains on the
		// message. remote.ParseReference already quotes the ref in its own
		// error, so a Contains check passes whether or not this function names
		// anything — it was written that way first and a mutation proved it
		// vacuous. The stage word and the %q here are what this function
		// contributes, and they are what tells a reader which of the two mint
		// stages failed.
		assert.True(t, strings.HasPrefix(err.Error(), `parse "::not a reference::":`),
			"the error does not carry this function's own naming: %v", err)
	})

	// The convert stage (Ref.AsBundleRef) has no test here, deliberately and not
	// by omission: every malformed input tried reaches remote.ParseReference
	// first and is refused there, so the arm is unreachable through this
	// function's grammar. It is guarded by construction rather than by a
	// scenario, and inventing an input that "looks like" it would assert
	// nothing. If ParseReference ever loosens, this note is the thing to
	// revisit.

	t.Run("a mintable ref still returns no error", func(t *testing.T) {
		br, err := canonicalBundleRefTyped("https://github.com/acme/repo@bundles/tooling")
		require.NoError(t, err)
		assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/tooling", br.String())
	})

	t.Run("the warning reaches the user naming the source and the consequence", func(t *testing.T) {
		var sink bytes.Buffer
		t.Cleanup(clidiag.SetSink(&sink))

		warnUnmintableSource("file:///srv/broken@bundles/x", assert.AnError)

		got := sink.String()
		assert.Contains(t, got, "file:///srv/broken@bundles/x",
			"the warning does not name the source, so a reader cannot tell WHICH bundle vanished")
		assert.Contains(t, got, assert.AnError.Error(),
			"the warning does not carry the reason, so it cannot be acted on")
		assert.True(t, strings.Contains(got, "withheld"),
			"the warning does not state the CONSEQUENCE; a user seeing it must know content is missing, not just that a ref was odd")
	})
}

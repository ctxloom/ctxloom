package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// A `ctxloom:local@…` reference PARSES cleanly and carries no repository URL,
// so it is the input that reaches both of the single-ref path's reference
// guards. Whichever guard a caller happens to hit decides what the user is
// told about the same string, so the two must agree on one message — and it
// must be the one that names the actual problem (no repository URL), not the
// generic "invalid reference", which sends the reader looking for a syntax
// error that isn't there.
func TestSingleUpdateRefGuard_OneMessageForANoURLReference(t *testing.T) {
	const noURLRef = "ctxloom:local@bundles/x"

	parsed, perr := remote.ParseReference(noURLRef)
	require.NoError(t, perr, "this input must PARSE — the point is that it parses and still has no URL")
	require.Empty(t, parsed.URL)

	_, err := parseCheckRef(noURLRef)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reference has no repository URL",
		"the no-URL rejection must name the missing URL, not report a parse failure")
	assert.Contains(t, err.Error(), noURLRef, "the message must name the reference it rejected")
}

// A genuinely unparseable reference keeps the parser's own explanation, which
// carries the accepted forms.
func TestSingleUpdateRefGuard_UnparseableKeepsParserMessage(t *testing.T) {
	_, err := parseCheckRef("::::not-a-valid-reference")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid reference")
}

// detectSingleUpdate takes an already-validated reference, so the guard above
// is the single rejection point on this path: the resolution step can no longer
// be handed a string it has to re-validate with its own opinion.
func TestDetectSingleUpdate_TakesAValidatedReference(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.DefaultBranch = "main"
	mock.Refs = map[string]string{"main": "mainsha"}

	const refStr = "https://github.com/o/r@bundles/x"
	ref, err := parseCheckRef(refStr)
	require.NoError(t, err)

	var out strings.Builder
	u, upToDate, err := detectSingleUpdate(context.Background(), &out, mock, &remote.Lockfile{}, ref, refStr)
	require.NoError(t, err)
	assert.False(t, upToDate)
	assert.Equal(t, "mainsha", u.LatestSHA)
}

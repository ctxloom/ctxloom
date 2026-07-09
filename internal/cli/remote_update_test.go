// Tests for the pure helpers in cmd/remote_update.go. The cobra
// RunE bodies do network IO (forge fetcher, puller) and can't be
// unit-tested without a sizable refactor; the extracted pullOutcome
// classifier and shortSHA helper carry the testable decision logic.
package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// =============================================================================
// shortSHA
// =============================================================================

func TestShortSHA_TruncatesAtSeven(t *testing.T) {
	assert.Equal(t, "abc1234", shortSHA("abc12345def"))
}

func TestShortSHA_PreservesShorterInput(t *testing.T) {
	assert.Equal(t, "abc", shortSHA("abc"))
}

func TestShortSHA_ExactlySevenStaysUnchanged(t *testing.T) {
	// 7-char SHA hits the `len > 7` boundary on the false side: no truncation.
	assert.Equal(t, "abc1234", shortSHA("abc1234"))
}

func TestShortSHA_Empty(t *testing.T) {
	assert.Equal(t, "", shortSHA(""))
}

// =============================================================================
// classifyPullError
// =============================================================================

func TestClassifyPullError_Skipped(t *testing.T) {
	// A cancellation reads as a user-initiated skip. It's now identified by the
	// errs.ErrCancelled sentinel (via errors.Is), not by substring, so it
	// survives arbitrary wrapping.
	cases := []error{
		errs.ErrCancelled,
		fmt.Errorf("installation cancelled: %w", errs.ErrCancelled),
		fmt.Errorf("puller: %w", fmt.Errorf("overwrite cancelled: %w", errs.ErrCancelled)),
	}
	for _, e := range cases {
		t.Run(e.Error(), func(t *testing.T) {
			assert.Equal(t, pullOutcomeSkipped, classifyPullError(e))
		})
	}
}

func TestClassifyPullError_Removed(t *testing.T) {
	// Forge-layer "not found" is identified by the errs.ErrRemoteContentNotFound
	// sentinel (via errors.Is), which fetchers wrap around their 404 errors.
	cases := []error{
		errs.ErrRemoteContentNotFound,
		fmt.Errorf("file not found: owner/repo/x: %w", errs.ErrRemoteContentNotFound),
		fmt.Errorf("puller: %w", fmt.Errorf("ref not found: v9: %w", errs.ErrRemoteContentNotFound)),
	}
	for _, e := range cases {
		t.Run(e.Error(), func(t *testing.T) {
			assert.Equal(t, pullOutcomeRemoved, classifyPullError(e))
		})
	}
}

func TestClassifyPullError_FailedByDefault(t *testing.T) {
	// Anything that doesn't match the known substrings is a generic failure
	// the update report should count as failed.
	cases := []error{
		errors.New("connection refused"),
		errors.New("permission denied"),
		errors.New("authentication required"),
	}
	for _, e := range cases {
		t.Run(e.Error(), func(t *testing.T) {
			assert.Equal(t, pullOutcomeFailed, classifyPullError(e))
		})
	}
}

func TestClassifyPullError_PrecedenceCancelledBeforeRemoved(t *testing.T) {
	// If both signals are present, cancelled wins (it's the more specific
	// user-action signal). Pin this so a future refactor that re-orders the
	// checks doesn't silently change behavior.
	err := fmt.Errorf("file not found: %w", errs.ErrCancelled)
	assert.Equal(t, pullOutcomeSkipped, classifyPullError(err))
}

func TestClassifyPullError_PlainTextWithoutSentinelIsFailed(t *testing.T) {
	// classifyPullError now matches sentinels via errors.Is, not error text.
	// A plain error that merely mentions "cancelled" or "not found" — but
	// doesn't wrap a sentinel — falls through to Failed.
	assert.Equal(t, pullOutcomeFailed, classifyPullError(errors.New("operation cancelled by user")))
	assert.Equal(t, pullOutcomeFailed, classifyPullError(errors.New("file not found")))
}

func TestClassifyPullError_NilGuards(t *testing.T) {
	// Caller shouldn't ask in the success path, but the helper must not
	// panic if it does. Returning Failed is the safe sentinel.
	assert.Equal(t, pullOutcomeFailed, classifyPullError(nil))
}

// =============================================================================
// Sanity: classify works with wrapped errors at any depth
// =============================================================================

func TestClassifyPullError_DeeplyWrapped(t *testing.T) {
	inner := fmt.Errorf("file not found: %w", errs.ErrRemoteContentNotFound)
	mid := fmt.Errorf("fetcher: %w", inner)
	outer := fmt.Errorf("puller: %w", mid)
	assert.Equal(t, pullOutcomeRemoved, classifyPullError(outer))
	// Sanity: errors.Is finds the sentinel through arbitrary wrapping.
	assert.True(t, errors.Is(outer, errs.ErrRemoteContentNotFound))
}

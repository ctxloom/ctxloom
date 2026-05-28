// Tests for the pure helpers in cmd/remote_update.go. The cobra
// RunE bodies do network IO (forge fetcher, puller) and can't be
// unit-tested without a sizable refactor; the extracted pullOutcome
// classifier and shortSHA helper carry the testable decision logic.
package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	// Anything mentioning "cancelled" reads as a user-initiated skip
	// (interactive confirmation declined, ctx canceled).
	cases := []error{
		errors.New("operation cancelled by user"),
		errors.New("pull cancelled"),
		fmt.Errorf("wrapped: %w", errors.New("user cancelled review")),
	}
	for _, e := range cases {
		t.Run(e.Error(), func(t *testing.T) {
			assert.Equal(t, pullOutcomeSkipped, classifyPullError(e))
		})
	}
}

func TestClassifyPullError_Removed(t *testing.T) {
	// Forge-layer "not found" surfaces as either of these messages.
	cases := []error{
		errors.New("file not found"),
		errors.New("github API returned 404"),
		errors.New("404 Not Found"),
		fmt.Errorf("wrapped: %w", errors.New("file not found at path")),
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
	// switch doesn't silently change behavior.
	err := errors.New("cancelled: file not found")
	assert.Equal(t, pullOutcomeSkipped, classifyPullError(err))
}

func TestClassifyPullError_CaseSensitive(t *testing.T) {
	// classifyPullError uses strings.Contains, which is case-sensitive.
	// Document that here so future-you sees the expected behavior on
	// capitalized variants — they fall through to Failed.
	assert.Equal(t, pullOutcomeFailed, classifyPullError(errors.New("CANCELLED")))
	assert.Equal(t, pullOutcomeFailed, classifyPullError(errors.New("File Not Found")))
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
	inner := errors.New("file not found")
	mid := fmt.Errorf("fetcher: %w", inner)
	outer := fmt.Errorf("puller: %w", mid)
	assert.Equal(t, pullOutcomeRemoved, classifyPullError(outer))
	// Sanity: also check the substring really survives wrapping (this is
	// the contract that justifies the substring-match design).
	assert.True(t, strings.Contains(outer.Error(), "file not found"))
}

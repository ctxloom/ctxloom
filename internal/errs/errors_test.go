package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentinelErrors_ErrorsIs(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
		wrapped  error
	}{
		{
			name:     "ErrBundleNotFound",
			sentinel: errs.ErrBundleNotFound,
			wrapped:  fmt.Errorf("loading config: %w", errs.ErrBundleNotFound),
		},
		{
			name:     "ErrFragmentNotFound",
			sentinel: errs.ErrFragmentNotFound,
			wrapped:  fmt.Errorf("assembling context: %w", errs.ErrFragmentNotFound),
		},
		{
			name:     "ErrCommandNotFound",
			sentinel: errs.ErrCommandNotFound,
			wrapped:  fmt.Errorf("loading command: %w", errs.ErrCommandNotFound),
		},
		{
			name:     "ErrProfileNotFound",
			sentinel: errs.ErrProfileNotFound,
			wrapped:  fmt.Errorf("resolving profile: %w", errs.ErrProfileNotFound),
		},
		{
			name:     "ErrRemoteNotFound",
			sentinel: errs.ErrRemoteNotFound,
			wrapped:  fmt.Errorf("fetching remote: %w", errs.ErrRemoteNotFound),
		},
		{
			name:     "ErrCircularInheritance",
			sentinel: errs.ErrCircularInheritance,
			wrapped:  fmt.Errorf("profile 'base' inherits from 'child': %w", errs.ErrCircularInheritance),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Direct comparison
			assert.True(t, errors.Is(tt.sentinel, tt.sentinel), "sentinel should match itself")

			// Wrapped error should match
			assert.True(t, errors.Is(tt.wrapped, tt.sentinel), "wrapped error should match sentinel")

			// Different sentinels should not match
			for _, other := range tests {
				if other.name != tt.name {
					assert.False(t, errors.Is(tt.sentinel, other.sentinel),
						"%s should not match %s", tt.name, other.name)
				}
			}
		})
	}
}

func TestSentinelErrors_ErrorMessages(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{errs.ErrBundleNotFound, "bundle not found"},
		{errs.ErrFragmentNotFound, "fragment not found"},
		{errs.ErrCommandNotFound, "command not found"},
		{errs.ErrProfileNotFound, "profile not found"},
		{errs.ErrRemoteNotFound, "remote not found"},
		{errs.ErrRemoteContentNotFound, "remote content not found"},
		{errs.ErrCircularInheritance, "circular profile inheritance detected"},
		{errs.ErrProfileDepthExceeded, "profile inheritance depth exceeds maximum"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestSentinelErrors_DeepWrapping(t *testing.T) {
	// Test that errors.Is works through multiple layers of wrapping
	original := errs.ErrBundleNotFound
	wrapped1 := fmt.Errorf("layer 1: %w", original)
	wrapped2 := fmt.Errorf("layer 2: %w", wrapped1)
	wrapped3 := fmt.Errorf("layer 3: %w", wrapped2)

	assert.True(t, errors.Is(wrapped3, errs.ErrBundleNotFound))
	assert.Contains(t, wrapped3.Error(), "layer 3")
	assert.Contains(t, wrapped3.Error(), "bundle not found")
}

// A sentinel is rendered LAST when a producer wraps it, so its own message is
// the final clause a user reads. A message that names only the outcome and not
// its subject therefore ends every error in that family with a word that says
// nothing: "file not found: acme/tools/x.md: not found". Every sentinel here
// must name what it is about.
func TestSentinelErrors_EveryMessageNamesItsSubject(t *testing.T) {
	generic := map[string]bool{
		"not found": true, "error": true, "failed": true, "invalid": true,
		"missing": true, "unavailable": true, "withheld": true,
	}
	sentinels := []error{
		errs.ErrBundleNotFound, errs.ErrFragmentNotFound, errs.ErrCommandNotFound,
		errs.ErrSkillNotFound, errs.ErrProfileNotFound, errs.ErrBadItemRef,
		errs.ErrFragmentWithheld, errs.ErrCommandWithheld, errs.ErrSkillWithheld,
		errs.ErrNoVersionResolver,
		errs.ErrRemoteNotFound, errs.ErrRemoteContentNotFound, errs.ErrRemoteNotMaterialized,
		errs.ErrCircularInheritance, errs.ErrProfileDepthExceeded, errs.ErrCancelled,
	}
	for _, sentinel := range sentinels {
		msg := sentinel.Error()
		require.NotEmpty(t, msg)
		assert.False(t, generic[msg],
			"sentinel message %q names an outcome but no subject; it reads as the last clause of every error that wraps it", msg)
	}
}

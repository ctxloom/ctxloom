package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The per-argument cap is Linux's MAX_ARG_STRLEN (32 pages) minus the NUL the
// kernel counts; every other platform declares no per-argument cap, so the
// check is disabled there rather than refusing prompts that genuinely exec.
func TestSingleArgLimit_OnlyLinuxCapsOneArgument(t *testing.T) {
	assert.Equal(t, 131071, singleArgLimit(true, 4096))
	assert.Zero(t, singleArgLimit(false, 4096))
	assert.Zero(t, singleArgLimit(true, 0), "an unknown page size must disable the check, not cap at -1")
}

// A prompt one byte over the limit is refused BY NAME, as the prompt, with its
// length; a prompt exactly at the limit passes through untouched.
func TestCheckArgvLimit_RefusesAnOversizedPromptByName(t *testing.T) {
	const limit = 16
	atLimit := strings.Repeat("p", limit)
	require.NoError(t, checkArgvLimit("claude-code", []string{"--", atLimit}, atLimit, limit))

	over := atLimit + "p"
	err := checkArgvLimit("claude-code", []string{"--name", "x", "--", over}, over, limit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the prompt is 17 bytes")
	assert.Contains(t, err.Error(), "longer than 16 bytes")
}

// An oversized argument that is NOT the prompt is still refused, but named by
// its argv index rather than misattributed to the prompt.
func TestCheckArgvLimit_NamesANonPromptArgumentByIndex(t *testing.T) {
	err := checkArgvLimit("claude-code", []string{"--flag", strings.Repeat("v", 17)}, "short prompt", 16)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command-line argument 1 is 17 bytes")
	assert.NotContains(t, err.Error(), "the prompt")
}

func TestCheckArgvLimit_ZeroLimitDisablesTheCheck(t *testing.T) {
	assert.NoError(t, checkArgvLimit("claude-code", []string{strings.Repeat("v", 1<<20)}, "", 0))
}

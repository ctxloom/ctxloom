package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// SentinelFail's response used to be the constant "mock-engine: fail sentinel
// matched". A constant is precisely what this package's own class gate
// (arch_test.go) forbids: "a limb that renders identically either way is not
// evidence". A fixed failure string is produced identically by a run that
// received a full composed context and by one that received nothing at all, so
// a test asserting it cannot fail for the reason it exists.
//
// The response now carries backends.MockFailPrefix followed by the OBSERVED
// prompt, which is what makes a negative scenario assertable positively.

// TestDispatch_FailSentinel_CarriesTheObservedPrompt pins the marker and the
// observed value together. Either alone is insufficient — see the file comment.
func TestDispatch_FailSentinel_CarriesTheObservedPrompt(t *testing.T) {
	prompt := "composed-context OBSERVED-VALUE-9f42\n" + SentinelFail + "\ntrailing task text"

	out, err := Dispatch(prompt, nil)
	require.NoError(t, err)

	require.Equal(t, failExitCode, out.ExitCode,
		"the fail sentinel still drives the nonzero fault path")
	assert.True(t, strings.HasPrefix(out.Response, backends.MockFailPrefix),
		"the failure marker must lead the response, got %q", out.Response)
	assert.Contains(t, out.Response, "OBSERVED-VALUE-9f42",
		"the response must carry what the engine actually received; a constant proves the fault path fired but not that anything reached the engine")
}

// TestDispatch_FailSentinel_RendersDifferentlyPerPrompt is the mutation-resistant
// half, and the one a revert to a constant response cannot survive. It asserts
// the property directly rather than any particular string: two runs that
// observed DIFFERENT input must produce DIFFERENT evidence.
func TestDispatch_FailSentinel_RendersDifferentlyPerPrompt(t *testing.T) {
	a, err := Dispatch(SentinelFail+" INPUT-AAAA-1111", nil)
	require.NoError(t, err)
	b, err := Dispatch(SentinelFail+" INPUT-BBBB-2222", nil)
	require.NoError(t, err)

	require.Equal(t, failExitCode, a.ExitCode)
	require.Equal(t, failExitCode, b.ExitCode)
	assert.NotEqual(t, a.Response, b.Response,
		"two fail runs that observed different prompts must not render identically — if they do, the response is a constant and carries no evidence of delivery")
}

// TestDispatch_EnvResponse_StillOverridesTheFailSentinel guards the documented
// precedence: an explicit env knob is a deliberate, unambiguous request from a
// test that owns the child's environment, and it still wins over the sentinel's
// response. Without this, widening the sentinel's response could silently
// capture the override path.
func TestDispatch_EnvResponse_StillOverridesTheFailSentinel(t *testing.T) {
	out, err := Dispatch(SentinelFail+" OBSERVED-VALUE-9f42",
		envMap(map[string]string{EnvResponse: "explicit-override"}))
	require.NoError(t, err)

	assert.Equal(t, "explicit-override", out.Response,
		"an explicit CTXLOOM_MOCK_RESPONSE still replaces the sentinel's response")
	assert.Equal(t, failExitCode, out.ExitCode,
		"overriding the response must not disturb the sentinel's exit code")
}

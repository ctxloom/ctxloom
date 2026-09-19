package backends

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// CTXLOOM_MOCK_FAIL_PREFIX exists so a NEGATIVE scenario can assert POSITIVELY.
//
// The alternative it replaces is asserting an absence ("the output does not
// contain X"), which this project's acceptance audit repeatedly found to be
// vacuous: an absence is satisfied just as well by an engine that never
// launched, never received context, and wrote zero bytes — the characteristic
// silent no-op. A response that carries the failure marker AND the observed
// context can only be produced by a run that actually reached the engine and
// actually delivered the value.
//
// The three tests below are deliberately one contract each, and the pair of
// "prefix present" / "prefix absent" is load-bearing: per the mock's class gate
// (internal/engines/mock/runtime/arch_test.go) a limb of evidence must be able to say NO,
// so the run where the knob was set and the run where it was not have to render
// DIFFERENTLY. A test for only the set case would still pass against a mock
// that prefixed unconditionally.

// TestMock_FailPrefix_KeepsTheObservedContext is the core guard. It asserts the
// marker and the verbatim observed context IN ONE OUTPUT, because that
// conjunction is the entire point: either half alone proves nothing. The marker
// alone is a constant the mock could emit having received nothing; the context
// alone is the pre-existing echo.
func TestMock_FailPrefix_KeepsTheObservedContext(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()

	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "FAILVALUE-MARKER-7c31"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	res, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env:    map[string]string{"CTXLOOM_MOCK_FAIL_PREFIX": "1"},
	}, &out, &out)
	require.NoError(t, err)
	require.Equal(t, int32(0), res.ExitCode,
		"the prefix annotates the RESPONSE; the exit code stays CTXLOOM_MOCK_EXIT_CODE's business")

	echo := out.String()
	assert.True(t, strings.HasPrefix(echo, MockFailPrefix+"\n"),
		"the failure marker must lead the response, got %q", echo)
	assert.Contains(t, echo, "[mock] context=FAILVALUE-MARKER-7c31",
		"the observed context must SURVIVE the prefix — a marker without the value it observed is a constant, and a constant cannot distinguish a delivered run from an empty one")
	assert.Contains(t, echo, "[mock] prompt=do the thing",
		"the prompt echo must survive too: the prefix is additive, not a replacement")
}

// TestMock_FailPrefix_AbsentKnob_RendersDifferently is the "can say NO" half.
// Without it, a mock that emitted the marker unconditionally would satisfy the
// test above and the evidence would be worthless.
func TestMock_FailPrefix_AbsentKnob_RendersDifferently(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()

	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "FAILVALUE-MARKER-7c31"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	_, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env:    map[string]string{},
	}, &out, &out)
	require.NoError(t, err)

	echo := out.String()
	assert.False(t, strings.HasPrefix(echo, MockFailPrefix),
		"an unset knob must not produce the failure marker, else set and unset runs are indistinguishable")
	assert.Contains(t, echo, "[mock] context=FAILVALUE-MARKER-7c31",
		"the ordinary echo is unchanged when the knob is absent")
}

// TestMock_FailPrefix_IsOrthogonalToCustomResponse pins the deliberate
// interaction with CTXLOOM_MOCK_RESPONSE. That knob REPLACES the response, so a
// test using it alone to signal failure can only assert a literal it wrote
// itself. The prefix applies on top of it rather than being swallowed by it, so
// the two knobs compose instead of one silently winning.
func TestMock_FailPrefix_IsOrthogonalToCustomResponse(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()

	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "ignored-by-custom-response"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	_, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env: map[string]string{
			"CTXLOOM_MOCK_FAIL_PREFIX": "1",
			"CTXLOOM_MOCK_RESPONSE":    "CUSTOM-BODY-4e08",
		},
	}, &out, &out)
	require.NoError(t, err)

	echo := out.String()
	assert.True(t, strings.HasPrefix(echo, MockFailPrefix+"\n"),
		"the prefix must survive a custom response, got %q", echo)
	assert.Contains(t, echo, "CUSTOM-BODY-4e08",
		"the custom response body must survive the prefix")
}

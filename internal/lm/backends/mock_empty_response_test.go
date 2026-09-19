package backends

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// CTXLOOM_MOCK_RESPONSE="" must be a REQUEST, not an absence.
//
// A zero-byte engine reply is the exact shape this project's characteristic bug
// produces — exit 0, a success message, nothing written — so a mock that cannot
// be ASKED for one cannot be used to prove ctxloom surfaces it. Reading the knob
// with a one-value getenv made "set to empty" and "unset" the same input, and
// the empty request silently answered with the full echo instead: a caller
// testing the zero-byte path got several hundred bytes and passed.
//
// The pair below is the whole point. Either test alone is satisfied by a mock
// that ignores the knob in one direction.

// TestMock_EmptyResponseOverride_YieldsZeroBytes is the request half.
func TestMock_EmptyResponseOverride_YieldsZeroBytes(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()
	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "EMPTYREQ-MARKER-8b12"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	res, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env:    map[string]string{"CTXLOOM_MOCK_RESPONSE": ""},
	}, &out, &out)
	require.NoError(t, err)
	require.Equal(t, int32(0), res.ExitCode,
		"an empty reply is a successful run that said nothing — the exit code is a separate knob")

	assert.Empty(t, out.String(),
		"an override SET to empty must produce a zero-byte reply; got %q", out.String())
}

// TestMock_UnsetResponse_YieldsTheEcho is the "can say NO" half: without the
// knob the mock must still echo, or the test above would be satisfied by a mock
// that had simply stopped responding at all.
func TestMock_UnsetResponse_YieldsTheEcho(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()
	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "EMPTYREQ-MARKER-8b12"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	_, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env:    map[string]string{},
	}, &out, &out)
	require.NoError(t, err)

	assert.Contains(t, out.String(), "[mock] context=EMPTYREQ-MARKER-8b12",
		"an UNSET override must leave the echo alone, else set-to-empty and unset are indistinguishable again")
}

// TestMock_EmptyResponseOverride_StillTakesTheFailPrefix pins the interaction
// with the other knob: the marker annotates whatever reply was requested, and an
// empty one is still a reply. Without this, a fix to either knob could quietly
// make the empty request unreachable whenever a failure was being signalled.
func TestMock_EmptyResponseOverride_StillTakesTheFailPrefix(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()
	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "EMPTYREQ-MARKER-8b12"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	_, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env: map[string]string{
			"CTXLOOM_MOCK_RESPONSE":    "",
			"CTXLOOM_MOCK_FAIL_PREFIX": "1",
		},
	}, &out, &out)
	require.NoError(t, err)

	assert.Equal(t, MockFailPrefix+"\n", out.String(),
		"the marker and nothing else: the empty reply was still honoured")
}

// TestMock_ResponseOverride_FallsBackToTheProcessEnvironment covers the third
// arm of the lookup, which a mutation proved nothing reached: with no key in
// the request's own env map, the knob must still be readable from the process
// environment.
//
// That arm is load-bearing rather than decorative — tests/acceptance's
// steps_j000200_common notes that a run reads os.Getenv for any CTXLOOM_MOCK_*
// key the config's env map does not carry — and deleting it left every test in
// this package green.
func TestMock_ResponseOverride_FallsBackToTheProcessEnvironment(t *testing.T) {
	t.Setenv("CTXLOOM_MOCK_RESPONSE", "FROM-PROC-ENV-3d77")

	dir := t.TempDir()
	m := NewMock()
	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "EMPTYREQ-MARKER-8b12"}}, &agent.ManagedConfig{})))

	var out strings.Builder
	_, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env:    map[string]string{},
	}, &out, &out)
	require.NoError(t, err)

	assert.Equal(t, "FROM-PROC-ENV-3d77", out.String(),
		"an override present only in the PROCESS environment must still be honoured")
}

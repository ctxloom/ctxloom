package codex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Characterization pins for a complexity-motivated split of
// scanSessionInfo's three-envelope switch apart. A pure
// reduction case: behaviour is unchanged by definition, so nothing here can be
// red. Their job is to cover EVERY arm before the split, so "behaviour
// preserving" is a checked claim rather than an assertion — including the
// skip arms, which are where a careless extraction quietly changes a
// `continue` into a `return`.
//
// The measured premise, for the record: `lizard -C 10` put scanSessionInfo at
// CCN 12 when first measured and CCN 13 since, genuinely over the repo's
// declared gate.

func scan(t *testing.T, raw ...string) *agent.ChatSessionInfo {
	t.Helper()
	lines := make([][]byte, 0, len(raw))
	for _, r := range raw {
		lines = append(lines, []byte(r))
	}
	info, err := scanSessionInfo(context.Background(), lines)
	require.NoError(t, err)
	return info
}

func TestScanSessionInfo_CollectsAcrossAllThreeEnvelopeTypes(t *testing.T) {
	info := scan(t,
		`{"type":"session_meta","payload":{"id":"sess-1"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-5.5","approval_policy":"on-request"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","model_context_window":258400}}`,
	)
	require.NotNil(t, info)
	assert.Equal(t, "sess-1", info.SessionID)
	assert.Equal(t, "gpt-5.5", info.Model)
	assert.Equal(t, "on-request", info.PermissionMode)
	assert.Equal(t, 258400, info.ContextWindow)
}

func TestScanSessionInfo_FirstOccurrenceOfEachFieldWins(t *testing.T) {
	info := scan(t,
		`{"type":"session_meta","payload":{"id":"first"}}`,
		`{"type":"turn_context","payload":{"model":"first-model","approval_policy":"first-policy"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","model_context_window":100}}`,
		`{"type":"session_meta","payload":{"id":"second"}}`,
		`{"type":"turn_context","payload":{"model":"second-model","approval_policy":"second-policy"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","model_context_window":200}}`,
	)
	require.NotNil(t, info)
	assert.Equal(t, "first", info.SessionID)
	assert.Equal(t, "first-model", info.Model)
	assert.Equal(t, "first-policy", info.PermissionMode)
	assert.Equal(t, 100, info.ContextWindow)
}

// Every skip arm: a line that cannot be decoded at any level must be passed
// over, never abort the scan, and never stop a LATER line from contributing.
// This is the property a split most easily breaks.
func TestScanSessionInfo_EverySkipArmKeepsScanning(t *testing.T) {
	cases := map[string]string{
		"malformed line":           `{not json`,
		"unknown envelope type":    `{"type":"world_state","payload":{"anything":1}}`,
		"session_meta bad payload": `{"type":"session_meta","payload":"not-an-object"}`,
		"event_msg bad payload":    `{"type":"event_msg","payload":"not-an-object"}`,
		"event_msg not task_started": `{"type":"event_msg","payload":{"type":"token_count",` +
			`"info":{"model_context_window":999}}}`,
		"turn_context bad payload": `{"type":"turn_context","payload":42}`,
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			info := scan(t, bad,
				`{"type":"session_meta","payload":{"id":"after-the-skip"}}`)
			require.NotNil(t, info, "the scan must continue past %s", name)
			assert.Equal(t, "after-the-skip", info.SessionID)
			assert.Zero(t, info.ContextWindow, "only task_started supplies the context window")
		})
	}
}

// A file carrying none of the three envelope types yields nil, not an
// all-zero ChatSessionInfo — SessionInfoBuilder's contract, and the
// difference between "no metadata observed" and "genuinely empty on purpose".
func TestScanSessionInfo_NothingFoundIsNil(t *testing.T) {
	assert.Nil(t, scan(t,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[]}}`,
		`{"type":"world_state","payload":{}}`,
	))
	assert.Nil(t, scan(t))
}

// A task_started with no window, and a turn_context with empty strings,
// latch nothing — the builder's non-empty/non-zero rule, which the split must
// not accidentally relocate into the caller.
func TestScanSessionInfo_EmptyValuesLatchNothing(t *testing.T) {
	assert.Nil(t, scan(t,
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"turn_context","payload":{"model":"","approval_policy":""}}`,
		`{"type":"session_meta","payload":{"id":"","session_id":""}}`,
	))
}

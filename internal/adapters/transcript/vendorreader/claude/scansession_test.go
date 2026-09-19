package claude

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterization pins for scanSessionInfo, the first of the two decode
// passes an early exit was added to. They are green before and after the scan learns to
// stop early: a pure-reduction case, where behaviour is
// unchanged by definition and no test can be red. Their job is to make that
// claim checkable rather than asserted — every arm the early exit could
// plausibly break has an arm here.

func rawLines(s ...string) [][]byte {
	out := make([][]byte, 0, len(s))
	for _, l := range s {
		out = append(out, []byte(l))
	}
	return out
}

func TestScanSessionInfo_LatchesFirstOccurrenceOfEachField(t *testing.T) {
	info := scanSessionInfo(rawLines(
		`{"type":"user","sessionId":"s1","permissionMode":"plan","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"first-model","content":"a"}}`,
		`{"type":"user","sessionId":"s-LATER","permissionMode":"acceptEdits","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"later-model","content":"b"}}`,
	))
	require.NotNil(t, info)
	assert.Equal(t, "s1", info.SessionID)
	assert.Equal(t, "first-model", info.Model)
	assert.Equal(t, "plan", info.PermissionMode)
}

// The exit condition must be "every field is latched", never "we have seen a
// few lines": a model that only appears at the very end of a long file must
// still be found.
func TestScanSessionInfo_FindsAFieldThatOnlyAppearsAtTheEnd(t *testing.T) {
	lines := rawLines(`{"type":"user","sessionId":"s1","permissionMode":"default","message":{"role":"user","content":"hi"}}`)
	for i := 0; i < 200; i++ {
		lines = append(lines, []byte(`{"type":"progress","sessionId":"s1"}`))
	}
	lines = append(lines, []byte(`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"late-model","content":"a"}}`))

	info := scanSessionInfo(lines)
	require.NotNil(t, info)
	assert.Equal(t, "late-model", info.Model)
}

// A transcript that never carries one of the fields yields whatever it does
// carry — the scan runs the file out rather than giving up.
func TestScanSessionInfo_MissingFieldStillReturnsTheRest(t *testing.T) {
	info := scanSessionInfo(rawLines(
		`{"type":"user","sessionId":"s1","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"m","content":"a"}}`,
	))
	require.NotNil(t, info)
	assert.Equal(t, "s1", info.SessionID)
	assert.Equal(t, "m", info.Model)
	assert.Empty(t, info.PermissionMode)
}

// Nothing found anywhere must stay nil, never an all-zero ChatSessionInfo —
// SessionInfoBuilder's contract, and the difference between "no metadata
// observed" and "genuinely empty on purpose".
func TestScanSessionInfo_NothingFoundIsNil(t *testing.T) {
	assert.Nil(t, scanSessionInfo(rawLines(
		`{"type":"progress"}`,
		`{not json`,
		`{"type":"summary"}`,
	)))
	assert.Nil(t, scanSessionInfo(nil))
}

// Malformed lines are skipped without stopping the scan — a corrupt line
// early in the file must not hide metadata that comes after it.
func TestScanSessionInfo_MalformedLinesDoNotStopTheScan(t *testing.T) {
	info := scanSessionInfo(rawLines(
		`{truncated`,
		strings.TrimSpace(`{"type":"assistant","sessionId":"s9","message":{"role":"assistant","model":"m9","content":"a"}}`),
	))
	require.NotNil(t, info)
	assert.Equal(t, "s9", info.SessionID)
	assert.Equal(t, "m9", info.Model)
}

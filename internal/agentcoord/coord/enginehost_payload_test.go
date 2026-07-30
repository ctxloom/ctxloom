package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestStructFromJSON_DegradesRatherThanDroppingThePayload pins that a
// permission request's tool input always survives into the ApprovalRequest
// payload in SOME readable form. An input that is not a JSON object already
// wrapped as {"value": …}; input that is not valid JSON at all used to return
// nil, so the human deciding the approval saw the tool NAME and nothing about
// what it was asked to do.
func TestStructFromJSON_DegradesRatherThanDroppingThePayload(t *testing.T) {
	t.Run("empty stays nil", func(t *testing.T) {
		assert.Nil(t, structFromJSON(nil))
		assert.Nil(t, structFromJSON([]byte("")))
	})

	t.Run("object marshals directly", func(t *testing.T) {
		s := structFromJSON([]byte(`{"command":"ls -l"}`))
		require.NotNil(t, s)
		assert.Equal(t, "ls -l", s.GetFields()["command"].GetStringValue())
	})

	t.Run("non-object json wraps", func(t *testing.T) {
		s := structFromJSON([]byte(`["a","b"]`))
		require.NotNil(t, s)
		assert.Len(t, s.GetFields()["value"].GetListValue().GetValues(), 2)
	})

	t.Run("invalid json keeps the raw text", func(t *testing.T) {
		s := structFromJSON([]byte("rm -rf / --no-preserve-root"))
		require.NotNil(t, s, "an unparseable tool input must not drop the whole payload")
		assert.Equal(t, "rm -rf / --no-preserve-root", s.GetFields()["value"].GetStringValue())
	})

	t.Run("invalid utf8 is repaired, not dropped", func(t *testing.T) {
		s := structFromJSON([]byte{'{', 0xff, 0xfe})
		require.NotNil(t, s)
		assert.NotEmpty(t, s.GetFields()["value"].GetStringValue())
	})
}

// TestRunStartedConfig_EchoesEverySpecField pins the live path a review row called
// an error-swallowing one. The echo is five plain strings, so structpb's
// construction cannot fail for ANY HarnessSpec — the nil-on-error arm is
// defensive, not a reachable payload drop. This pins what the arm's absence
// would have to keep true.
func TestRunStartedConfig_EchoesEverySpecField(t *testing.T) {
	assert.Nil(t, runStartedConfig(nil), "no spec, no config echo")

	cfg := runStartedConfig(&agentcoordpb.HarnessSpec{
		Harness:         "claude-code",
		Model:           "claude-sonnet-5",
		Workspace:       "/wörk/ünicode\n\t",
		PermissionMode:  "bypass",
		ResumeSessionId: "sess-42",
	})
	require.NotNil(t, cfg)
	f := cfg.GetFields()
	assert.Equal(t, "claude-code", f["harness"].GetStringValue())
	assert.Equal(t, "claude-sonnet-5", f["model"].GetStringValue())
	assert.Equal(t, "/wörk/ünicode\n\t", f["workspace"].GetStringValue())
	assert.Equal(t, "bypass", f["permission_mode"].GetStringValue())
	assert.Equal(t, "sess-42", f["resumed_from_harness_session_id"].GetStringValue())

	// An entirely empty spec still echoes every key (empty strings), so the
	// log never has to distinguish "not reported" from "reported empty".
	empty := runStartedConfig(&agentcoordpb.HarnessSpec{})
	require.NotNil(t, empty)
	assert.Len(t, empty.GetFields(), 5)
}

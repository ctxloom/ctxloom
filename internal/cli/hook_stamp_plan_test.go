package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestParseEditPayload covers the stamp-plan hook's stdin parser.
// Claude Code's shapes (wrapped + bare), plus malformed/empty inputs. The
// contract: any failure mode produces empty path + nil error (the hook is
// silently a no-op for bad payloads — never fail the host backend over a
// malformed message).
func TestParseEditPayload(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"tool_input_wrapper", `{"tool_input":{"file_path":"/x/y.md"}}`, "/x/y.md"},
		{"bare_shape", `{"file_path":"/x/y.md"}`, "/x/y.md"},
		{"empty_tool_input", `{"tool_input":{}}`, ""},
		{"empty_object", `{}`, ""},
		{"missing_file_path", `{"tool_input":{"old_string":"x","new_string":"y"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEditPayload([]byte(tc.raw))
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseEditPayload_MalformedJSON exercises the explicit-error path:
// parseEditPayload returns the json.Unmarshal error so the caller can report
// it (see TestStampPlanHook_WarnsOnUnparsablePayload for what the command
// does with it).
func TestParseEditPayload_MalformedJSON(t *testing.T) {
	_, err := parseEditPayload([]byte("not json"))
	assert.Error(t, err)
}

// TestStampPlanHook_WarnsOnUnparsablePayload pins the hook's
// warn-and-continue contract on an UNPARSABLE payload: a payload this hook
// cannot decode at all is a contract break with the host engine (both
// supported shapes are JSON), so it must be diagnosable on stderr rather than
// vanishing — the same rule the sibling stdin-read branch already follows. The
// hook still returns nil: a stamping hiccup never fails the host's tool call.
func TestStampPlanHook_WarnsOnUnparsablePayload(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "wave-u036-f10")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("not json"))
	require.NoError(t, stampPlanCmd.RunE(cmd, nil),
		"a malformed payload must never fail the host agent's tool call")

	assert.Contains(t, sink.String(), "stamp-plan",
		"the unparsable payload must be reported on the diagnostic channel")
}

// TestStampPlanHook_SilentOnNonEditPayload is TestStampPlanHook_WarnsOnUnparsablePayload's
// counterpart: a well-formed payload that simply names no file (a non-edit
// tool call) is an ordinary, expected event and must stay silent, so the warn
// above cannot degrade into per-tool-call noise.
func TestStampPlanHook_SilentOnNonEditPayload(t *testing.T) {
	t.Setenv("CTXLOOM_SESSION_HARP", "wave-u036-f10")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(`{"tool_input":{}}`))
	require.NoError(t, stampPlanCmd.RunE(cmd, nil))

	assert.Empty(t, sink.String(), "a payload with no file_path is not an error")
}

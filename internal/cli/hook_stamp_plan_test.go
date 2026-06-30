package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseEditPayload covers the stamp-plan hook's stdin parser.
// Claude Code's shapes (wrapped + bare) plus Antigravity's
// toolCall.args shape (the agy fixtures mirror verbatim live
// captures), plus malformed/empty inputs. The contract: any failure
// mode produces empty path + nil error (the hook is silently a no-op
// for bad payloads — never fail the host backend over a malformed
// message).
func TestParseEditPayload(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"tool_input_wrapper", `{"tool_input":{"file_path":"/x/y.md"}}`, "/x/y.md"},
		{"bare_shape", `{"file_path":"/x/y.md"}`, "/x/y.md"},
		{"empty_tool_input", `{"tool_input":{}}`, ""},
		{"empty_object", `{}`, ""},
		{"missing_file_path", `{"tool_input":{"old_string":"x","new_string":"y"}}`, ""},
		{"agy_write_to_file",
			`{"conversationId":"a404a6e2","toolCall":{"name":"write_to_file","args":{"CodeContent":"hi","Overwrite":true,"TargetFile":"/x/plan.md"}},"workspacePaths":["/x"]}`,
			"/x/plan.md"},
		{"agy_replace_file_content",
			`{"toolCall":{"name":"replace_file_content","args":{"TargetFile":"/x/plan.md","TargetContent":"a","ReplacementContent":"b","StartLine":1,"EndLine":1}}}`,
			"/x/plan.md"},
		{"agy_run_command_has_no_file",
			`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi","Cwd":"/x"}}}`,
			""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEditPayload([]byte(tc.raw))
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseEditPayload_MalformedJSON exercises the explicit-error path.
// parseEditPayload returns the json.Unmarshal error so the caller can
// decide whether to log; the stamp-plan command itself ignores the error
// and no-ops.
func TestParseEditPayload_MalformedJSON(t *testing.T) {
	_, err := parseEditPayload([]byte("not json"))
	assert.Error(t, err)
}

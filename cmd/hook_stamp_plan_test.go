package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseEditPayload covers the stamp-plan hook's stdin parser.
// Two payload shapes (wrapped + bare) plus malformed/empty inputs.
// The contract: any failure mode produces empty path + nil error
// (the hook is silently a no-op for bad payloads — never fail the
// host backend over a malformed message).
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

// TestParseEditPayload_MalformedJSON exercises the explicit-error path.
// parseEditPayload returns the json.Unmarshal error so the caller can
// decide whether to log; the stamp-plan command itself ignores the error
// and no-ops.
func TestParseEditPayload_MalformedJSON(t *testing.T) {
	_, err := parseEditPayload([]byte("not json"))
	assert.Error(t, err)
}

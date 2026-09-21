package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Claude-shaped transcript fixtures shared by the hook verbs that read a
// turn's transcript (next-step and skill-mates). The text-message sibling,
// assistantText, lives beside next-step capture, which is what reads it.

func writeClaudeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return p
}

func assistantTool(uuid, msgID, tool, input string) string {
	return `{"type":"assistant","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"` + msgID +
		`","type":"message","role":"assistant","content":[{"type":"tool_use","id":"t-` + uuid + `","name":"` + tool + `","input":` + input +
		`}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:01.000Z"}`
}

func userPrompt(text, uuid string) string {
	return `{"type":"user","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:00.000Z"}`
}

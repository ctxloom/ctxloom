package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The payload fixtures below are verbatim captures from a live agy v1.0.7
// PreToolUse hook (2026-06-10), not hand-written approximations.

const capturedRunCommand = `{
    "artifactDirectoryPath": "/home/u/.gemini/antigravity-cli/brain/c6a3e887-aeea-4f48-87d9-242525b5f482",
    "conversationId": "c6a3e887-aeea-4f48-87d9-242525b5f482",
    "stepIdx": 3,
    "toolCall": {
        "args": {
            "CommandLine": "echo hook-probe-1",
            "Cwd": "/tmp/agy-probe",
            "WaitMsBeforeAsync": 1000
        },
        "name": "run_command"
    },
    "transcriptPath": "/home/u/.gemini/antigravity-cli/brain/c6a3e887-aeea-4f48-87d9-242525b5f482/.system_generated/logs/transcript_full.jsonl",
    "workspacePaths": [
        "/tmp/agy-probe"
    ]
}`

const capturedWriteToFile = `{
    "artifactDirectoryPath": "/home/u/.gemini/antigravity-cli/brain/a404a6e2-2bc3-466d-86f7-4abca16ffb04",
    "conversationId": "a404a6e2-2bc3-466d-86f7-4abca16ffb04",
    "stepIdx": 3,
    "toolCall": {
        "args": {
            "CodeContent": "hi",
            "Description": "Create probe.txt containing the word hi",
            "Overwrite": true,
            "TargetFile": "/tmp/agy-probe/probe.txt"
        },
        "name": "write_to_file"
    },
    "transcriptPath": "/home/u/.gemini/antigravity-cli/brain/a404a6e2-2bc3-466d-86f7-4abca16ffb04/.system_generated/logs/transcript_full.jsonl",
    "workspacePaths": [
        "/tmp/agy-probe"
    ]
}`

const capturedReplaceFileContent = `{
    "toolCall": {
        "args": {
            "AllowMultiple": false,
            "Description": "Update probe.txt content",
            "EndLine": 1,
            "Instruction": "Change 'hi' to 'hello there'",
            "ReplacementContent": "hello there",
            "StartLine": 1,
            "TargetContent": "hi",
            "TargetFile": "/tmp/agy-probe/probe.txt"
        },
        "name": "replace_file_content"
    }
}`

func TestDecodeHookPayload_RunCommand(t *testing.T) {
	p, err := DecodeHookPayload([]byte(capturedRunCommand))
	require.NoError(t, err)

	assert.Equal(t, "c6a3e887-aeea-4f48-87d9-242525b5f482", p.ConversationID)
	assert.Equal(t, 3, p.StepIdx)
	assert.Equal(t, ToolRunCommand, p.ToolCall.Name)
	assert.Equal(t, "echo hook-probe-1", p.ToolCall.Args.CommandLine)
	assert.Equal(t, "/tmp/agy-probe", p.ToolCall.Args.Cwd)
	assert.Equal(t, []string{"/tmp/agy-probe"}, p.WorkspacePaths)
	assert.Contains(t, p.TranscriptPath, "transcript_full.jsonl")
	assert.Empty(t, p.ToolCall.Args.FilePath())
}

func TestDecodeHookPayload_WriteToFile(t *testing.T) {
	p, err := DecodeHookPayload([]byte(capturedWriteToFile))
	require.NoError(t, err)

	assert.Equal(t, ToolWriteToFile, p.ToolCall.Name)
	assert.Equal(t, "/tmp/agy-probe/probe.txt", p.ToolCall.Args.TargetFile)
	assert.Equal(t, "/tmp/agy-probe/probe.txt", p.ToolCall.Args.FilePath())
	assert.Empty(t, p.ToolCall.Args.CommandLine)
}

func TestDecodeHookPayload_ReplaceFileContent(t *testing.T) {
	p, err := DecodeHookPayload([]byte(capturedReplaceFileContent))
	require.NoError(t, err)

	assert.Equal(t, ToolReplaceFileContent, p.ToolCall.Name)
	assert.Equal(t, "/tmp/agy-probe/probe.txt", p.ToolCall.Args.FilePath())
	assert.Equal(t, "hi", p.ToolCall.Args.TargetContent)
	assert.Equal(t, "hello there", p.ToolCall.Args.ReplacementContent)
}

func TestDecodeHookPayload_Malformed(t *testing.T) {
	_, err := DecodeHookPayload([]byte("{not json"))
	assert.Error(t, err)
}

// TestDecodeHookPayload_MalformedErrorIsAttributed pins U029-F08: the decode
// error reaches a user through a hook process whose only output channel is
// stderr, where a bare `encoding/json` message ("invalid character 'o' ...")
// names neither the payload nor the engine. Consumers (ltk's antigravity
// engine, the session/hook-stamp readers in internal/cli) surface it verbatim,
// so the attribution has to come from here.
func TestDecodeHookPayload_MalformedErrorIsAttributed(t *testing.T) {
	_, err := DecodeHookPayload([]byte("{not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "antigravity hook payload", "the error names what failed to decode")
	assert.Contains(t, err.Error(), "invalid character", "the underlying json error is still wrapped, not replaced")
}

// TestEncodeDeny pins the exact decision JSON agy accepts: verified live —
// this shape on stdout (exit 0) blocks the tool call and the model receives
// "Tool call denied with reason: <reason>".
func TestEncodeDeny(t *testing.T) {
	out, err := EncodeDeny("use the frobnicate command instead")
	require.NoError(t, err)
	assert.JSONEq(t, `{"decision":"deny","reason":"use the frobnicate command instead"}`, string(out))

	var d HookDecision
	require.NoError(t, json.Unmarshal(out, &d))
	assert.Equal(t, DecisionDeny, d.Decision)
}

func TestEncodeDeny_NoReason(t *testing.T) {
	out, err := EncodeDeny("")
	require.NoError(t, err)
	assert.JSONEq(t, `{"decision":"deny"}`, string(out))
}

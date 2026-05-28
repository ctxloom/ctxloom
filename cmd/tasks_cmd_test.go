package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTasksCaptureCmd_AcceptsStdinFlag pins the contract between the embedded
// tasks bundle and the CLI: the bundle ships
//
//	PostToolUse matcher=TodoWrite → ctxloom tasks capture --stdin
//
// (resources/builtin_bundles/tasks.yaml). If the `capture` command stops
// declaring --stdin, cobra rejects that hook invocation with "unknown flag:
// --stdin" and every TodoWrite auto-capture silently fails — tasks the agent
// records via its todo tool never reach .ctxloom/tasks.md. This regressed once
// when the flag definition was dropped while the bundle, tests, and doc
// comments all still referenced it.
func TestTasksCaptureCmd_AcceptsStdinFlag(t *testing.T) {
	require.NotNil(t, tasksCaptureCmd.Flags().Lookup("stdin"),
		"`tasks capture` must declare --stdin to match the bundle-shipped auto-capture hook command")

	// And cobra must actually parse it without erroring.
	require.NoError(t, tasksCaptureCmd.Flags().Parse([]string{"--stdin"}),
		"`tasks capture --stdin` must parse")
}

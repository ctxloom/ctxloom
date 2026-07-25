// Tests for weave's input guards and output floor — the four places the command
// could fan real agents out over nothing, or report success having produced
// nothing (U043-F02, F03, F05, F08).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// U043-F03: the "nothing to weave" guard tested the FLAGS, not the resolved
// parts. `--parts-from <dir>` where the dir is empty (or holds only
// subdirectories, which the scan skips) passed the flag guard, resolved to zero
// injected parts, and went on to synthesize nothing.
func TestCheckWeaveInputs_PartsFromResolvingToNothing(t *testing.T) {
	err := checkWeaveInputs(nil, nil, "merge these findings")
	require.Error(t, err, "no members and no resolved parts is nothing to weave, whatever the flags said")
	assert.Contains(t, err.Error(), "nothing to weave")
}

// U043-F08: the task was never checked non-empty, so `ctxloom weave -p a -p b`
// with no arguments and nothing on stdin fanned real agents out over an empty
// prompt — several engine launches, billed, over nothing.
func TestCheckWeaveInputs_EmptyTaskWithMembers(t *testing.T) {
	err := checkWeaveInputs([]string{"reviewer/a"}, nil, "   \n ")
	require.Error(t, err, "members with no task is a fan-out over nothing")
	assert.Contains(t, err.Error(), "task")
}

// A parts-only run legitimately has no task: the synthesizer is handed the
// injected parts and the task section is simply omitted. That is the
// "legitimately nothing to do" side of the discriminator and must stay green.
func TestCheckWeaveInputs_PartsOnlyRunNeedsNoTask(t *testing.T) {
	injected := []operations.Part{{Profile: "legacy", Output: "old report"}}
	assert.NoError(t, checkWeaveInputs(nil, injected, ""))
}

// U043-F08 (read half): a stdin read FAILURE left task empty and was swallowed
// entirely — `if data, rerr := io.ReadAll(os.Stdin); rerr == nil` has no else.
func TestReadTask_StdinErrorIsReported(t *testing.T) {
	_, err := readTask(nil, failingReader{}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin")
}

func TestReadTask_ArgumentsWinOverStdin(t *testing.T) {
	got, err := readTask([]string{"review", "this"}, failingReader{}, true)
	require.NoError(t, err)
	assert.Equal(t, "review this", got, "arguments are read before stdin is touched")
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, assert.AnError }

// U043-F02: a synthesis that returned empty output printed ONE BLANK LINE and
// exited 0 — the characteristic bug. An empty report is a synthesis that did not
// happen, so it falls back to the parts exactly as a synthesis ERROR does, and
// says so.
func TestWeaveText_EmptyReportFallsBackToParts(t *testing.T) {
	result := &operations.WeaveResult{
		Report: "  \n ",
		Parts:  []operations.Part{{Profile: "a", Output: "member a said this"}},
	}
	out, reason := weaveText(result, nil, false)
	assert.NotEmpty(t, reason, "a synthesizer that produced nothing must be reported")
	assert.Contains(t, out, "member a said this", "the parts are emitted instead of a blank line")
}

func TestWeaveText_ReportIsEmittedWhenItHasContent(t *testing.T) {
	result := &operations.WeaveResult{
		Report: "the synthesis",
		Parts:  []operations.Part{{Profile: "a", Output: "member a"}},
	}
	out, reason := weaveText(result, nil, false)
	assert.Empty(t, reason)
	assert.Equal(t, "the synthesis\n", out)
}

// Nothing at all: no report and no parts. There is no salvage, so this is the
// hard failure — never a blank line and exit 0.
func TestWeaveText_NoReportAndNoParts(t *testing.T) {
	out, reason := weaveText(&operations.WeaveResult{}, nil, false)
	assert.NotEmpty(t, reason)
	assert.Empty(t, strings.TrimSpace(out), "the caller must be able to detect that there is nothing to print")
}

// U043-F05(a): saveParts made the directory and returned nil after writing zero
// files. The user passed --save-parts and got an empty directory reported as
// success.
func TestSaveParts_EmptyIsAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "parts")
	err := saveParts(dir, nil)
	require.Error(t, err, "--save-parts with nothing to save must not report success")
}

// U043-F05(b): the filename was strings.ReplaceAll(p.Profile, "/", "_") with no
// uniqueness check, so `-p a/b -p a_b` (or an agent and a profile of the same
// name, which mergeMembers does not dedup) wrote both members to a_b.txt —
// losing one member's entire output.
func TestSaveParts_CollidingNamesDoNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, saveParts(dir, []operations.Part{
		{Profile: "a/b", Output: "first member"},
		{Profile: "a_b", Output: "second member"},
	}))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "two members must produce two files")

	var bodies []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		bodies = append(bodies, strings.TrimSpace(string(data)))
	}
	assert.ElementsMatch(t, []string{"first member", "second member"}, bodies,
		"neither member's output may be overwritten by the other")
}

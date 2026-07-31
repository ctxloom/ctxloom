// Tests for weave's input guards and output floor — the four places the command
// could fan real agents out over nothing, or report success having produced
// nothing (U043-F02, F03, F05, F08).
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

// --map-only is documented as an ALIAS for --no-synthesize, but it was
// registered as a second, independent BoolVar aimed at the same pointer. Two
// flags sharing one variable is a latent correctness hazard, not just untidy
// help: registration order silently decides the value (each BoolVar writes its
// own default into the shared pointer as it registers), pflag marks only the
// flag the user actually typed as Changed, and the alias shows up as a
// separate feature in --help and in shell completion.
//
// A real alias is one flag with a normalized second spelling: one default, one
// Changed bit, one help line, and --map-only still works.
func TestWeaveMapOnly_IsOneFlagNotTwo(t *testing.T) {
	orig := weaveNoSynth
	t.Cleanup(func() { weaveNoSynth = orig })

	flags := weaveCmd.Flags()
	noSynth := flags.Lookup("no-synthesize")
	require.NotNil(t, noSynth)

	assert.Same(t, noSynth, flags.Lookup("map-only"),
		"--map-only must resolve to the SAME flag as --no-synthesize, not a twin bound to the same pointer")

	var listed []string
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Name == "no-synthesize" || f.Name == "map-only" {
			listed = append(listed, f.Name)
		}
	})
	assert.Equal(t, []string{"no-synthesize"}, listed,
		"the alias must not appear as its own entry in --help or in shell completion")
}

// The alias must keep WORKING — this is the guard that stops the fix above
// from quietly deleting a documented spelling.
func TestWeaveMapOnly_StillSetsNoSynthesize(t *testing.T) {
	orig := weaveNoSynth
	t.Cleanup(func() { weaveNoSynth = orig })

	weaveNoSynth = false
	require.NoError(t, weaveCmd.Flags().Parse([]string{"--map-only"}))
	assert.True(t, weaveNoSynth, "--map-only must still skip synthesis")
	assert.True(t, weaveCmd.Flags().Changed("no-synthesize"),
		"passing the alias must mark the one flag it aliases as Changed")
}

// weaveOnlyInjectedParts points the weave globals at a parts-only,
// synthesis-free run: nothing launches an engine, so the pre-fix behaviour can
// be exercised end to end without spawning members.
func weaveOnlyInjectedParts(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("a part"), 0o644))

	prevFrom, prevNoSynth, prevAgents, prevProfiles, prevSynth :=
		weavePartsFrom, weaveNoSynth, weaveAgents, weaveProfiles, weaveSynthesize
	weavePartsFrom, weaveNoSynth, weaveAgents, weaveProfiles, weaveSynthesize =
		dir, true, nil, nil, ""
	t.Cleanup(func() {
		weavePartsFrom, weaveNoSynth, weaveAgents, weaveProfiles, weaveSynthesize =
			prevFrom, prevNoSynth, prevAgents, prevProfiles, prevSynth
	})
}

// brokenConfigProject makes cwd a project whose config.yaml will not parse.
// config.Load degrades that to a kind-tagged Warning and a nil error (CLAUDE.md
// fault tolerance), and printAndRecordConfigWarnings records it as a FATAL-class
// strictness finding — the finding every process-owning entry point gates on.
func brokenConfigProject(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ctxloom", "config.yaml"),
		[]byte("profiles:\n  definitions: [this is not a mapping\n"), 0o644))
	t.Setenv(projectroot.EnvVar, root)
	t.Chdir(root)

	cfg, err := config.LoadFresh()
	require.NoError(t, err)
	require.NotEmpty(t, cfg.GetWarnings(),
		"the fixture must actually produce a config warning, or the pin proves nothing")
}

// weave LAUNCHES engine processes, which makes it a process-owning entry point
// exactly like run / mcp / acp / profile materialize — every one of which
// checkpoints strictness and calls failOnFindings before doing the dangerous
// thing. weave did neither, so a config broken badly enough that `ctxloom run`
// refuses to start would fan real agents out over it anyway, on whatever
// partial context survived the parse. Silent, and worse than the failure the
// gate exists to prevent, because it is N sessions instead of one.
func TestRunWeave_FatalConfigFindingAbortsBeforeLaunch(t *testing.T) {
	resetStrictness(t)
	brokenConfigProject(t)
	weaveOnlyInjectedParts(t)

	cmd, _ := testCmd()
	err := runWeave(cmd, nil)

	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr, "a fatal config finding must abort weave before it launches anything")
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code, "weave must use the same findings exit code as run/mcp")
}

// --degraded is the documented escape hatch, and it must work here for the
// same reason it works for run: the user has been told, and chose to launch.
func TestRunWeave_DegradedModeStillWeaves(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	brokenConfigProject(t)
	weaveOnlyInjectedParts(t)

	cmd, out := testCmd()
	require.NoError(t, runWeave(cmd, nil), "degraded mode must not abort")
	assert.Contains(t, out.String(), "a part", "the parts must still be emitted")
}

// Characterization for the --save-parts arm of runWeave, taken BEFORE the
// complexity split so the split is provably behaviour-preserving: a save that
// fails warns and the run still emits its parts (partial success is success).
func TestRunWeave_SavePartsFailureWarnsButStillEmits(t *testing.T) {
	resetStrictness(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ctxloom"), 0o755))
	t.Setenv(projectroot.EnvVar, root)
	t.Chdir(root)
	weaveOnlyInjectedParts(t)

	// A regular FILE where the save dir should be: MkdirAll cannot create it.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	prev := weaveSaveParts
	weaveSaveParts = blocker
	t.Cleanup(func() { weaveSaveParts = prev })

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	cmd, out := testCmd()
	require.NoError(t, runWeave(cmd, nil), "a failed --save-parts must not fail the run")
	assert.Contains(t, sink.String(), "saving parts failed")
	assert.Contains(t, out.String(), "a part", "the parts are still emitted")
}

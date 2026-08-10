package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// formatCmd builds a bare command with the --format flag registered and set,
// mirroring how the inherited persistent flag reaches a subcommand at runtime.
func formatCmd(format string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.Flags().String("format", string(clifmt.FormatText), "")
	_ = cmd.Flags().Set("format", format)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

type emitTestItem struct {
	Name string `json:"name"`
}

func TestEmit_JSON_DelegatesToClifmt(t *testing.T) {
	cmd, buf := formatCmd("json")
	err := emit(cmd, emitTestItem{Name: "abc"}, func() error {
		t.Fatal("text renderer must not run in json mode")
		return nil
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"abc"}`, buf.String())
}

func TestEmit_YAML_DelegatesToClifmt(t *testing.T) {
	cmd, buf := formatCmd("yaml")
	err := emit(cmd, emitTestItem{Name: "abc"}, func() error {
		t.Fatal("text renderer must not run in yaml mode")
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "name: abc\n", buf.String())
}

func TestEmit_TOML_DelegatesToClifmt(t *testing.T) {
	cmd, buf := formatCmd("toml")
	err := emit(cmd, emitTestItem{Name: "abc"}, func() error {
		t.Fatal("text renderer must not run in toml mode")
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "abc")
}

func TestEmit_Markdown_DelegatesToClifmtReflection(t *testing.T) {
	cmd, buf := formatCmd("markdown")
	err := emit(cmd, emitTestItem{Name: "abc"}, func() error {
		t.Fatal("text renderer must not run in markdown mode")
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "abc")
}

func TestEmit_Text_RunsHumanRenderer(t *testing.T) {
	cmd, buf := formatCmd("text")
	err := emit(cmd, struct{}{}, func() error {
		_, e := buf.WriteString("human output")
		return e
	})
	require.NoError(t, err)
	assert.Equal(t, "human output", buf.String())
}

func TestEmit_Text_NilRenderer_FallsBackToReflection(t *testing.T) {
	// A call site with no bespoke human renderer (nil text) still gets a
	// readable --format text output via clifmt's reflective default, instead
	// of emit() panicking on a nil call.
	cmd, buf := formatCmd("text")
	err := emit(cmd, emitTestItem{Name: "abc"}, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "abc")
}

func TestEmit_UnsetFormat_DefaultsToText(t *testing.T) {
	cmd := &cobra.Command{} // no --format flag registered → reads as ""
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	ran := false
	err := emit(cmd, struct{}{}, func() error { ran = true; return nil })
	require.NoError(t, err)
	assert.True(t, ran, "empty format must run the text renderer")
}

func TestEmit_UnknownFormat_WrapsErrUnsupportedFormat(t *testing.T) {
	cmd, _ := formatCmd("xml")
	err := emit(cmd, struct{}{}, func() error {
		t.Fatal("neither renderer should run for an unsupported format")
		return nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, clifmt.ErrUnsupportedFormat), "got: %v", err)
}

// --- the runtime guard that turns "--format is registered
// globally but honoured opt-in" into a loud error instead of a silent exit 0
// ---

// withFormatGuardReset saves/restores the package-level formatWasHonored
// tracker around a subtest, since it is process-global state (by design —
// see its doc comment) and format_test.go's other cases don't touch it.
func withFormatGuardReset(t *testing.T) {
	t.Helper()
	prev := formatWasHonored
	t.Cleanup(func() { formatWasHonored = prev })
	resetFormatGuard()
}

func TestCheckFormatWasHonored_TextFormatNeverErrors(t *testing.T) {
	withFormatGuardReset(t)
	cmd, _ := formatCmd("text")
	// formatWasHonored is false (freshly reset) — text must still pass:
	// there is nothing to honor when the caller asked for (or defaulted to)
	// human text.
	assert.NoError(t, checkFormatWasHonored(cmd))
}

func TestCheckFormatWasHonored_NonTextAndHonored_NoError(t *testing.T) {
	withFormatGuardReset(t)
	cmd, _ := formatCmd("json")
	formatWasHonored = true
	assert.NoError(t, checkFormatWasHonored(cmd))
}

func TestCheckFormatWasHonored_NonTextAndNotHonored_ErrorsLoudly(t *testing.T) {
	withFormatGuardReset(t)
	cmd, _ := formatCmd("json")
	cmd.Use = "widget frobnicate"
	// formatWasHonored stays false: no emit()/outputFormatOf call happened.
	err := checkFormatWasHonored(cmd)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--format json")
	assert.Contains(t, err.Error(), "does not support it yet")
}

func TestEmit_MarksFormatWasHonored(t *testing.T) {
	withFormatGuardReset(t)
	cmd, _ := formatCmd("json")
	require.NoError(t, emit(cmd, emitTestItem{Name: "abc"}, nil))
	assert.True(t, formatWasHonored)
}

func TestOutputFormatOf_MarksFormatWasHonored(t *testing.T) {
	withFormatGuardReset(t)
	cmd, _ := formatCmd("json")
	outputFormatOf(cmd)
	assert.True(t, formatWasHonored)
}

// TestUnwiredCommand_FormatJSONErrorsLoudly is the end-to-end pin: `config
// create` is a real, currently-unwired command (format_coverage_test.go's
// formatDebtAllowlist: "config.go: runConfigCreate must route through emit()
// instead of a bare fmt.Fprintf" — owned by a different flow batch, not touched
// here) — exactly the "accepted and silently ignored on dozens of
// commands" shape formatWasHonored guards against. An unwired command must
// refuse loudly instead of exiting 0 while lying about having honored the flag.
//
// It drives `config create` rather than `config show`: `config show`/`config
// get` were wired through emit(), so they no longer exhibit the shape this
// guard exists to catch.
func TestUnwiredCommand_FormatJSONErrorsLoudly(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"config", "create", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	require.Error(t, err, "an unwired command must not silently exit 0 under a non-text --format")
	assert.Contains(t, err.Error(), "does not support it yet")
}

// TestUnwiredCommand_DefaultTextStillWorks is the negative control: the same
// unwired command with NO --format (defaults to text) must keep working exactly
// as before — the guard only gates non-text formats.
func TestUnwiredCommand_DefaultTextStillWorks(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// Explicit --format=text, not an omitted flag: rootCmd's --format is a
	// process-wide PersistentFlags() value that pflag does not reset to its
	// default between Execute() calls sharing the same *cobra.Command tree,
	// so an earlier test in this binary leaving it at "json" would otherwise
	// leak into this one (test-order hazard, not a product bug).
	rootCmd.SetArgs([]string{"config", "create", "--format", "text"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	require.NoError(t, rootCmd.Execute())
	assert.NotEmpty(t, out.String())
}

// A prior review counted four format vocabularies for one flag and named the
// third independent resolveFormat (cmd/harp) as the duplication. That copy is gone:
// cmd/harp imports cliemit and its resolveFormat delegates to
// cliemit.Resolve, pinned by cmd/harp/format_parity_test.go. What remains is
// not a fourth implementation but a deliberate NARROWING, documented in this
// file's const block: the streaming commands (session watch, plan watch,
// run's structured stream) render one event at a time and cannot hand clifmt
// a single result value, so they switch on text/json themselves.
//
// The real drift risk in that arrangement is a VALUE divergence, which
// nothing pinned: these are plain strings, not clifmt.Format aliases, so if
// clifmt's spellings ever changed the streaming commands would silently
// reject a --format the rest of the CLI accepts. This pins the two
// vocabularies to the same wire spellings, and pins the narrowing itself -
// text and json are the only two the streaming switch admits, and every other
// clifmt format must reach unknownFormatError.
func TestStreamingFormatVocabularyAgreesWithClifmt(t *testing.T) {
	assert.Equal(t, string(clifmt.FormatText), formatText,
		"the streaming vocabulary and clifmt must spell text the same way")
	assert.Equal(t, string(clifmt.FormatJSON), formatJSON,
		"the streaming vocabulary and clifmt must spell json the same way")

	narrowed := map[string]bool{formatText: true, formatJSON: true}
	for _, f := range []clifmt.Format{
		clifmt.FormatText, clifmt.FormatJSON, clifmt.FormatYAML, clifmt.FormatTOML, clifmt.FormatMarkdown,
	} {
		if narrowed[string(f)] {
			continue
		}
		err := unknownFormatError(string(f))
		require.Error(t, err)
		assert.Contains(t, err.Error(), string(f),
			"a format outside the streaming pair must be named in its own rejection")
	}
}

// The six commands that accept the global --format and never read it were
// once described as "so `--format json` silently returns text". They do not
// read it — that part is accurate and all six are TRACKED as debt in
// formatDebtAllowlist — but the outcome is no longer silent: the
// PersistentPostRunE guard (formatWasHonored) turns the accepted-and-ignored
// case into a loud error and a non-zero exit. This pins both halves.
func TestFormatDebtCommands_AreTrackedAndRefuseNonTextLoudly(t *testing.T) {
	for _, path := range []string{
		"bundle hold", "bundle unhold", "mcp server edit",
		"agent default",
	} {
		_, ok := formatDebtAllowlist[path]
		assert.True(t, ok, "%q must stay in formatDebtAllowlist: the debt is tracked, not forgotten", path)
	}

	// End-to-end on one of them. `agent default` with no argument succeeds on
	// an empty project (it reports that no default agent is set), so RunE
	// returns nil and the guard's window — "exited 0 having silently ignored
	// --format" — is exactly what is reached.
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"agent", "default", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		// rootCmd's --format is a process-wide PersistentFlags() value that
		// pflag does NOT reset between Execute() calls on the same tree, and
		// its PersistentPreRun flips the process-wide structured-diagnostics
		// channel to match. Leaving either set reroutes every later test's
		// warnings through the JSON renderer (test-order hazard, not a product
		// bug — one CLI process handles exactly one invocation).
		_ = rootCmd.PersistentFlags().Set("format", formatText)
		clidiag.SetStructured(false)
	})

	err := rootCmd.Execute()
	require.Error(t, err, "--format json on an unwired command must not exit 0 pretending to have honored it")
	assert.Contains(t, err.Error(), "does not support it yet")
	assert.Contains(t, err.Error(), "formatDebtAllowlist", "the error names where the tracked fix lives")
}

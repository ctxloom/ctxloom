package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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

func TestResolveFormat_AcceptsAllFiveEncodings(t *testing.T) {
	for _, want := range []clifmt.Format{
		clifmt.FormatJSON, clifmt.FormatYAML, clifmt.FormatTOML, clifmt.FormatText, clifmt.FormatMarkdown,
	} {
		cmd, _ := formatCmd(string(want))
		got, err := resolveFormat(cmd)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

// --- U104-F01: the runtime guard that turns "--format is registered
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

// TestConfigShow_UnwiredCommand_FormatJSONErrorsLoudly is the end-to-end pin:
// `config show` is a real, currently-unwired command (format_coverage_test.go's
// formatDebtAllowlist: "config.go: runConfigShow must route through emit()
// instead of calling renderConfigYAML directly" — owned by a different flow
// batch, not touched here) — exactly U104-F01's "accepted and silently
// ignored on dozens of commands" shape. Before this guard it printed human
// YAML text and exited 0 regardless of --format json; now it must refuse
// loudly instead of lying about having honored the flag.
func TestConfigShow_UnwiredCommand_FormatJSONErrorsLoudly(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"config", "show", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	require.Error(t, err, "an unwired command must not silently exit 0 under a non-text --format")
	assert.Contains(t, err.Error(), "does not support it yet")
}

// TestConfigShow_UnwiredCommand_DefaultTextStillWorks is the negative
// control: the same unwired command with NO --format (defaults to text) must
// keep working exactly as before — the guard only gates non-text formats.
func TestConfigShow_UnwiredCommand_DefaultTextStillWorks(t *testing.T) {
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
	rootCmd.SetArgs([]string{"config", "show", "--format", "text"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	require.NoError(t, rootCmd.Execute())
	assert.NotEmpty(t, out.String())
}

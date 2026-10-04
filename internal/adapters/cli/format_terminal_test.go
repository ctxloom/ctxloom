package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// unsetFormatCmd is a command whose --format is registered and never Set: the
// invocation that asked for nothing, so cliemit.Resolve derives the format
// from whether stdout is a terminal. Those are the arms these tests drive.
func unsetFormatCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	resetApp()
	cmd := &cobra.Command{Use: "widget frobnicate"}
	cmd.Flags().String("format", string(clifmt.FormatText), "")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

// TestWantsStructuredOutput_FollowsTheTerminalWhenNothingWasAsked covers both
// arms. A human at a terminal who asked for nothing gets text, so nothing
// machine-only is stamped and no prompt is withheld from them; the same
// invocation piped is a script, and gets the structured treatment.
func TestWantsStructuredOutput_FollowsTheTerminalWhenNothingWasAsked(t *testing.T) {
	t.Run("terminal", func(t *testing.T) {
		t.Cleanup(cliemit.OverrideTerminal(true))
		cmd, _ := unsetFormatCmd(t)
		assert.False(t, wantsStructuredOutput(cmd), "a human at a terminal who asked for nothing is not a script")
	})
	t.Run("not a terminal", func(t *testing.T) {
		t.Cleanup(cliemit.OverrideTerminal(false))
		cmd, _ := unsetFormatCmd(t)
		assert.True(t, wantsStructuredOutput(cmd), "a piped invocation derives json, which a script parses")
	})
}

// TestEmit_FollowsTheTerminalWhenNothingWasAsked: at a terminal the bespoke
// text closure is what renders; off one, the same call renders the value.
func TestEmit_FollowsTheTerminalWhenNothingWasAsked(t *testing.T) {
	withFormatGuardReset(t)
	t.Run("terminal", func(t *testing.T) {
		t.Cleanup(cliemit.OverrideTerminal(true))
		cmd, buf := unsetFormatCmd(t)
		ran := false
		require.NoError(t, emit(cmd, emitTestItem{Name: "abc"}, func() error { ran = true; return nil }))
		assert.True(t, ran, "a human at a terminal gets the text rendering")
		assert.Empty(t, buf.String(), "nothing but the text closure may write")
	})
	t.Run("not a terminal", func(t *testing.T) {
		t.Cleanup(cliemit.OverrideTerminal(false))
		cmd, buf := unsetFormatCmd(t)
		require.NoError(t, emit(cmd, emitTestItem{Name: "abc"}, func() error {
			t.Fatal("a piped invocation must not get the text rendering")
			return nil
		}))
		assert.JSONEq(t, `{"name":"abc"}`, buf.String())
	})
}

// TestPersistentPreRun_StructuredDiagnosticsFollowTheTerminal: the warnings
// side channel is structured exactly when the output is, so a derived format
// moves it too — plain at a terminal, structured when piped.
func TestPersistentPreRun_StructuredDiagnosticsFollowTheTerminal(t *testing.T) {
	t.Cleanup(func() { clidiag.SetStructured(false) })
	for _, c := range []struct {
		name         string
		terminal     bool
		wantJSONLine bool
	}{
		{"terminal", true, false},
		{"not a terminal", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(cliemit.OverrideTerminal(c.terminal))
			cmd, _ := unsetFormatCmd(t)
			_ = rootCmd.PersistentPreRunE(cmd, nil)

			var buf bytes.Buffer
			clidiag.Fwarn(&buf, "ctxloom", "probe")
			assert.Equal(t, c.wantJSONLine, json.Valid(buf.Bytes()), "Fwarn output %q", buf.String())
		})
	}
}

// TestFormatGuards_PassAHumanAtATerminal: an invocation at a terminal that
// asked for nothing resolves to text, which every --format guard lets
// through — none may refuse a human for a flag they never typed.
func TestFormatGuards_PassAHumanAtATerminal(t *testing.T) {
	withFormatGuardReset(t)
	t.Cleanup(cliemit.OverrideTerminal(true))
	for name, guard := range map[string]func(*cobra.Command) error{
		"checkFormatWasHonored":   checkFormatWasHonored,
		"refuseUnsupportedFormat": refuseUnsupportedFormat,
		"groupNodeFormatRefusal":  groupNodeFormatRefusal,
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _ := unsetFormatCmd(t)
			got, err := cliemit.Resolve(cmd)
			require.NoError(t, err)
			require.Equal(t, clifmt.FormatText, got, "the seam must put Resolve on its terminal arm")
			assert.NoError(t, guard(cmd))
		})
	}
}

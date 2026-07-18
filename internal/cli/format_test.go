package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

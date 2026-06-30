package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// formatCmd builds a bare command with the --format flag registered and set,
// mirroring how the inherited persistent flag reaches a subcommand at runtime.
func formatCmd(format string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.Flags().String("format", formatText, "")
	_ = cmd.Flags().Set("format", format)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

func TestEmit_JSON_MarshalsStructuredResult(t *testing.T) {
	cmd, buf := formatCmd("json")
	type item struct {
		Name string `json:"name"`
	}
	err := emit(cmd, item{Name: "abc"}, func() error {
		t.Fatal("text renderer must not run in json mode")
		return nil
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"abc"}`, buf.String())
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

func TestEmit_UnsetFormat_DefaultsToText(t *testing.T) {
	cmd := &cobra.Command{} // no --format flag registered → reads as ""
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	ran := false
	err := emit(cmd, struct{}{}, func() error { ran = true; return nil })
	require.NoError(t, err)
	assert.True(t, ran, "empty format must run the text renderer")
}

func TestEmit_UnknownFormat_Errors(t *testing.T) {
	cmd, _ := formatCmd("yaml")
	err := emit(cmd, struct{}{}, func() error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown format")
}

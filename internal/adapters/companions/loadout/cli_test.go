package loadout

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmit_YAML_WritesBytesVerbatim(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\nfragments:\n  x:\n    content: hi\n")
	var buf bytes.Buffer
	require.NoError(t, Emit(&buf, FormatYAML, bundle))
	assert.Equal(t, bundle, buf.Bytes())
}

// Any format but the document itself is refused without writing, json
// included: there is no envelope to emit.
func TestEmit_UnknownFormatErrorsWithoutWriting(t *testing.T) {
	for _, format := range []string{"toml", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			require.Error(t, Emit(&buf, format, []byte("x")))
			assert.Empty(t, buf.Bytes())
		})
	}
}

// TestEmit_EmptyBundleErrors proves a companion embedding zero bytes (a build
// mistake — forgot the go:embed directive, wrong glob, empty loadout.yaml)
// fails loud instead of emitting nothing a consumer would also accept.
func TestEmit_EmptyBundleErrors(t *testing.T) {
	var buf bytes.Buffer
	require.Error(t, Emit(&buf, FormatYAML, nil))
	assert.Empty(t, buf.Bytes(), "nothing must be written once the emptiness check fails")
}

func TestNewCommand_DefaultFormatIsYAML(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\n")
	cmd := NewCommand("acme", bundle)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Equal(t, bundle, buf.Bytes())
}

// A host CLI's --json shorthand reaches the command as a request for json,
// which is refused rather than silently answered with YAML.
func TestNewCommand_HostJSONShorthandIsRefused(t *testing.T) {
	root := &cobra.Command{Use: "host", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	cmd := NewCommand("acme", []byte("version: \"1.0.0\"\n"))
	root.AddCommand(cmd)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{Subcommand, "--json"})
	require.Error(t, root.Execute())
	assert.Empty(t, buf.Bytes())
}

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// runVersionCmd drives the real versionCmd's RunE with a throwaway parent that
// carries the given --format value, mirroring how the root's persistent flag
// reaches the command at runtime. An empty format leaves the flag at its
// default (text).
func runVersionCmd(t *testing.T, format string) (string, error) {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().String("format", "text", "")
	if format != "" {
		require.NoError(t, c.Flags().Set("format", format))
	}
	var buf bytes.Buffer
	c.SetOut(&buf)
	err := versionCmd.RunE(c, nil)
	return buf.String(), err
}

func TestVersionCmd_TextEmitsBareVersion(t *testing.T) {
	out, err := runVersionCmd(t, "text")
	require.NoError(t, err)
	assert.Equal(t, version+"\n", out)
}

func TestVersionCmd_JSONEmitsNameAndVersion(t *testing.T) {
	out, err := runVersionCmd(t, "json")
	require.NoError(t, err)
	var got cliversion.Info
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, cliversion.Info{Name: "taskloom", Version: version}, got)
}

// Routing version through clifmt (like cmd/ctxloom) widens it from text/json
// to the full five formats: yaml now renders instead of erroring.
func TestVersionCmd_YAMLRenders(t *testing.T) {
	out, err := runVersionCmd(t, "yaml")
	require.NoError(t, err)
	assert.Contains(t, out, "name: taskloom")
	assert.Contains(t, out, "version: "+version)
}

func TestVersionCmd_UnknownFormatErrors(t *testing.T) {
	out, err := runVersionCmd(t, "xml")
	require.Error(t, err)
	assert.Empty(t, out)
	assert.True(t, strings.Contains(err.Error(), "unsupported format") || strings.Contains(err.Error(), "xml"))
}

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

// runVersionCmd drives the real versionCmd with the given --format and returns
// its stdout. It exercises taskloom's wiring (name="taskloom") on top of the
// shared cliversion.Render contract.
func runVersionCmd(t *testing.T, format string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	versionCmd.SetOut(&buf)
	t.Cleanup(func() { versionCmd.SetOut(nil) })
	prev := versionFormat
	versionFormat = format
	t.Cleanup(func() { versionFormat = prev })
	err := versionCmd.RunE(versionCmd, nil)
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

func TestVersionCmd_UnknownFormatErrors(t *testing.T) {
	out, err := runVersionCmd(t, "yaml")
	require.Error(t, err)
	assert.Empty(t, out)
}

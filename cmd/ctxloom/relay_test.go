package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The command claude's MCP entry spawns is composed into the binary, hidden
// from the CLI's documented tree, and refuses at once — on stderr, never on
// the stdout that is its MCP channel — when the entry named no endpoint.
func TestComposedBinary_CarriesClaudesHiddenRelay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(claude.EnvRelayURL, "")
	t.Setenv(claude.EnvRelayBearer, "")
	comp := compose(&report.Collector{})

	cmd, _, err := cli.GetRootCmd(comp).Find([]string{claude.RelayCommand})
	require.NoError(t, err)
	require.Equal(t, claude.RelayCommand, cmd.Name())
	assert.True(t, cmd.Hidden, "a machine callback is not part of the documented CLI")

	var stdout bytes.Buffer
	code := cli.RunWithArgs(comp, []string{claude.RelayCommand}, &stdout)
	assert.NotEqual(t, 0, code)
	assert.Empty(t, stdout.String(), "stdout is the relay's MCP channel")
}

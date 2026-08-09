package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManageInstall_WarnsWhenTheRegisteredCommandCannotBeResolved pins the
// missing half of what `manage install` promises. The entry it writes names a
// BARE command ("taskloom"), resolved against whichever PATH the agent happens
// to have at some future invocation — nothing about it is checked at the
// moment of registration, so an install onto a machine where the binary is not
// reachable reports "registered MCP server for claude-code" and produces a
// server that silently never starts, hours later and far from this command.
// `manage check` already runs exactly this lookup; install must not be the
// one path that declines to.
//
// The assertion is on the PAYLOAD (the diagnostic the user actually sees), not
// on an exit code: install still succeeds, because a config written now for a
// binary installed later is legitimate.
func TestManageInstall_WarnsWhenTheRegisteredCommandCannotBeResolved(t *testing.T) {
	home := fakeHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	// An empty PATH: nothing named taskloom can resolve from here.
	t.Setenv("PATH", t.TempDir())

	var errOut bytes.Buffer
	require.NoError(t, manageInstall("", ".", true, false, &errOut))

	out := errOut.String()
	assert.Contains(t, out, "PATH", "the warning must name what could not resolve the command")
	assert.Contains(t, out, "taskloom", "the warning must name the command that will not start")

	// The registration itself still happened — the warning is a diagnostic,
	// not a refusal.
	servers := readServers(t, filepath.Join(home, ".claude.json"))
	require.Contains(t, servers, "taskloom")
}

// TestManageInstall_SilentWhenTheRegisteredCommandResolves is the other half:
// the check must not cry wolf on the ordinary case, or the warning stops
// meaning anything. A directory holding an executable named taskloom is
// enough to satisfy the lookup the registered entry will later perform.
func TestManageInstall_SilentWhenTheRegisteredCommandResolves(t *testing.T) {
	home := fakeHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "taskloom"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", bin)

	var errOut bytes.Buffer
	require.NoError(t, manageInstall("", ".", true, false, &errOut))

	assert.NotContains(t, errOut.String(), "PATH",
		"a resolvable command must produce no PATH diagnostic at all")
}

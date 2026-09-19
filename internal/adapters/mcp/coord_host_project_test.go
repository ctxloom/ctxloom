package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A relayed host tool runs in the COORDINATOR's process, whose cwd is the
// originator's project — but it runs on behalf of the CALLER, whose cell may
// be a different project entirely (a delegated child in its own worktree, or a
// runner hosted for another project). The identity relayHost hands the handler
// (serverFor(caller)) already names the caller's project; a handler that
// consults os.Getwd() instead answers for the wrong project and never notices.
//
// Both projects carry a session so the assertion has a direction: the caller's
// session is listed AND the host's is not.
func TestRelayHost_ListSessionsResolvesTheCallersProjectNotTheHostsCwd(t *testing.T) {
	testsupport.Isolate(t)
	hostProject := t.TempDir()
	callerProject := t.TempDir()
	testsupport.ChangeDir(t, hostProject)

	hostEntry, err := operations.AssignSessionHarp(hostProject, "mock")
	require.NoError(t, err)
	callerEntry, err := operations.AssignSessionHarp(callerProject, "mock")
	require.NoError(t, err)

	handlers := coordCustomHandlers(&config.Config{}, nil)
	relay := handlers[coord.CustomToolPrefix+"list_sessions"]
	require.NotNil(t, relay, "list_sessions is a host-relayed tool")

	caller := coord.Identity{Harp: callerEntry.HarpName, Project: callerProject}
	raw, err := relay(context.Background(), caller, nil)
	require.NoError(t, err)

	var out listSessionsResult
	require.NoError(t, json.Unmarshal(raw, &out))
	harps := make([]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		harps = append(harps, s.Harp)
	}
	assert.Contains(t, harps, callerEntry.HarpName,
		"the relayed tool must list the CALLER's project's sessions")
	assert.NotContains(t, harps, hostEntry.HarpName,
		"the coordinator's own cwd is not the caller's project; its sessions must not leak into the caller's listing")
}

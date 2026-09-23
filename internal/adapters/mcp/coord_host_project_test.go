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
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A relayed host tool runs in the COORDINATOR's process, whose cwd is the
// originator's project — but it runs on behalf of the CALLER, whose cell may
// be a different project entirely (a delegated child in its own worktree, or a
// runner hosted for another project). The HostApp serves every Verbs.Host
// frame under the caller's identity, which names the caller's project; a
// handler that consults os.Getwd() instead answers for the wrong project and
// never notices.
//
// Both projects carry a session so the assertion has a direction: the caller's
// session is listed AND the host's is not.
func TestHostApp_ListSessionsResolvesTheCallersProjectNotTheHostsCwd(t *testing.T) {
	testsupport.Isolate(t)
	hostProject := t.TempDir()
	callerProject := t.TempDir()
	testsupport.ChangeDir(t, hostProject)

	hostEntry, err := operations.AssignSessionHarp(hostProject, "mock")
	require.NoError(t, err)
	callerEntry, err := operations.AssignSessionHarp(callerProject, "mock")
	require.NoError(t, err)

	app := NewHostApp(&config.Config{}, engines.Registry())

	caller := coord.Identity{Harp: callerEntry.HarpName, ProjectDir: callerProject}
	res, err := app.Serve(context.Background(), caller, coord.HostRequest{Tool: "list_sessions"})
	require.NoError(t, err)

	var out listSessionsResult
	require.NoError(t, json.Unmarshal(res.Body, &out))
	harps := make([]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		harps = append(harps, s.Harp)
	}
	assert.Contains(t, harps, callerEntry.HarpName,
		"the relayed tool must list the CALLER's project's sessions")
	assert.NotContains(t, harps, hostEntry.HarpName,
		"the coordinator's own cwd is not the caller's project; its sessions must not leak into the caller's listing")
}

// TestHostApp_AnUnknownToolIsRefused: the set the app serves is derived —
// every tool that reads the sessions root or cross-session history — and a
// name outside it is refused by name, never answered by a default.
func TestHostApp_AnUnknownToolIsRefused(t *testing.T) {
	_, err := NewHostApp(&config.Config{}, engines.Registry()).Serve(context.Background(), coord.Identity{Harp: "h"}, coord.HostRequest{Tool: "agent_run"})
	require.ErrorIs(t, err, coord.ErrUnknownHostTool)
}

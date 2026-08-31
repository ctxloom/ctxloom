package coord

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestEnqueueRun_JournalsPermissionAndMCPServerNames is THE GAP (taskloom
// spare-chevy): a child's resolved permission mode and MCP server set are
// composed at spawn (spawner.go's headlessSafePermission/childMCPServers)
// but, before this test existed, journaled NOWHERE — the run registry
// (facts.go's runEnqueued) recorded everything else about the run
// (RunID/Harp/Agent/lineage/Ladder) except the two fields that ARE the
// delegation security claim. This pins that enqueueRun now journals both,
// in the SAME kind-name/names-only shape Ladder already established.
func TestEnqueueRun_JournalsPermissionAndMCPServerNames(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {
			perm:     "bypass",
			profiles: []string{"p1"},
			mcpServers: []agent.ChatMCPServer{
				{Name: "server-a", Command: "echo", Args: []string{"a"}},
				{Name: "server-b", Command: "echo", Args: []string{"b"}},
			},
		},
	}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)

	var rec *RunRecord
	require.Eventually(t, func() bool {
		c.runs.View(func() { rec = c.runsF.currentRun(out.Harp) })
		return rec != nil
	}, conformanceWait, 10*time.Millisecond)

	assert.Equal(t, "bypass", rec.Permission, "the resolved permission mode's kind name, not a wire number")
	assert.ElementsMatch(t, []string{"server-a", "server-b"}, rec.MCPServers)
}

// TestEnqueueRun_ChildMCPServers_JournalDisjointPerAgent is the security
// property this whole change exists for: two children resolving DISJOINT
// profile sets must each journal ONLY their own agent's MCP servers — never
// a sibling's, never the parent's (the parent has none here, but the point
// generalizes: prodSpawner.childMCPServers composes strictly from
// plan.Profiles, and enqueueRun journals exactly what Resolve put on the
// plan, so this is testable at the fake-spawner layer without a real
// engine). Modeled on
// internal/mcp/mcp_tools_agents_test.go's
// TestProdSpawner_ChildMCPServers_ScopedPerAgent, which pins the same
// property against the REAL prodSpawner composition; this one pins the
// JOURNAL side of the same guarantee.
func TestEnqueueRun_ChildMCPServers_JournalDisjointPerAgent(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"workerA": {
			perm:       "bypass",
			profiles:   []string{"p-a"},
			mcpServers: []agent.ChatMCPServer{{Name: "server-a", Command: "echo", Args: []string{"a"}}},
		},
		"workerB": {
			perm:       "bypass",
			profiles:   []string{"p-b"},
			mcpServers: []agent.ChatMCPServer{{Name: "server-b", Command: "echo", Args: []string{"b"}}},
		},
	}, nil)
	c := newTestCoordinator(t, sp, nil)

	outA, err := c.AgentRun(context.Background(), ownerIdentity(), "workerA", "go", "", "")
	require.NoError(t, err)
	outB, err := c.AgentRun(context.Background(), ownerIdentity(), "workerB", "go", "", "")
	require.NoError(t, err)

	var recA, recB *RunRecord
	require.Eventually(t, func() bool {
		c.runs.View(func() {
			recA = c.runsF.currentRun(outA.Harp)
			recB = c.runsF.currentRun(outB.Harp)
		})
		return recA != nil && recB != nil
	}, conformanceWait, 10*time.Millisecond)

	assert.Equal(t, []string{"server-a"}, recA.MCPServers, "workerA's journaled entry must carry ONLY its own server")
	assert.Equal(t, []string{"server-b"}, recB.MCPServers, "workerB's journaled entry must carry ONLY its own server")
	assert.NotContains(t, recA.MCPServers, "server-b", "workerA must never journal its sibling's server")
	assert.NotContains(t, recB.MCPServers, "server-a", "workerB must never journal its sibling's server")
}

// TestListRuns_SurfacesPermissionAndMCPServerNames pins the ROSTER side:
// the "roster" MCP tool (mcpschema.ToolRoster) is backed by
// Coordinator.ListRuns/listRunsSnapshot (consumer.go), the only black-box
// surface a real operator has onto a live delegation. Before this test, an
// operator calling roster could see a child's agent/phase/lineage but
// NOTHING about what privileges it actually got.
func TestListRuns_SurfacesPermissionAndMCPServerNames(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {
			perm:     "plan",
			profiles: []string{"p1"},
			mcpServers: []agent.ChatMCPServer{
				{Name: "server-a", Command: "echo", Args: []string{"a"}},
			},
		},
	}, nil)
	c := newTestCoordinator(t, sp, nil)

	_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)

	var result = c.ListRuns(true, "")
	require.Eventually(t, func() bool {
		result = c.ListRuns(true, "")
		return len(result.GetRuns()) == 1
	}, conformanceWait, 10*time.Millisecond)

	info := result.GetRuns()[0]
	assert.Equal(t, "plan", info.GetPermissionMode())
	assert.Equal(t, []string{"server-a"}, info.GetMcpServers())
}

// TestRunsFold_OldEntryMissingPrivilegeFields_LoadsWithoutError is the
// no-backward-compat-shim discipline applied to THIS change (CLAUDE.md: no
// migration, no versioning shim — new fields are omitempty, old entries
// simply lack them). It writes a raw run.enqueued line, byte for byte as an
// entry journaled before Permission/MCPServers existed would look (no
// "permission" or "mcp_servers" key at all — not merely a Go zero value,
// which would prove nothing about the ACTUAL old wire shape), and asserts
// the fold loads it without error and leaves the new fields empty rather
// than crashing or fabricating data.
func TestRunsFold_OldEntryMissingPrivilegeFields_LoadsWithoutError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	oldLine := `{"kind":"run.enqueued","at":"2026-01-01T00:00:00Z","data":{"run_id":"run-old","harp":"harp-old","agent":"worker","cred_hash":"h1","depth":1}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(oldLine), 0o600))

	runsF, queueF, rosterF := newRunsFold(), newQueueFold(), newRosterFold()
	store, err := openStore(path, runsF, queueF, rosterF)
	require.NoError(t, err, "an old journal entry lacking the new privilege fields must still load")
	t.Cleanup(func() { _ = store.Close() })

	rec := runsF.run("run-old")
	require.NotNil(t, rec)
	assert.Empty(t, rec.Permission, "an old entry has no permission — must stay empty, never fabricated")
	assert.Empty(t, rec.MCPServers, "an old entry has no mcp_servers — must stay empty, never fabricated")
}

// TestRunsFold_EntryCarryingARetiredLadderKey_LoadsWithoutError is the
// forward half of the same leniency, and it is the one the ladder's removal
// rests on: journals written before the escalation ladder was retired still
// carry a "ladder" key that NOTHING decodes any more. Fact.decode is a plain
// json.Unmarshal and DisallowUnknownFields appears nowhere in this package,
// so the unknown key is ignored and the record still loads. If anyone makes
// the decoder strict, this goes red instead of every existing project
// failing to start.
func TestRunsFold_EntryCarryingARetiredLadderKey_LoadsWithoutError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	line := `{"kind":"run.enqueued","at":"2026-01-01T00:00:00Z","data":{"run_id":"run-lad","harp":"harp-lad","agent":"worker","cred_hash":"h2","depth":1,"permission":"bypass","ladder":[{"kinds":["COMMAND_EXECUTION"],"action":"relay_to_role","role":"parent","timeout":"10s"}]}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))

	runsF, queueF, rosterF := newRunsFold(), newQueueFold(), newRosterFold()
	store, err := openStore(path, runsF, queueF, rosterF)
	require.NoError(t, err, "a journal entry carrying the retired ladder key must still load")
	t.Cleanup(func() { _ = store.Close() })

	rec := runsF.run("run-lad")
	require.NotNil(t, rec, "the record must replay despite the unknown ladder key")
	assert.Equal(t, "harp-lad", rec.Harp)
	assert.Equal(t, "bypass", rec.Permission, "the fields that DO still exist must decode normally alongside it")
}

// TestEnqueueRun_JournalCarriesNamesOnly_NeverCommandOrArgs is the explicit
// no-secret-leakage test the change's own design boundary demands: an MCP
// server's COMMAND LINE can carry a credential (an API key in an env var or
// an arg); its NAME cannot. The journal already refuses to store the bearer
// token (CredHash, a SHA-256, per facts.go's comment) — this holds the exact
// same line for MCP composition. It plants an unmistakable "secret" token in
// both Command and Args and asserts the raw journaled JSON on disk contains
// the server's NAME but neither the command word nor the planted secret.
func TestEnqueueRun_JournalCarriesNamesOnly_NeverCommandOrArgs(t *testing.T) {
	resetStrictness(t)
	const plantedSecret = "sk-do-not-leak-this-9f8e7d"
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {
			perm:     "bypass",
			profiles: []string{"p1"},
			mcpServers: []agent.ChatMCPServer{
				{Name: "server-a", Command: "some-secret-launcher-binary", Args: []string{"--token", plantedSecret}},
			},
		},
	}, nil)
	stateDir := t.TempDir()
	c, err := New(Options{ProjectDir: t.TempDir(), StateDir: stateDir, Spawner: sp})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)

	var rec *RunRecord
	require.Eventually(t, func() bool {
		c.runs.View(func() { rec = c.runsF.currentRun(out.Harp) })
		return rec != nil
	}, conformanceWait, 10*time.Millisecond)
	require.Equal(t, []string{"server-a"}, rec.MCPServers)

	raw, err := os.ReadFile(filepath.Join(stateDir, "runs.jsonl"))
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "server-a", "the journal must name the server so an audit can see what a child received")
	assert.NotContains(t, text, "some-secret-launcher-binary", "the journal must NEVER carry the server's command")
	assert.NotContains(t, text, plantedSecret, "the journal must NEVER carry an MCP server's args/env — that is where a secret lives")
	assert.NotContains(t, text, "--token", "the journal must never carry MCP server argv at all")
}

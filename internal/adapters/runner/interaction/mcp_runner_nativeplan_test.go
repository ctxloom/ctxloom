package interaction

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// nativePlanSession is a live coordinator, the session owner's home on it,
// a session home holding the engine's native plans dir, and a client on the
// session's MCP surface.
type nativePlanSession struct {
	home        *runner.Home
	sessionHome string
	cs          *mcp.ClientSession
}

const nativePlanHarp = "owner-harp"

func newNativePlanSession(t *testing.T) nativePlanSession {
	t.Helper()
	cwd := testsupport.ProjectDir(t)
	c, err := coord.New(coord.Options{
		ProjectDir: cwd,
		StateDir:   t.TempDir(),
		Spawner:    spawn.New(nil, fixtureApp(t, testConfig()), cwd, nil),
		OwnerHarp:  nativePlanHarp,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, coordgrpc.Serve(c))
	token, err := c.RegisterSessionOwner(nativePlanHarp)
	require.NoError(t, err)
	home, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL: c.LoopbackURL(), Token: token, Harness: "mock", Version: "test", Harp: nativePlanHarp,
	})
	require.NoError(t, err)
	t.Cleanup(func() { home.Close(0, "") })

	sessionHome := filepath.Join(t.TempDir(), "home", ".claude")
	require.NoError(t, os.MkdirAll(filepath.Join(sessionHome, nativePlansDir), 0o755))
	server, err := NewServer(report.To(nil), home, nativePlanHarp, cwd, sessionHome, false, loadoutSurface{}, NewWakeSignal(nil))
	require.NoError(t, err)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return nativePlanSession{home: home, sessionHome: sessionHome, cs: cs}
}

// writePlan writes a native plan, its mtime at, and returns its path.
func (s nativePlanSession) writePlan(t *testing.T, name, body string, at time.Time) string {
	t.Helper()
	p := filepath.Join(s.sessionHome, nativePlansDir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	require.NoError(t, os.Chtimes(p, at, at))
	return p
}

// fetch has the session fetch one of its own artifacts and returns its bytes.
func (s nativePlanSession) fetch(t *testing.T, artifactID string) []byte {
	t.Helper()
	args, err := json.Marshal(map[string]any{"agent_id": nativePlanHarp, "artifact_id": artifactID, "dest_path": "fetched/" + filepath.Base(artifactID)})
	require.NoError(t, err)
	res, err := s.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "agent_fetch_artifact", Arguments: json.RawMessage(args)})
	require.NoError(t, err)
	require.False(t, res.IsError, "agent_fetch_artifact: %+v", res)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(cwd, "fetched", filepath.Base(artifactID)))
	require.NoError(t, err)
	return got
}

// TestAgentReport_StampsTheEnginesNativePlans: the engine's own plans,
// under the session home, are stamped as plan artifacts like the agent's
// *.plan.md, and a parent can fetch them.
func TestAgentReport_StampsTheEnginesNativePlans(t *testing.T) {
	s := newNativePlanSession(t)
	s.writePlan(t, "plan-add-hello.md", "# Plan\n1. add hello.txt\n", time.Now())
	require.NoError(t, os.WriteFile(filepath.Join(s.sessionHome, nativePlansDir, "notes.txt"), []byte("not a plan"), 0o644))

	args, err := json.Marshal(map[string]any{"scope": "SCOPE_PROGRESS", "text": "planned"})
	require.NoError(t, err)
	res, err := s.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "agent_report", Arguments: json.RawMessage(args)})
	require.NoError(t, err)
	require.False(t, res.IsError, "agent_report: %+v", res)
	var out struct {
		ArtifactIDs []string `json:"artifact_ids"`
	}
	require.NoError(t, decodeStructured(res, &out))
	assert.Equal(t, []string{"plan/plan-add-hello"}, out.ArtifactIDs)
	assert.Equal(t, "# Plan\n1. add hello.txt\n", string(s.fetch(t, "plan/plan-add-hello")))
}

// TestTurnPlan_NamesTheNewestNativePlanAndPublishesIt: at a turn's end the
// stamper publishes the engine's newest plan and names it; a plan unchanged
// since is named again without a second upload; no plan names nothing.
func TestTurnPlan_NamesTheNewestNativePlanAndPublishesIt(t *testing.T) {
	s := newNativePlanSession(t)
	stamper := &artifactStamper{harp: nativePlanHarp, sessionHome: s.sessionHome}
	ctx := context.Background()
	assert.Empty(t, stamper.turnPlan(ctx, report.To(nil), s.home), "no plan written: nothing to name")

	now := time.Now()
	s.writePlan(t, "plan-old.md", "old", now.Add(-time.Hour))
	s.writePlan(t, "plan-new.md", "new", now)
	assert.Equal(t, "plan/plan-new", stamper.turnPlan(ctx, report.To(nil), s.home))
	assert.Equal(t, "new", string(s.fetch(t, "plan/plan-new")), "named only once it is fetchable")
	assert.Equal(t, "plan/plan-new", stamper.turnPlan(ctx, report.To(nil), s.home), "an unchanged plan is still the turn's plan")
}

// TestPlanCandidates_NoSessionHomeNoNativePlans: a run on the host's own
// engine home (engine_home: host) has no session home, so nothing there is
// attributable to it and nothing is stamped from it.
func TestPlanCandidates_NoSessionHomeNoNativePlans(t *testing.T) {
	testsupport.Isolate(t)
	got, err := (&artifactStamper{harp: "fuzzy-blank-heron"}).nativePlanCandidates()
	require.NoError(t, err)
	assert.Empty(t, got)
}

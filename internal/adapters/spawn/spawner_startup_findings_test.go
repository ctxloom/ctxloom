package spawn

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// TestProdSpawner_ResolveLaunch_TheChildIsToldWhatWasWithheld: a delegated
// child composes startup findings the way a top-level launch does, and
// receives them in its OWN package — the carrier the runner redeems. Its
// agent's profile vetoes the server a fragment is linked to, so that fragment
// is withheld from the child, and the child's context names it, its kind and
// why, while the withheld body stays out. What the coordinator's process
// recorded before this spawn is the coordinator's, not the child's: it is not
// listed.
func TestProdSpawner_ResolveLaunch_TheChildIsToldWhatWasWithheld(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "schema_version: 7\nruntime: host\nworkspace: none\nagents:\n  dev:\n    llm: mock\n    profiles: [vetoed]\n    permissions:\n      mock:\n        mode: bypass\n")
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "vetoed.yaml"),
		[]byte("bundles:\n  - linked\nexclude_mcp:\n  - think\n"), 0o644))
	bundletree.WriteOS(t, paths.LocalBundlesPathFor(appDir, paths.LayoutV2), "linked", `version: "1.0"
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=think]
fragments:
  guide:
    content: "LINKED-GUIDE"
    tags: [ctxloom:link_id=think]
  plain:
    content: "PLAIN-FRAGMENT"
`)
	projectDir := filepath.Dir(appDir)
	s := newSpawner(termRep(), spawnerApp(t, appDir), projectDir, nil)
	ctx := context.Background()

	plan, err := s.Resolve(ctx, "dev")
	require.NoError(t, err)
	harp, err := s.AssignSession(projectDir, plan.Backend)
	require.NoError(t, err)
	strictness.Record(report.KindConfig, "", "COORDINATOR-ONLY-FINDING")
	resolved, err := s.ResolveLaunch(ctx, plan, coord.SpawnStart{Identity: sessions.Identity{Harp: harp, Depth: 1}, Prompt: "go"})
	require.NoError(t, err, "a withhold is not a fault: the child's launch must resolve")

	deps, err := operations.LaunchDepsFor(s.app.LaunchFacts(), plan.Snapshot)
	require.NoError(t, err)
	pkg, err := launch.Open(ctx, deps.ForSession(harp), resolved.Launch)
	require.NoError(t, err)
	assert.Contains(t, pkg.Context.Text, "PLAIN-FRAGMENT")
	assert.NotContains(t, pkg.Context.Text, "LINKED-GUIDE")
	assert.Contains(t, pkg.Context.Text, "DOCTOR-CHECK-WITHHELD-ITEMS")
	assert.Contains(t, pkg.Context.Text, "fragment ctxloom+local:linked#fragments/guide")
	assert.Contains(t, pkg.Context.Text, `to MCP server "think", which this run was not granted`)
	assert.NotContains(t, pkg.Context.Text, "COORDINATOR-ONLY-FINDING")
}

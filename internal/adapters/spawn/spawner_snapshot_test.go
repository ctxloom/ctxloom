package spawn

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// countingSources wraps the real reader and counts its Reads: the gate is
// stated in how many times a spawn consults the sources, so the count is the
// assertion and everything else is the real path.
type countingSources struct {
	config.Sources
	reads atomic.Int32
}

func (c *countingSources) Read(ctx context.Context) (*config.Config, []config.Warning, error) {
	c.reads.Add(1)
	return c.Sources.Read(ctx)
}

// TestProdSpawner_Resolve_OneSnapshotPerSpawn is the slice's "one snapshot
// per spawn" gate: every agent_run reloads the configuration exactly ONCE —
// so an agent definition edited mid-session takes effect on the NEXT spawn —
// and never twice or not at all. The memoized loader made it 0 or N.
func TestProdSpawner_Resolve_OneSnapshotPerSpawn(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: plan\n")

	inner, err := configload.New(nil, nil, configload.WithAppDir(appDir))
	require.NoError(t, err)
	src := &countingSources{Sources: inner}
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	src.reads.Store(0)

	s := newSpawner(termRep(), operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims}), filepath.Dir(appDir), nil)

	_, err = s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, int32(1), src.reads.Load(), "one spawn is exactly one Read")

	_, err = s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, int32(2), src.reads.Load(), "the next spawn is the next Read, never a memo hit")

	// The edited definition is visible on the NEXT spawn and never before:
	// the snapshot published by the second spawn's Reload is what the
	// third spawn's Resolve reads from the owner after its own Reload.
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: plan\n  fresh:\n    llm: claude-code\n    permissions:\n      claude-code:\n        mode: bypass\n")
	plan, err := s.Resolve(context.Background(), "fresh")
	require.NoError(t, err, "an agent added mid-session resolves on the next spawn")
	assert.Equal(t, "claude-code", plan.Backend)
	assert.Equal(t, int32(3), src.reads.Load())
}

// spawnerApp opens the process composition a spawner test drives: the real
// reader pinned to appDir, plus ctxloom's OWN companion loadout — the repo's
// real cmd/ctxloom/loadout.yaml, read as the self-probe would read it, so a
// child's reach-back server is present the way it is for a real ctxloom.
// No remote readers, no other companion.
func spawnerApp(t *testing.T, appDir string) *operations.App {
	t.Helper()
	src, err := configload.New(nil, nil, configload.WithAppDir(appDir), configload.WithReaderSource(ctxloomOwnLoadoutReader(t)))
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	return operations.OpenedApp(owner, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
}

// packageDirAtStart is the test binary's working directory before any
// sandbox moves it: this package's directory, three levels below the repo
// root (-trimpath strips runtime.Caller's path, so the directory is the only
// anchor).
var packageDirAtStart, _ = os.Getwd()

// ctxloomOwnLoadoutReader is a reader source over ctxloom's own loadout as
// the self-probe reads it: the repo's real cmd/ctxloom/loadout.yaml, marked
// Self.
func ctxloomOwnLoadoutReader(t *testing.T) func(*config.Config) []bundles.Reader {
	t.Helper()
	require.NotEmpty(t, packageDirAtStart)
	doc, err := os.ReadFile(filepath.Join(packageDirAtStart, "..", "..", "..", "cmd", "ctxloom", "loadout.yaml"))
	require.NoError(t, err)
	self := bundles.CompanionLoadout{Bin: "ctxloom", Path: "/opt/build/ctxloom", Document: doc, Self: true}
	return func(cfg *config.Config) []bundles.Reader {
		probe := func(context.Context) (bundles.CompanionProbe, error) {
			return bundles.CompanionProbe{Loadouts: []bundles.CompanionLoadout{self}}, nil
		}
		return []bundles.Reader{bundles.NewCompanionReader(probe, bundles.WithTrustRoot(cfg.Trust().Root()))}
	}
}

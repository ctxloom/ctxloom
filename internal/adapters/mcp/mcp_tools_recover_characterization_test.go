package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const recoverBody = "Distilled: the recovered predecessor."

// recoverFixture isolates the session index, assigns a harp in a fresh project,
// and returns a server speaking as that harp whose distiller answers for
// distilled — the id the test expects recover to target.
func recoverFixture(t *testing.T, distilled string) (*ctxServer, string, *sessions.Manager) {
	t.Helper()
	testsupport.Isolate(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	return &ctxServer{
		facts:            testLaunchFacts(),
		self:             coord.Identity{Harp: entry.HarpName, ProjectDir: projectDir},
		cfg:              config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")}),
		compactorFactory: fixedCompactor(distilled, recoverBody),
	}, entry.HarpName, mgr
}

// linkLineage plants one lineage link per id in harp's dir, oldest first, each
// resolving to a transcript file whose base name is the id.
func linkLineage(t *testing.T, harp string, ids ...string) {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	logs := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i, id := range ids {
		target := filepath.Join(logs, id+".jsonl")
		require.NoError(t, os.WriteFile(target, canonicalRecords(harp, id), 0o644))
		mt := base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, os.Chtimes(target, mt, mt))
		require.NoError(t, os.Symlink(target, filepath.Join(dir, fmt.Sprintf("%sclaude-code-%s", paths.EngineTranscriptLinkPrefix, id))))
	}
}

func TestHandleRecoverSession_UnknownBackendIsAnError(t *testing.T) {
	s, _, _ := recoverFixture(t, "x")
	_, out, err := s.handleRecoverSession(context.Background(), nil, recoverSessionInput{Backend: "no-such-engine"})
	require.EqualError(t, err, "unknown backend: no-such-engine")
	require.Nil(t, out)
}

func TestHandleRecoverSession_NoProjectIsAnError(t *testing.T) {
	s, _, _ := recoverFixture(t, "x")
	s.self.ProjectDir = ""
	_, out, err := s.handleRecoverSession(context.Background(), nil, recoverSessionInput{Backend: "claude-code"})
	require.ErrorIs(t, err, errNoCallerProject)
	require.ErrorContains(t, err, "resolve project directory: ")
	require.Nil(t, out)
}

// With nothing but the current session anywhere, recover refuses rather than
// returning the session already in context.
func TestHandleRecoverSession_NothingToRecover(t *testing.T) {
	s, harp, mgr := recoverFixture(t, "x")
	const current = "4c1d3b8e-0000-4000-8000-000000000001"
	require.NoError(t, mgr.BindSession(harp, current, ""))
	linkLineage(t, harp, current)

	_, out, err := s.handleRecoverSession(context.Background(), nil, recoverSessionInput{Backend: "claude-code"})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.False(t, out.Loaded)
	require.Equal(t, fmt.Sprintf(recoverNothingToRecoverMsg, harp), out.Message)
}

// The lineage's newest session other than the current one is the target. The
// predecessor is not bound in the index, so its load reports a missing
// canonical transcript — naming the id recover chose.
func TestHandleRecoverSession_TargetsTheLineagePredecessor(t *testing.T) {
	const pred = "4c1d3b8e-0000-4000-8000-00000000000a"
	const current = "4c1d3b8e-0000-4000-8000-00000000000b"
	s, harp, mgr := recoverFixture(t, pred)
	require.NoError(t, mgr.BindSession(harp, current, ""))
	linkLineage(t, harp, pred, current)

	_, out, err := s.handleRecoverSession(context.Background(), nil, recoverSessionInput{Backend: "claude-code"})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.False(t, out.Loaded)
	require.Contains(t, out.Message, "No transcript was captured for session "+pred+" ")
	require.NotContains(t, out.Message, current)
}

// An empty backend falls back to the configured default, which the fixture
// leaves unset.
func TestHandleRecoverSession_EmptyBackendUsesTheDefault(t *testing.T) {
	s, _, _ := recoverFixture(t, "x")
	_, out, err := s.handleRecoverSession(context.Background(), nil, recoverSessionInput{SessionID: "any"})
	require.EqualError(t, err, "unknown backend: ")
	require.Nil(t, out)
}

// An explicit session_id skips resolution entirely — even when it names the
// current session — and is distilled and loaded.
func TestHandleRecoverSession_ExplicitSessionID(t *testing.T) {
	const id = "4c1d3b8e-0000-4000-8000-0000000000ee"
	s, harp, mgr := recoverFixture(t, id)
	require.NoError(t, mgr.BindSession(harp, id, ""))
	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonPath), 0o755))
	require.NoError(t, os.WriteFile(canonPath, canonicalRecords(harp, id), 0o644))

	_, out, err := s.handleRecoverSession(context.Background(), nil, recoverSessionInput{SessionID: id, Backend: "claude-code"})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.True(t, out.Loaded, out.Message)
	require.Contains(t, out.Content, recoverBody)
}

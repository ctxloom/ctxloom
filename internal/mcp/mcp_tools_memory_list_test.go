package mcp

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// bindProjectSession registers one harp-named session for projectDir with a
// real transcript file whose mtime is set to activity, so ActivityTime is
// deterministic (not the wall-clock StartedAt). Returns the assigned harp.
func bindProjectSession(t *testing.T, mgr *sessions.Manager, projectDir, backend, sessionID string, activity time.Time) string {
	t.Helper()
	e, err := mgr.AssignHarp(projectDir, backend)
	require.NoError(t, err)
	transcript := filepath.Join(t.TempDir(), sessionID+".jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o644))
	require.NoError(t, os.Chtimes(transcript, activity, activity))
	require.NoError(t, mgr.BindSession(e.HarpName, sessionID, transcript))
	return e.HarpName
}

// TestHandleListSessions_AllProjectsSortedByActivity is the list_sessions
// contract: all_projects returns every project's sessions, most-recent-first
// by ActivityTime, with the title from the index summary and a last_activity
// stamp in the CLI's local second-granularity format. Fails if the handler
// stops sorting, drops the title, or changes the timestamp shape.
func TestHandleListSessions_AllProjectsSortedByActivity(t *testing.T) {
	testsupport.Isolate(t) // isolate HOME → ~/.ctxloom is a temp index
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	projA := t.TempDir()
	projB := t.TempDir()
	now := time.Now()
	// Bind the STALE one first so insertion order is [B, A]; only an
	// activity-descending sort produces the expected [A, B]. A worked most
	// recently; B is an hour stale.
	harpB := bindProjectSession(t, mgr, projB, "codex", "sidB", now.Add(-time.Hour))
	harpA := bindProjectSession(t, mgr, projA, "claude-code", "sidA", now)
	require.NoError(t, mgr.SetSummary(harpA, "worked on A", nil, 0))

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(projA, ".ctxloom")})}
	_, out, err := s.handleListSessions(context.Background(), nil, listSessionsInput{AllProjects: true})
	require.NoError(t, err)
	require.Len(t, out.Sessions, 2)

	assert.Equal(t, harpA, out.Sessions[0].Harp, "most-recent activity must sort first")
	assert.Equal(t, harpB, out.Sessions[1].Harp)
	assert.Equal(t, "claude-code", out.Sessions[0].Backend)
	assert.Equal(t, "worked on A", out.Sessions[0].Title)
	assert.Empty(t, out.Sessions[1].Title, "B was never distilled → no title")

	fmtRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)
	for _, row := range out.Sessions {
		assert.Regexp(t, fmtRe, row.LastActivity, "last_activity must be local second-granularity")
	}
}

// TestHandleListSessions_DefaultScopeIsCwdProject confirms the default (no
// all_projects) scope returns only the current working directory's project,
// mirroring `ctxloom session list` without --all. Fails if the handler stops
// filtering by cwd.
func TestHandleListSessions_DefaultScopeIsCwdProject(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	projA := t.TempDir()
	projB := t.TempDir()
	now := time.Now()
	harpA := bindProjectSession(t, mgr, projA, "claude-code", "sidA", now)
	harpB := bindProjectSession(t, mgr, projB, "codex", "sidB", now)

	t.Chdir(projA)
	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(projA, ".ctxloom")})}
	_, out, err := s.handleListSessions(context.Background(), nil, listSessionsInput{})
	require.NoError(t, err)

	require.Len(t, out.Sessions, 1)
	assert.Equal(t, harpA, out.Sessions[0].Harp)
	for _, row := range out.Sessions {
		assert.NotEqual(t, harpB, row.Harp, "cwd scope must exclude other projects")
	}
}

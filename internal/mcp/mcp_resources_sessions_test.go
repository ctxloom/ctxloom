package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedSession records one session against projectDir and binds it a transcript
// whose mtime is activity — the signal sessions.ActivityTime orders on, pinned
// explicitly so ordering assertions never depend on how fast the test ran.
func seedSession(t *testing.T, projectDir string, activity time.Time) string {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	e, err := mgr.AssignHarp(projectDir, "claude")
	require.NoError(t, err)
	transcript := filepath.Join(t.TempDir(), e.HarpName+".jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("{}\n"), 0o644))
	require.NoError(t, os.Chtimes(transcript, activity, activity))
	require.NoError(t, mgr.BindSession(e.HarpName, "sess-"+e.HarpName, transcript))
	return e.HarpName
}

// TestHandleResourceSessionsRecent_ScopesToTheCallersProject pins that
// `ctxloom://sessions/recent` is documented as "sessions for the current
// project", and on the runner-terminated surface the current project is the
// CELL's work dir — which the ctxServer already carries as its identity.
// Reading os.Getwd() instead answers with whatever directory the serving
// process happens to sit in: on a workspace:worktree run the runner inherits
// the COORDINATOR's cwd, so the agent asking for "my project's sessions" was
// handed the parent project's list, or (for an unrelated cwd) an empty one.
func TestHandleResourceSessionsRecent_ScopesToTheCallersProject(t *testing.T) {
	// ProjectDir isolates HOME and parks the process cwd somewhere that is
	// deliberately NOT the cell — the divergence the row is about.
	processCwd := testsupport.ProjectDir(t)
	cell := t.TempDir()
	require.NotEqual(t, processCwd, cell)

	mine := seedSession(t, cell, time.Now())
	theirs := seedSession(t, processCwd, time.Now())

	s := &ctxServer{self: coord.Identity{Harp: "runner-harp", Project: cell}}
	req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceSessionsRecentURI}}
	res, err := s.handleResourceSessionsRecent(t.Context(), req)
	require.NoError(t, err)
	body := res.Contents[0].Text

	assert.Contains(t, body, mine, "the cell's own session must be listed")
	assert.NotContains(t, body, theirs, "a session belonging to the serving process's cwd must not leak into the cell's view")
}

// TestHandleResourceSessionsRecent_DeclaresItsTruncation pins that the
// handler caps the body at 25 rows. Without a marker in the payload a client
// reading 25 rows cannot tell a complete answer from a silently clipped one —
// so "the session I am looking for is not in the index" and "the session I am
// looking for is row 26" are the same observation, and the client stops
// looking.
func TestHandleResourceSessionsRecent_DeclaresItsTruncation(t *testing.T) {
	testsupport.ProjectDir(t)
	cell := t.TempDir()
	base := time.Now().Add(-time.Hour)
	const seeded = 30
	for i := range seeded {
		seedSession(t, cell, base.Add(time.Duration(i)*time.Minute))
	}

	s := &ctxServer{self: coord.Identity{Project: cell}}
	req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceSessionsRecentURI}}
	res, err := s.handleResourceSessionsRecent(t.Context(), req)
	require.NoError(t, err)

	var got struct {
		Sessions  []map[string]any `yaml:"sessions"`
		Total     int              `yaml:"total"`
		Truncated bool             `yaml:"truncated"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(res.Contents[0].Text), &got))
	assert.Len(t, got.Sessions, 25, "the cap itself is unchanged")
	assert.Equal(t, seeded, got.Total, "the payload must say how many sessions actually exist")
	assert.True(t, got.Truncated, "the payload must say it was clipped")
}

// TestHandleResourceSessionsRecent_DeclaresAnUntruncatedAnswer is the other
// half of the same contract: the marker must be present and FALSE when nothing
// was clipped. A field that only appears on truncation is no better than no
// field at all — its absence is still ambiguous with an older server.
func TestHandleResourceSessionsRecent_DeclaresAnUntruncatedAnswer(t *testing.T) {
	testsupport.ProjectDir(t)
	cell := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := range 3 {
		seedSession(t, cell, base.Add(time.Duration(i)*time.Minute))
	}

	s := &ctxServer{self: coord.Identity{Project: cell}}
	req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceSessionsRecentURI}}
	res, err := s.handleResourceSessionsRecent(t.Context(), req)
	require.NoError(t, err)

	body := res.Contents[0].Text
	var got struct {
		Sessions  []map[string]any `yaml:"sessions"`
		Total     int              `yaml:"total"`
		Truncated bool             `yaml:"truncated"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(body), &got))
	assert.Len(t, got.Sessions, 3)
	assert.Equal(t, 3, got.Total)
	assert.False(t, got.Truncated)
	assert.Contains(t, body, "truncated:", "the marker must be emitted, not omitted, when false")
}

// TestHandleResourceSessionsAll_MatchesSessionListAll pins that the
// handler's own doc claims parity with `ctxloom session list --all`, and that
// command reads operations.ListAllSessions — activity-sorted, most-recent
// first. The unsorted lister returns raw index order, which is CREATION order:
// for a resumed-and-worked session that is precisely backwards, and an agent
// reading "sessions, most recent first" off the top of the list gets the
// oldest one.
func TestHandleResourceSessionsAll_MatchesSessionListAll(t *testing.T) {
	testsupport.ProjectDir(t)
	projectA := t.TempDir()
	projectB := t.TempDir()
	base := time.Now().Add(-3 * time.Hour)

	// Creation order and activity order are deliberately OPPOSED: the
	// first-created session is the least recently worked, so raw index order
	// (creation) is the exact reverse of the answer this resource promises.
	leastRecentlyWorked := seedSession(t, projectA, base)
	middle := seedSession(t, projectB, base.Add(time.Hour))
	mostRecentlyWorked := seedSession(t, projectA, base.Add(2*time.Hour))

	s := &ctxServer{self: coord.Identity{Project: projectA}}
	req := &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceSessionsURI}}
	res, err := s.handleResourceSessionsAll(t.Context(), req)
	require.NoError(t, err)
	body := res.Contents[0].Text

	var got sessions.Index
	require.NoError(t, yaml.Unmarshal([]byte(body), &got))
	names := make([]string, 0, len(got.Sessions))
	for _, e := range got.Sessions {
		names = append(names, e.HarpName)
	}
	assert.Equal(t, []string{mostRecentlyWorked, middle, leastRecentlyWorked}, names,
		"the resource must be activity-sorted, most-recent-first")

	// And parity with the command it claims to mirror, stated directly.
	want, err := operations.ListAllSessions()
	require.NoError(t, err)
	wantNames := make([]string, 0, len(want))
	for _, e := range want {
		wantNames = append(wantNames, e.HarpName)
	}
	assert.Equal(t, wantNames, names, "ctxloom://sessions must agree with `session list --all`")
	assert.True(t, strings.Contains(body, "harp_name"), "the payload shape is unchanged")
}

// TestResourceProjectDir_FallsBackToCwdWithoutAnIdentity covers the other arm
// of the project-scoping fix: a ctxServer with no identity (docgen, tests,
// any caller that never threads a cell) still resolves the process cwd, so
// the fix is additive
// rather than a behaviour swap. The os.Getwd failure arm is deliberately not
// exercised — it is unreachable while the process has a valid cwd, and the
// point of the change is that it now returns an error instead of quietly
// listing sessions for "".
func TestResourceProjectDir_FallsBackToCwdWithoutAnIdentity(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	s := &ctxServer{}
	got, err := s.resourceProjectDir()
	require.NoError(t, err)
	// TMPDIR can resolve through a symlink, so compare resolved forms.
	wantResolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotResolved, err := filepath.EvalSymlinks(got)
	require.NoError(t, err)
	assert.Equal(t, wantResolved, gotResolved)
}

// TestResourceProjectDir_PrefersTheCallersIdentity is the positive arm: an
// identity-bearing server never consults the process cwd at all.
func TestResourceProjectDir_PrefersTheCallersIdentity(t *testing.T) {
	testsupport.ProjectDir(t)
	cell := t.TempDir()
	s := &ctxServer{self: coord.Identity{Harp: "h", Project: cell}}
	got, err := s.resourceProjectDir()
	require.NoError(t, err)
	assert.Equal(t, cell, got)
}

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestPreviousSessionByHarp_ReturnsCachedEssenceFromHarpDir pins the
// canonical/ACP branch of get_previous_session (viral-equal). An ACP-launched
// session never binds a backend SessionID; its only source is the harp's own
// captured transcript and its essence lives at
// ~/.ctxloom/sessions/<harp>/essence.md — NOT the legacy sessionID-keyed path
// under the project workdir that the backend branch reads. This test proves the
// by-harp path materializes such a session from the harp dir and, when the
// essence is fresh (stamped SourceSize matches the canonical transcript),
// returns it cached without spending an LLM call. If the handler regressed to
// the backend/sessionID path it would fail to resolve (empty session id) or read
// the wrong file.
func TestPreviousSessionByHarp_ReturnsCachedEssenceFromHarpDir(t *testing.T) {
	testsupport.Isolate(t) // isolate HOME → ~/.ctxloom is a temp index
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	projectDir := t.TempDir()
	// AssignHarp mints an ACP-style entry: a harp with no bound SessionID
	// (BindSession is never called on the ACP path).
	entry, err := mgr.AssignHarp(projectDir, "opencode")
	require.NoError(t, err)
	harp := entry.HarpName
	require.Empty(t, entry.SessionID, "ACP entry must have no backend session id")

	// The harp's own captured transcript — Find enriches CanonicalTranscriptPath
	// only when this file exists on disk.
	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonPath), 0o755))
	transcript := []byte("{\"role\":\"user\"}\n{\"role\":\"assistant\"}\n")
	require.NoError(t, os.WriteFile(canonPath, transcript, 0o644))

	// A pre-existing essence in the HARP dir (the correct location the fix
	// targets), plus a matching SourceSize so SourceStale reports fresh.
	essPath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	const essenceBody = "## Previous session\nPicked up where we left off.\n"
	require.NoError(t, os.WriteFile(essPath, []byte(essenceBody), 0o644))
	require.NoError(t, mgr.SetSummary(harp, "prev work", nil, int64(len(transcript))))

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")})}
	_, out, err := s.previousSessionByHarp(context.Background(), harp, "")
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.True(t, out.Loaded, "canonical previous session must materialize")
	assert.True(t, out.WasCached, "fresh essence must be reused, not re-distilled")
	assert.Equal(t, essenceBody, out.Content, "content must come from ~/.ctxloom/sessions/<harp>/essence.md")
	assert.Empty(t, out.SessionID, "ACP session carries no backend id")
}

// TestPreviousSessionByHarp_UnknownHarpDegrades confirms an unknown harp yields
// a usable "no previous session" result rather than a tool error — recovery
// must never block the agent.
func TestPreviousSessionByHarp_UnknownHarpDegrades(t *testing.T) {
	testsupport.Isolate(t)
	_, err := sessions.Open("")
	require.NoError(t, err)

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(t.TempDir(), ".ctxloom")})}
	_, out, err := s.previousSessionByHarp(context.Background(), "no-such-harp", "")
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.False(t, out.Loaded)
	assert.Contains(t, out.Message, "No previous session")
}

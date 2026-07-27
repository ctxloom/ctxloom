package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLoadOrDistillSession_NoCanonicalTranscript_ActionableMessage pins
// quit-eagle requirement 3: when a harp has no captured canonical transcript
// (the interactive-session capture gap tracked separately as petty-green —
// NOT this task's job to fix), recover_session's degraded message must name
// the harp AND the concrete remedy (`ctxloom session backfill <harp>`)
// instead of a bare "no canonical transcript captured"/"legacy scraper reader
// retired" wrapper a human has to decode themselves.
//
// Reproduces the real shape of the live failure (session d9c76e71-...,
// harp wild-timid-snout): a session-index entry binds a backend-native
// SessionID (a UUID) to a harp, but the harp's own transcript.jsonl was never
// captured (claude-code is a RetiredScraperBackends entry, so there is no
// legacy leg to fall back to). recover_session is called with the bound
// SessionID, exactly as handleRecoverSession does — not the harp directly —
// so this exercises CanonicalFallbackSource's sessionID->harp reverse lookup,
// the path the real incident actually took.
func TestLoadOrDistillSession_NoCanonicalTranscript_ActionableMessage(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const fakeSessionID = "d9c76e71-cbfe-41e2-b0f3-4a7d440deec1"
	require.NoError(t, mgr.BindSession(harp, fakeSessionID, ""))

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(t.TempDir(), ".ctxloom")})}

	_, out, err := s.loadOrDistillSession(context.Background(), fakeSessionID, "claude-code", "", true)
	require.NoError(t, err, "recovery must never block the agent (CLAUDE.md) — this degrades to a usable message, not a tool error")
	require.NotNil(t, out)
	assert.False(t, out.Loaded)
	assert.Contains(t, out.Message, harp, "message must name the harp")
	assert.Contains(t, out.Message, "ctxloom session backfill", "message must name the concrete remedy")
}

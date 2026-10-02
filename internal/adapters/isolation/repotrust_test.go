package isolation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// trustedByHuman makes cwd a repository the human's own claude trusts:
// projects[cwd].hasTrustDialogAccepted in the host's ~/.claude.json.
func trustedByHuman(t *testing.T, home, cwd string, accepted bool) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o755))
	data, err := json.Marshal(map[string]any{"projects": map[string]any{cwd: map[string]any{"hasTrustDialogAccepted": accepted}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), data, 0o600))
}

// sessionHomeTrust reads the trust answer the session home was written for
// cwd, and whether one was written at all.
func sessionHomeTrust(t *testing.T, home, cwd string) (bool, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(claudeHome(home, harpA), ".claude.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	projects, _ := cfg["projects"].(map[string]any)
	entry, ok := projects[cwd].(map[string]any)
	if !ok {
		return false, false
	}
	v, ok := entry["hasTrustDialogAccepted"].(bool)
	return v, ok
}

// A repository the human trusted in their own claude is trusted for the
// run: the placement carries the verdict and the session home carries the
// answer, so claude does not re-ask what the human already settled.
func TestRepoTrust_TheHumansAnswerTrustsTheRun(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	trustedByHuman(t, home, cwd, true)

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, hostRelocator{})
	assert.Equal(t, engine.TrustTrusted, pl.Trust)
	accepted, written := sessionHomeTrust(t, home, cwd)
	assert.True(t, written && accepted, "the trusted repository's answer is carried into the session home")
}

// A repository the human never trusted is untrusted for the run, and
// ctxloom writes no trust answer for it: claude's prompt is the human's.
func TestRepoTrust_NoAnswerIsUntrustedAndAnswersNothing(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	trustedByHuman(t, home, cwd, false)

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, hostRelocator{})
	assert.Equal(t, engine.TrustUntrusted, pl.Trust)
	accepted, _ := sessionHomeTrust(t, home, cwd)
	assert.False(t, accepted, "ctxloom answered claude's trust prompt for an untrusted repository")
}

// A run on the engine's REAL home has no session home to answer in, but
// still takes the verdict: the launch flags depend on it all the same.
func TestRepoTrust_AHostHomeRunStillTakesTheVerdict(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	trustedByHuman(t, home, cwd, true)

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeHost), cwd, hostRelocator{})
	assert.Equal(t, engine.TrustTrusted, pl.Trust)
}

// A preview reports the verdict the run would take.
func TestRepoTrust_APreviewReportsTheVerdict(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)
	trustedByHuman(t, home, s.project, true)

	stores, err := stageStores(s.backend(), s.creds.Stores)
	require.NoError(t, err)
	pl, _, err := hostRelocator{}.relocate(previewLayout(s, stores))
	require.NoError(t, err)
	assert.Equal(t, engine.TrustTrusted, pl.Trust)
}

// PrepareInstanceHome hands the engine the verdict it was given.
func TestPrepareInstanceHome_HandsTheEngineTheVerdict(t *testing.T) {
	withFakeHome(t)
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)
	for _, v := range []engine.WorkspaceTrust{engine.TrustUntrusted, engine.TrustTrusted} {
		_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir(), Trust: v})
		require.NoError(t, err)
	}
	seen := rec.seen()
	require.Len(t, seen, 2)
	assert.Equal(t, engine.TrustUntrusted, seen[0].Trust)
	assert.Equal(t, engine.TrustTrusted, seen[1].Trust)
}

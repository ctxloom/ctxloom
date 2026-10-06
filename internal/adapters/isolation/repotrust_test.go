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
	pl, _, err := hostRelocator{}.relocate(previewLayout(s, hostRelocator{}, stores, false))
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

// The trust answer the session home carries is keyed by the directory the
// AGENT works in — the project root as the child sees it — not by the
// controller's path for it: claude looks its trust entry up by its own cwd,
// so an answer keyed by any other path is never found and the run re-asks.
func TestRepoTrust_TheSessionHomeAnswerIsKeyedByTheAgentsView(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	trustedByHuman(t, home, cwd, true)

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, containerOf)
	agentView := pl.Paths.Paths().ProjectRoot.Engine
	require.NotEqual(t, cwd, agentView, "the fake runtime places the project elsewhere")
	accepted, written := sessionHomeTrust(t, home, agentView)
	assert.True(t, written && accepted, "the answer is keyed by the agent's view of the project")
	_, wrongKey := sessionHomeTrust(t, home, cwd)
	assert.False(t, wrongKey, "no answer is keyed by the controller's path, which the agent never uses")
}

// layerRuntime is fakeRuntime as one of its daemon's containers: its own
// layer names a controller path in host space as primary says.
type layerRuntime struct {
	fakeRuntime
	layer Layer
}

func (r layerRuntime) primary() Layer { return r.layer }

// A controller inside a container reads the human's answer under the
// project's HOST name — the human answered on the host — while the session
// home's answer is keyed by the agent's view.
func TestRepoTrust_AContainerizedControllerLooksUpTheHostName(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o755))
	data, err := json.Marshal(map[string]any{"projects": map[string]any{"/host" + cwd: map[string]any{"hasTrustDialogAccepted": true}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), data, 0o600))

	rt := layerRuntime{fakeRuntime: fakeRuntime{name: "docker", available: true}, layer: Layer{mounts: []LayerMount{{Host: "/host", View: "/"}}}}
	r := containerRelocator{rt: rt, instanceHome: defaultContainerInstanceHome, home: defaultContainerHome}
	pl, mounts := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, r)
	assert.Equal(t, engine.TrustTrusted, pl.Trust, "the human's answer is found under the host name")
	accepted, written := sessionHomeTrust(t, home, pl.Paths.Paths().ProjectRoot.Engine)
	assert.True(t, written && accepted)
	require.NotEmpty(t, mounts)

	plain := containerRelocator{rt: fakeRuntime{name: "docker", available: true}, instanceHome: defaultContainerInstanceHome, home: defaultContainerHome}
	pl, _ = placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, plain)
	assert.Equal(t, engine.TrustUntrusted, pl.Trust, "under the controller's own name the answer is not there")
}

// A HOST-runtime child of a containerized controller runs in the controller's
// own container, and ctxloom mounts nothing for it: the child's config home is
// the session home, at one path in both views, and the .claude.json it reads
// there is generated from the controller process's OWN ~/.claude.json. That
// file was written by a claude running in the same view (ctxloom never mounts
// a host ~/.claude.json into any container), so the human's answers in it are
// keyed by the controller's names, and the lookup takes them unreversed
// (hostRelocator.primary is HostLayer).
func TestRepoTrust_AHostRuntimeChildReadsTheControllersOwnHome(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	trustedByHuman(t, home, cwd, true)

	pl, mounts := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, hostRelocator{})
	assert.Empty(t, mounts, "a host-runtime child is given no mount: every file it reads is the controller process's own")
	sh := pl.Paths.Paths().SessionHome
	assert.Equal(t, claudeHome(home, harpA), sh.Host)
	assert.Equal(t, sh.Host, sh.Engine, "the child names its config home as the controller does")
	assert.Contains(t, pl.Env, "CLAUDE_CONFIG_DIR")
	assert.Equal(t, sh.Engine, pl.Env["CLAUDE_CONFIG_DIR"], "the .claude.json the child reads is the session home's")
	assert.Equal(t, engine.TrustTrusted, pl.Trust, "the answer recorded under the controller's own name is found")
	accepted, written := sessionHomeTrust(t, home, cwd)
	assert.True(t, written && accepted, "the child's .claude.json carries it under the cwd it runs in")
}

// The converse: an answer recorded only under some other name for the
// project (a host name the controller's container never sees) is not the
// host-runtime child's — the lookup does not reverse.
func TestRepoTrust_AHostRuntimeChildDoesNotReverseToHostNames(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o755))
	data, err := json.Marshal(map[string]any{"projects": map[string]any{"/host" + cwd: map[string]any{"hasTrustDialogAccepted": true}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), data, 0o600))

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, hostRelocator{})
	assert.Equal(t, engine.TrustUntrusted, pl.Trust)
}

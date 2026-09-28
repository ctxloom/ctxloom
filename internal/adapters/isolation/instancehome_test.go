package isolation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/spf13/afero"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// recordingInstanceConfig is a stand-in engine config writer: it records every
// request it is handed so a test can prove the ENGINE was actually reached with
// the right instance home and working directory — the engine write-config
// directive's whole point being that isolation orchestrates and the engine
// edits.
type recordingInstanceConfig struct {
	mu       sync.Mutex
	requests []engine.InstanceConfigRequest
	// inFlight/maxInFlight measure overlap, so the serialization test observes
	// serialization rather than asserting a lock file exists.
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	hold        time.Duration
	report      engine.InstanceConfigReport
	err         error
}

func (r *recordingInstanceConfig) WriteInstanceConfig(req engine.InstanceConfigRequest, _ afero.Fs) (engine.InstanceConfigReport, error) {
	n := r.inFlight.Add(1)
	for {
		max := r.maxInFlight.Load()
		if n <= max || r.maxInFlight.CompareAndSwap(max, n) {
			break
		}
	}
	if r.hold > 0 {
		time.Sleep(r.hold)
	}
	r.inFlight.Add(-1)

	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return r.report, r.err
}

func (r *recordingInstanceConfig) seen() []engine.InstanceConfigRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.InstanceConfigRequest(nil), r.requests...)
}

// withInstanceConfigWriter declares w as engine's config generator for the
// duration of the test (nil: none), leaving the engine's other facts as they
// are; the accessor is restored on cleanup.
func withInstanceConfigWriter(t *testing.T, name string, w engine.InstanceConfigWriter) {
	t.Helper()
	stageEngineFacts(t, name, func(f *EngineFacts) { f.Home.InstanceConfig = w })
}

// clearAuth unsets every var that can authenticate claude, so a test sets
// only the ones it names.
func clearAuth(t *testing.T) {
	t.Helper()
	a, ok := TokenAuthFor("claude-code")
	require.True(t, ok)
	for _, v := range append([]string{a.TokenVar}, a.EnvTriggers...) {
		t.Setenv(v, "")
	}
}

// TestPrepareInstanceHome_ReachesTheEngineWithTheInstanceAndWorkDir pins the
// engine write-config directive at the seam: the ENGINE is handed the host
// home, the instance home and the working directory, and isolation performs
// no byte-level edit of the engine's format on its own.
func TestPrepareInstanceHome_ReachesTheEngineWithTheInstanceAndWorkDir(t *testing.T) {
	home := withFakeHome(t)
	clearAuth(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-x")
	rec := &recordingInstanceConfig{report: engine.InstanceConfigReport{
		Wrote:    []string{"/generated/.claude.json"},
		Warnings: []string{"the host .claude.json carries no \"hasCompletedOnboarding\""},
	}}
	withInstanceConfigWriter(t, "claude-code", rec)

	instance := t.TempDir()
	workDir := t.TempDir()
	report, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: workDir})
	require.NoError(t, err)

	require.Len(t, rec.seen(), 1, "the engine must be asked exactly once")
	got := rec.seen()[0]
	assert.Equal(t, home, got.HostHome, "the engine reads the real host home through the caller's one resolution")
	assert.Equal(t, instance, got.InstanceHome)
	assert.Equal(t, workDir, got.WorkDir, "the trust target is the run's working directory")

	assert.Equal(t, []string{"/generated/.claude.json"}, report.Generated)
	assert.Len(t, report.Warnings, 1, "the engine's fail-loud notices reach the caller, not just stderr")
	assert.False(t, report.Unauthenticated)
}

// With nothing in the env to authenticate, the home is refused, and the
// reason names every fix that works: mint and store a setup-token, set an
// API var, or select the real home on the binding. No config is generated
// for a home the caller is about to refuse. A login in the host's own
// ~/.claude does not count: it is never copied.
func TestPrepareInstanceHome_NoAuthVarRefusesAndNamesTheFixes(t *testing.T) {
	home := withFakeHome(t)
	clearAuth(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{}}`), 0o600))
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	report, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.Unauthenticated)
	for _, want := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "claude setup-token", "ctxloom auth set-token", "ANTHROPIC_API_KEY", "engine_home: host"} {
		assert.Contains(t, report.Reason, want)
	}
	assert.Empty(t, rec.seen(), "no config is generated for an instance the caller will refuse")
}

// A run that shares the human's login needs no auth var: the same empty env
// that refuses above prepares the home, config and all, and places no
// credential in it.
func TestPrepareInstanceHome_SharedLoginNeedsNoAuthVar(t *testing.T) {
	withFakeHome(t)
	clearAuth(t)
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)
	instance := t.TempDir()

	report, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir(), SharedLogin: true})
	require.NoError(t, err)
	assert.False(t, report.Unauthenticated)
	assert.Len(t, rec.seen(), 1, "the engine's config is still generated")
	assert.NoFileExists(t, filepath.Join(instance, ".credentials.json"))
}

// Any one auth var authenticates the home, and the engine's config is still
// generated: auth riding the env says nothing about onboarding or trust.
func TestPrepareInstanceHome_AnyAuthVarAuthenticates(t *testing.T) {
	withFakeHome(t)
	a, ok := TokenAuthFor("claude-code")
	require.True(t, ok)
	for _, v := range append([]string{a.TokenVar}, a.EnvTriggers...) {
		t.Run(v, func(t *testing.T) {
			clearAuth(t)
			t.Setenv(v, "1")
			rec := &recordingInstanceConfig{}
			withInstanceConfigWriter(t, "claude-code", rec)
			report, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
			require.NoError(t, err)
			assert.False(t, report.Unauthenticated)
			assert.Len(t, rec.seen(), 1)
		})
	}
}

// An engine the facts accessor does not know cannot be prepared for.
// Silently succeeding would report a prepared instance that had nothing done
// to it.
func TestPrepareInstanceHome_UnregisteredEngineIsAnError(t *testing.T) {
	_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "acp", InstanceHome: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a composed engine")
}

// Two runs WITHIN one session (a coordinator and its in-tree delegated child,
// which inherits the harp) share ONE instance home, and both load-modify-write
// the same config file. Without the lock their reads and writes interleave and
// one run's generated content is lost.
//
// MUTATION TARGET: drop the lockInstanceHome call in PrepareInstanceHome and
// this goes red — the recorder observes two generations in flight at once.
func TestPrepareInstanceHome_SerializesTwoRunsSharingOneInstance(t *testing.T) {
	withFakeHome(t)
	clearAuth(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-x")
	rec := &recordingInstanceConfig{hold: 60 * time.Millisecond}
	withInstanceConfigWriter(t, "claude-code", rec)

	// The real instance shape: the session's home member under the ctxloom
	// home, which is what the lock is keyed on.
	project := t.TempDir()
	instance, err := paths.HarpSessionEngineHomes("ugly-icy-squid")
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cerr := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: project})
			assert.NoError(t, cerr)
		}()
	}
	wg.Wait()

	require.Len(t, rec.seen(), 2, "both runs must have prepared the shared instance")
	assert.Equal(t, int32(1), rec.maxInFlight.Load(),
		"two runs sharing one session instance must serialize; %d were generating at once", rec.maxInFlight.Load())
}

// claude's .claude.json lands in the session home through the engine's own
// writer, owner-only, carrying the account identity and never the user's own
// registrations. No credential file lands beside it, however complete the
// host's own login is: the run authenticates from its env.
func TestPrepareInstanceHome_WritesClaudesConfigAndNoCredential(t *testing.T) {
	home := withFakeHome(t)
	clearAuth(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-x")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"acc","refreshToken":"ref"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"hasCompletedOnboarding":true,"oauthAccount":{"emailAddress":"user@example.com"},"mcpServers":{"x":{"command":"secret"}}}`), 0o600))

	instance := t.TempDir()
	_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)

	cfgPath := filepath.Join(instance, ".claude.json")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	assert.Equal(t, map[string]any{"emailAddress": "user@example.com"}, cfg["oauthAccount"])
	assert.NotContains(t, cfg, "mcpServers", "the user's own registrations never cross")
	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	_, err = os.Lstat(filepath.Join(instance, ".credentials.json"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no credential is ever copied into a session home")
}

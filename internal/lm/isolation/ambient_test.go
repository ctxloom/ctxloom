package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// errAssertProjector is the sentinel a fakeCredentialProjector returns to prove
// a projection failure fails the copy.
var errAssertProjector = errors.New("projector refused this credential")

// recordingInstanceConfig is a stand-in engine config writer: it records every
// request it is handed so a test can prove the ENGINE was actually reached with
// the right instance home and working directory — the engine write-config
// directive's whole point being that isolation orchestrates and the engine
// edits.
type recordingInstanceConfig struct {
	mu       sync.Mutex
	requests []agent.InstanceConfigRequest
	// inFlight/maxInFlight measure overlap, so the serialization test observes
	// serialization rather than asserting a lock file exists.
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	hold        time.Duration
	report      agent.InstanceConfigReport
	err         error
}

func (r *recordingInstanceConfig) WriteInstanceConfig(req agent.InstanceConfigRequest) (agent.InstanceConfigReport, error) {
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

func (r *recordingInstanceConfig) seen() []agent.InstanceConfigRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.InstanceConfigRequest(nil), r.requests...)
}

// withInstanceConfigWriter installs w as engine's generator for the duration of
// the test and restores whatever was registered before. The registry is
// process-global (internal/lm/backends populates it at init, and its external
// test package links backends into this test binary), so a test that replaced
// an entry and left it replaced would silently reshape every later test.
func withInstanceConfigWriter(t *testing.T, engine string, w agent.InstanceConfigWriter) {
	t.Helper()
	instanceConfigMu.Lock()
	prev, had := instanceConfigWriters[engine]
	instanceConfigMu.Unlock()
	RegisterInstanceConfigWriter(engine, w)
	t.Cleanup(func() {
		if had {
			RegisterInstanceConfigWriter(engine, prev)
			return
		}
		RegisterInstanceConfigWriter(engine, nil)
	})
}

// fakeCredentialProjector records what it was handed and returns a fixed
// replacement (or an error), so a test can prove CopyAmbient routes the
// credential copy THROUGH the engine's projector rather than writing the host
// bytes raw.
type fakeCredentialProjector struct {
	mu            sync.Mutex
	seenDestNames []string
	seenBytes     [][]byte
	replaceWith   []byte
	err           error
}

func (f *fakeCredentialProjector) ProjectAmbientCredential(destName string, hostBytes []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenDestNames = append(f.seenDestNames, destName)
	f.seenBytes = append(f.seenBytes, append([]byte(nil), hostBytes...))
	if f.err != nil {
		return nil, f.err
	}
	if f.replaceWith != nil {
		return f.replaceWith, nil
	}
	return hostBytes, nil
}

// withCredentialProjector installs p as engine's projector for the test and
// restores whatever was registered before (backends registers claude's real
// one at init, so a test that replaced it and left it would reshape later ones).
func withCredentialProjector(t *testing.T, engine string, p agent.CredentialProjector) {
	t.Helper()
	credentialProjectorMu.Lock()
	prev, had := credentialProjectors[engine]
	credentialProjectorMu.Unlock()
	RegisterCredentialProjector(engine, p)
	t.Cleanup(func() {
		if had {
			RegisterCredentialProjector(engine, prev)
			return
		}
		RegisterCredentialProjector(engine, nil)
	})
}

// TestCopyAmbient_RoutesCredentialThroughTheEngineProjector pins the seam: the
// ambient credential copy passes the host bytes through the ENGINE's projector
// (claude's refresh-token strip) and writes the PROJECTED result, never the host
// bytes raw. The projector is keyed by the engine's own destName.
func TestCopyAmbient_RoutesCredentialThroughTheEngineProjector(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`), 0o600))

	proj := &fakeCredentialProjector{replaceWith: []byte(`{"projected":true}`)}
	withCredentialProjector(t, "claude-code", proj)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})

	instance := t.TempDir()
	_, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)

	require.Equal(t, []string{".credentials.json"}, proj.seenDestNames, "the projector is keyed by the engine's own leaf")
	require.Len(t, proj.seenBytes, 1)
	assert.Contains(t, string(proj.seenBytes[0]), "refreshToken", "the projector is handed the FULL host bytes to project")

	seeded, err := os.ReadFile(filepath.Join(instance, "claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"projected":true}`, string(seeded), "the PROJECTED bytes are written, not the host bytes")
}

// TestCopyAmbient_ProjectorErrorFailsTheCopy: a projector that cannot sanitize
// the credential fails the whole copy loud rather than falling back to the
// unprojected host bytes — for claude that fallback would be the refresh-token
// leak the strip exists to prevent.
func TestCopyAmbient_ProjectorErrorFailsTheCopy(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)

	proj := &fakeCredentialProjector{err: errAssertProjector}
	withCredentialProjector(t, "claude-code", proj)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})

	instance := t.TempDir()
	_, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.Error(t, err, "a projection failure must fail the copy, not write the raw credential")

	_, statErr := os.Stat(filepath.Join(instance, "claude", ".credentials.json"))
	assert.True(t, os.IsNotExist(statErr), "no credential is written when projection fails")
}

// TestCopyAmbient_ClaudeStripsRefreshTokenEndToEnd exercises the REAL claude
// projector (registered by backends at init, linked into this test binary) all
// the way through CopyAmbient: the seeded copy is access-token-only.
//
// MUTATION TARGET (m1): don't strip refreshToken in internal/claude and this
// goes red — the seeded copy would still carry the host's single-use token.
func TestCopyAmbient_ClaudeStripsRefreshTokenEndToEnd(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"acc","refreshToken":"ref","expiresAt":1,"refreshTokenExpiresAt":2,"subscriptionType":"max"}}`), 0o600))
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})

	instance := t.TempDir()
	_, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)

	seeded, err := os.ReadFile(filepath.Join(instance, "claude", ".credentials.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(seeded, &cfg))
	oauth := cfg["claudeAiOauth"].(map[string]any)
	assert.NotContains(t, oauth, "refreshToken", "the seeded copy must be access-token-only")
	assert.NotContains(t, oauth, "refreshTokenExpiresAt")
	assert.Equal(t, "acc", oauth["accessToken"], "the access token still authenticates the run")
	assert.Equal(t, "max", oauth["subscriptionType"])

	// The host credential is UNTOUCHED — it still carries its refresh token.
	hostBytes, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Contains(t, string(hostBytes), "ref", "the real host credential must keep its refresh token")
}

// TestAmbientSet_IsAnExplicitAllowListPerEngine is the roster guard the plan
// asks for: every engine with a declared ambient set names its files ONE BY
// ONE, at owner-only mode, and kiro's set is empty by declaration rather than
// by being missing from the roster.
//
// The allow-list shape is what makes D4/D5 decisions rather than accidents: a
// deny-list would copy each new engine file by default, and the default
// direction of a mistake there is a confidentiality leak.
func TestAmbientSet_IsAnExplicitAllowListPerEngine(t *testing.T) {
	home := withFakeHome(t)

	names := AmbientEngineNames()
	sort.Strings(names)
	assert.Equal(t, []string{"claude-code", "codex", "kiro", "opencode"}, names,
		"every registered backend needs an EXPLICIT ambient declaration, empty or not")

	want := map[string][]AmbientFile{
		"claude-code": {{HostRel: ".claude/.credentials.json", DestRel: "claude/.credentials.json", Mode: 0o600, Required: true}},
		"codex":       {{HostRel: ".codex/auth.json", DestRel: ".codex/auth.json", Mode: 0o600, Required: true}},
		"kiro":        nil,
		"opencode": {
			{HostRel: ".local/share/opencode/auth.json", DestRel: "xdg-data/opencode/auth.json", Mode: 0o600, Required: true},
			{HostRel: ".local/share/opencode/mcp-auth.json", DestRel: "xdg-data/opencode/mcp-auth.json", Mode: 0o600, Required: false},
		},
	}
	for engine, files := range want {
		assert.Equal(t, files, AmbientSet(engine), "%s's ambient set", engine)
	}
	assert.Nil(t, AmbientSet("mock"), "an engine with no declaration has no ambient set")

	// The sets are resolved against the REAL host home seam, not a literal.
	claude := AmbientSet("claude-code")
	require.Len(t, claude, 1)
	assert.NotContains(t, claude[0].HostRel, home, "HostRel is home-RELATIVE, never an absolute host path")
}

// TestCopyAmbient_ReachesTheEngineWithTheInstanceAndWorkDir pins the engine
// write-config directive at the seam: CopyAmbient copies the allow-listed files
// itself and then hands the ENGINE the host home, the instance home and the
// working directory, performing no byte-level edit of the engine's format on
// its own.
func TestCopyAmbient_ReachesTheEngineWithTheInstanceAndWorkDir(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true)
	rec := &recordingInstanceConfig{report: agent.InstanceConfigReport{
		Wrote:    []string{"/generated/.claude.json"},
		Warnings: []string{"the host .claude.json carries no \"hasCompletedOnboarding\""},
	}}
	withInstanceConfigWriter(t, "claude-code", rec)

	instance := t.TempDir()
	workDir := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: workDir})
	require.NoError(t, err)

	require.Len(t, rec.seen(), 1, "the engine must be asked exactly once")
	got := rec.seen()[0]
	assert.Equal(t, home, got.HostHome, "the engine reads the real host home through the caller's one resolution")
	assert.Equal(t, instance, got.InstanceHome)
	assert.Equal(t, workDir, got.WorkDir, "the trust target is the run's working directory")

	assert.Equal(t, []string{"/generated/.claude.json"}, report.Generated)
	assert.Len(t, report.Warnings, 1, "the engine's fail-loud notices reach the caller, not just stderr")
	assert.Equal(t, 1, report.Copied)
	assert.False(t, report.NoSource)
}

// TestCopyAmbient_NoSourceSkipsGeneration: when there is nothing to
// authenticate with, the caller is about to refuse this instance outright — so
// generating a config for it would be work thrown away and a directory created
// for a home nobody will use.
func TestCopyAmbient_NoSourceSkipsGeneration(t *testing.T) {
	withFakeHome(t) // no host creds at all
	t.Setenv("ANTHROPIC_API_KEY", "")
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.NoSource)
	assert.Empty(t, rec.seen(), "no config is generated for an instance the caller will refuse")
}

// TestCopyAmbient_EnvTriggerStillGeneratesTheEngineConfig: an ANTHROPIC_API_KEY
// run needs no credential copy and STILL meets claude's onboarding and trust
// dialogs. The two halves of the copy-in are independent, and treating
// "skipped the credential" as "skipped everything" would re-prompt every
// API-key session.
func TestCopyAmbient_EnvTriggerStillGeneratesTheEngineConfig(t *testing.T) {
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.True(t, report.SkippedEnv)
	assert.Len(t, rec.seen(), 1, "auth riding the env says nothing about onboarding or trust")
}

// TestCopyAmbient_UnregisteredEngineIsAnError: an engine with no declared
// ambient set cannot be copied for. Silently succeeding would report a prepared
// instance that had nothing done to it.
func TestCopyAmbient_UnregisteredEngineIsAnError(t *testing.T) {
	_, err := CopyAmbient(AmbientRequest{Engine: "acp", InstanceHome: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no declared ambient set")
}

// TestCopyAmbient_Kiro_DeclaredEmptyCopiesNothing: kiro's ambient set is empty
// BY DESIGN (its credentials live in a global sqlite no home var relocates), so
// the copy-in moves no bytes and reports no fail-loud "nothing seedable" —
// there was never anything to seed.
func TestCopyAmbient_Kiro_DeclaredEmptyCopiesNothing(t *testing.T) {
	withFakeHome(t)
	t.Setenv("KIRO_API_KEY", "")
	instance := t.TempDir()

	report, err := CopyAmbient(AmbientRequest{Engine: "kiro", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.Zero(t, report.Copied)
	assert.False(t, report.NoSource, "an empty set is not a failed seed")
	assert.Nil(t, AmbientSet("kiro"))
}

// TestCopyAmbient_SerializesTwoRunsSharingOneInstance is the S5-flagged
// lock, the plan-1.6 item S5 skipped: two runs WITHIN one session (a
// coordinator and its in-tree delegated child, which inherits the harp) share
// ONE instance home, and both load-modify-write the same config file. Without
// the lock their reads and writes interleave and one run's generated content is
// lost.
//
// MUTATION TARGET: drop the lockInstanceHome call in CopyAmbient and this goes
// red — the recorder observes two generations in flight at once.
func TestCopyAmbient_SerializesTwoRunsSharingOneInstance(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	rec := &recordingInstanceConfig{hold: 60 * time.Millisecond}
	withInstanceConfigWriter(t, "claude-code", rec)

	// An instance home INSIDE a .ctxloom tree, which is what
	// paths.ProjectPathFor keys on — the real in-tree shape,
	// <project>/.ctxloom/state/<harp>/home.
	project := t.TempDir()
	instance, err := paths.SessionHomePath(filepath.Join(project, paths.AppDirName), "ugly-icy-squid")
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cerr := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: project})
			assert.NoError(t, cerr)
		}()
	}
	wg.Wait()

	require.Len(t, rec.seen(), 2, "both runs must have prepared the shared instance")
	assert.Equal(t, int32(1), rec.maxInFlight.Load(),
		"two runs sharing one session instance must serialize; %d were generating at once", rec.maxInFlight.Load())
}

// TestWorktreeAxis_RoutesThroughCopyAmbient is D8 made testable: the worktree
// axis's provisionConfigHome reaches the SAME mechanism the in-tree axis does,
// so claude's field-scoped .claude.json and codex's section elision apply to a
// fan-out member too. The LOCATION stays split — the instance home handed over
// is this axis's own per-agent config home, and the working directory is this
// member's own checkout, never the shared project root.
func TestWorktreeAxis_RoutesThroughCopyAmbient(t *testing.T) {
	resetStrictness(t)
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-ambient")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	seen := rec.seen()
	require.Len(t, seen, 1, "the worktree axis must reach the engine through CopyAmbient, not a second seeding path")
	assert.Equal(t, home, seen[0].HostHome)
	assert.Equal(t, ws.Dir(), seen[0].WorkDir,
		"the member's own checkout is what an engine-generated trust answer must name, never the shared project root")

	configDir := WorkspaceEnv(ws)["CLAUDE_CONFIG_DIR"]
	require.NotEmpty(t, configDir)
	assert.Equal(t, filepath.Dir(configDir), seen[0].InstanceHome,
		"the engine is handed the config-home ROOT and appends its own leaf, exactly as on the in-tree axis")
	assert.NotContains(t, filepath.ToSlash(seen[0].InstanceHome), "/"+paths.AppDirName+"/state/",
		"D8 shares the MECHANISM, not the location: a worktree member's home must not migrate into the project state tier")
}
